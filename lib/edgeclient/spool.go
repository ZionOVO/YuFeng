package edgeclient

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/kernel"
	"yufeng/lib/observability"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
)

// segmentWindow 是落盘分段的时间窗口：同一窗口内的事件追加进同一个
// 逐行 JavaScript 对象表示法文件，窗口滚动自动换新文件。按窗口而非按事件成文件，事件量大时
// 不会产生 inode 压力；窗口对齐墙时钟，重启后继续追加同窗口文件。
const segmentWindow = time.Hour

const (
	defaultSpoolMaxFiles = 64
	spoolBatchMax        = 100
)

// Spool 是事件遥测的落盘缓冲：事件追加到按时间窗口分段的逐行 JavaScript 对象表示法文件，
// 上传成功后整段删除。总字节与文件数有界，超限拒绝并计数。
//
// [逐行 JavaScript 对象表示法]: ../../docs/glossary.md#protocol-and-implementation-terms
type Spool struct {
	mu               sync.Mutex
	dir              string
	MaxBytes         int64
	MaxFiles         int
	Rejected         int
	Dropped          int
	bufferedCritical uint64
	bufferedOrdinary uint64
	droppedCritical  uint64
	droppedOrdinary  uint64
}

// NewSpool 创建落盘缓冲目录。
func NewSpool(dir string) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Spool{dir: dir, MaxBytes: kernel.EdgeTelemetrySpoolBytes, MaxFiles: defaultSpoolMaxFiles}
	files, err := s.Files()
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		events, err := s.ReadEvents(file)
		if err != nil {
			continue
		}
		critical, ordinary := eventClassCounts(events)
		s.bufferedCritical += critical
		s.bufferedOrdinary += ordinary
	}
	return s, nil
}

// Append 追加一个事件到当前时间窗口的分段文件。
func (s *Spool) Append(e *eventv1.Event) error {
	raw, err := protojson.Marshal(e)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.overLimitLocked(int64(len(raw))) {
		s.Rejected++
		s.Dropped++
		if isOrdinarySample(e) {
			s.droppedOrdinary++
		} else {
			s.droppedCritical++
		}
		observability.Default().Add(observability.MetricTelemetryDropped, 1)
		return fmt.Errorf("spool capacity exceeded")
	}
	name := fmt.Sprintf("events-%d.ndjson", time.Now().Truncate(segmentWindow).UnixNano())
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(raw)
	err = errors.Join(writeErr, f.Close())
	if err == nil {
		if isOrdinarySample(e) {
			s.bufferedOrdinary++
		} else {
			s.bufferedCritical++
		}
	}
	return err
}

// Files 返回按时间排序的待上传分段文件路径。
func (s *Spool) Files() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		// 只收逐行结构化事件分段；.corrupt 是隔离件，不重回上传队列。
		if !e.IsDir() && strings.HasPrefix(e.Name(), "events-") && strings.HasSuffix(e.Name(), ".ndjson") {
			files = append(files, filepath.Join(s.dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

// ReadEvents 读取一个分段文件中的事件。
func (s *Spool) ReadEvents(path string) ([]*eventv1.Event, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var events []*eventv1.Event
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var e eventv1.Event
		if err := protojson.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		events = append(events, &e)
	}
	return events, nil
}

// Remove 删除已确认上传的分段文件。
func (s *Spool) Remove(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	critical, ordinary := s.fileClassCounts(path)
	if err := os.Remove(path); err != nil {
		return err
	}
	s.removeBuffered(critical, ordinary)
	return nil
}

// Rewrite 用剩余未确认事件覆盖分段，实现事件级确认。
func (s *Spool) Rewrite(path string, events []*eventv1.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rewriteLocked(path, events)
}

// ResolveUpload 从上传队列移除已确认事件，保留暂时拒绝项，并把永久拒绝项写入隔离证据。
func (s *Spool) ResolveUpload(path string, retry, permanent []*eventv1.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(permanent) > 0 {
		quarantine := strings.TrimSuffix(path, ".ndjson") + ".corrupt"
		if err := appendEventsFile(quarantine, permanent); err != nil {
			return err
		}
	}
	return s.rewriteLocked(path, retry)
}

// rewriteLocked 在调用方持锁时原子替换分段内容，并同步内存计数。
func (s *Spool) rewriteLocked(path string, events []*eventv1.Event) error {
	oldCritical, oldOrdinary := s.fileClassCounts(path)
	if len(events) == 0 {
		if err := os.Remove(path); err != nil {
			return err
		}
		s.removeBuffered(oldCritical, oldOrdinary)
		return nil
	}
	tmp := path + ".tmp"
	if err := writeEventsFile(tmp, events); err != nil {
		removeErr := os.Remove(tmp)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		return errors.Join(err, removeErr)
	}
	if err := os.Rename(tmp, path); err != nil {
		removeErr := os.Remove(tmp)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		return errors.Join(err, removeErr)
	}
	s.removeBuffered(oldCritical, oldOrdinary)
	critical, ordinary := eventClassCounts(events)
	s.bufferedCritical += critical
	s.bufferedOrdinary += ordinary
	return nil
}

// writeEventsFile 把事件按逐行结构化对象格式完整写入指定文件。
func writeEventsFile(path string, events []*eventv1.Event) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	for _, e := range events {
		if e == nil {
			continue
		}
		raw, err := protojson.Marshal(e)
		if err != nil {
			return errors.Join(err, f.Close())
		}
		if _, err := f.Write(append(raw, '\n')); err != nil {
			return errors.Join(err, f.Close())
		}
	}
	return f.Close()
}

// appendEventsFile 把事件按逐行结构化对象格式追加到指定文件。
func appendEventsFile(path string, events []*eventv1.Event) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	for _, e := range events {
		if e == nil {
			continue
		}
		raw, err := protojson.Marshal(e)
		if err != nil {
			return errors.Join(err, f.Close())
		}
		if _, err := f.Write(append(raw, '\n')); err != nil {
			return errors.Join(err, f.Close())
		}
	}
	return f.Close()
}

// Quarantine 把无法解析的分段移出上传队列但保留在磁盘：半写文件既不可
// 恢复也不该删除（删除即丢遥测证据），留在队列里则会永远重试。
func (s *Spool) Quarantine(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	critical, ordinary := s.fileClassCounts(path)
	if err := os.Rename(path, strings.TrimSuffix(path, ".ndjson")+".corrupt"); err != nil {
		return err
	}
	s.removeBuffered(critical, ordinary)
	return nil
}

// ProductionStats 返回关键事件与普通样本的缓冲、丢弃计数。
func (s *Spool) ProductionStats() (bufferedCritical, bufferedOrdinary, droppedCritical, droppedOrdinary uint64) {
	if s == nil {
		return 0, 0, 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bufferedCritical, s.bufferedOrdinary, s.droppedCritical, s.droppedOrdinary
}

// fileClassCounts 读取一个分段并统计关键事件与普通样本数量。
func (s *Spool) fileClassCounts(path string) (uint64, uint64) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	var events []*eventv1.Event
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var event eventv1.Event
		if protojson.Unmarshal([]byte(line), &event) == nil {
			events = append(events, &event)
		}
	}
	return eventClassCounts(events)
}

// removeBuffered 从内存缓冲计数中安全扣除已经移出的事件。
func (s *Spool) removeBuffered(critical, ordinary uint64) {
	if critical > s.bufferedCritical {
		critical = s.bufferedCritical
	}
	if ordinary > s.bufferedOrdinary {
		ordinary = s.bufferedOrdinary
	}
	s.bufferedCritical -= critical
	s.bufferedOrdinary -= ordinary
}

// eventClassCounts 统计事件集合中的关键事件与普通样本数量。
func eventClassCounts(events []*eventv1.Event) (critical, ordinary uint64) {
	for _, event := range events {
		if isOrdinarySample(event) {
			ordinary++
		} else if event != nil {
			critical++
		}
	}
	return critical, ordinary
}

// isOrdinarySample 判断事件是否为无发现、无潜在拦截的普通放行样本。
func isOrdinarySample(event *eventv1.Event) bool {
	return event != nil && event.GetVerdict() == eventv1.Verdict_VERDICT_ALLOW &&
		len(event.GetDetections()) == 0 && !event.GetWouldHaveBlocked() &&
		event.GetObservation() == commonv1.ObservationState_OBSERVATION_STATE_SYNC_NO_DETECTION
}

// overLimitLocked 判断新增字节是否会使缓冲目录超过文件数或总大小上限。
func (s *Spool) overLimitLocked(extra int64) bool {
	maxBytes := s.MaxBytes
	if maxBytes <= 0 {
		maxBytes = kernel.EdgeTelemetrySpoolBytes
	}
	maxFiles := s.MaxFiles
	if maxFiles <= 0 {
		maxFiles = defaultSpoolMaxFiles
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return false
	}
	var size int64
	files := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "events-") {
			continue
		}
		files++
		if info, err := e.Info(); err == nil {
			size += info.Size()
		}
	}
	return files > maxFiles || size+extra > maxBytes
}

// BatchLimit 是每批最多上传事件数。
func BatchLimit() int { return spoolBatchMax }
