package edgeclient

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

const (
	reviewSpoolMaxBytes          = 64 << 20
	reviewCandidateSpoolMaxBytes = 16 << 20
	reviewSpoolQuarantineTTL     = 24 * time.Hour
)

type reviewFrame struct {
	Version       int               `json:"version"`
	Kind          string            `json:"kind"`
	Records       []json.RawMessage `json:"records"`
	PayloadDigest string            `json:"payload_digest"`
}

// ReviewSpool 以校验帧和原子重命名持久化冻结统计窗与脱敏候选。
type ReviewSpool struct {
	mu  sync.Mutex
	dir string
}

// NewReviewSpool 创建流量审查上传缓冲，并清理不占生产额度的过期隔离帧。
func NewReviewSpool(dir string) (*ReviewSpool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	spool := &ReviewSpool{dir: dir}
	if err := spool.cleanupQuarantineLocked(time.Now()); err != nil {
		return nil, err
	}
	return spool, nil
}

// AppendWindows 原子持久化一个统计窗快照；相同内容重复写入保持幂等。
func (s *ReviewSpool) AppendWindows(values []*telemetryv1.TrafficWindow) error {
	messages := make([]proto.Message, 0, len(values))
	for _, value := range values {
		if value != nil {
			messages = append(messages, value)
		}
	}
	return s.append("windows", messages)
}

// AppendCandidates 原子持久化一个候选快照；候选使用独立额度。
func (s *ReviewSpool) AppendCandidates(values []*telemetryv1.ReviewCandidate) error {
	messages := make([]proto.Message, 0, len(values))
	for _, value := range values {
		if value != nil {
			messages = append(messages, value)
		}
	}
	return s.append("candidates", messages)
}

// ReplaceWindows 用仍需重试的逐项结果替换原帧；空集合表示全部确认。
func (s *ReviewSpool) ReplaceWindows(path string, values []*telemetryv1.TrafficWindow) error {
	return s.replace(path, "windows", trafficWindowMessages(values))
}

// ReplaceCandidates 用仍需重试的逐项结果替换原帧；空集合表示全部确认。
func (s *ReviewSpool) ReplaceCandidates(path string, values []*telemetryv1.ReviewCandidate) error {
	return s.replace(path, "candidates", reviewCandidateMessages(values))
}

// QuarantineWindows 把被永久拒绝的统计窗写成不占生产额度的独立帧。
func (s *ReviewSpool) QuarantineWindows(values []*telemetryv1.TrafficWindow) error {
	return s.quarantineMessages("windows", trafficWindowMessages(values))
}

// QuarantineCandidates 把被永久拒绝的候选写成不占生产额度的独立帧。
func (s *ReviewSpool) QuarantineCandidates(values []*telemetryv1.ReviewCandidate) error {
	return s.quarantineMessages("candidates", reviewCandidateMessages(values))
}

func trafficWindowMessages(values []*telemetryv1.TrafficWindow) []proto.Message {
	messages := make([]proto.Message, 0, len(values))
	for _, value := range values {
		if value != nil {
			messages = append(messages, value)
		}
	}
	return messages
}

func reviewCandidateMessages(values []*telemetryv1.ReviewCandidate) []proto.Message {
	messages := make([]proto.Message, 0, len(values))
	for _, value := range values {
		if value != nil {
			messages = append(messages, value)
		}
	}
	return messages
}

func (s *ReviewSpool) replace(path, kind string, values []proto.Message) error {
	if len(values) == 0 {
		return s.Remove(path)
	}
	records, err := marshalReviewMessages(values)
	if err != nil {
		return err
	}
	digest := digestReviewRecords(records)
	frameRaw, err := json.Marshal(reviewFrame{Version: 1, Kind: kind, Records: records, PayloadDigest: digest})
	if err != nil {
		return err
	}
	directory, err := filepath.Abs(s.dir)
	if err != nil {
		return err
	}
	oldPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	oldName := filepath.Base(oldPath)
	if filepath.Dir(oldPath) != directory || !strings.HasPrefix(oldName, kind+"-") || !strings.HasSuffix(oldName, ".frame") {
		return errors.New("review spool replacement path is invalid")
	}
	target := filepath.Join(directory, kind+"-"+strings.TrimPrefix(digest, "sha256:")+".frame")
	if oldPath == target {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(target); err == nil {
		if err := os.Remove(oldPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncReviewDirectory(directory)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	oldInfo, err := os.Stat(oldPath)
	if err != nil {
		return err
	}
	if over, err := s.overLimitLocked(kind, int64(len(frameRaw))-oldInfo.Size()); err != nil {
		return err
	} else if over {
		return errors.New("review spool capacity exceeded")
	}
	if err := writeReviewFrameLocked(directory, target, frameRaw); err != nil {
		return err
	}
	if err := os.Remove(oldPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncReviewDirectory(directory)
}

func (s *ReviewSpool) quarantineMessages(kind string, values []proto.Message) error {
	if len(values) == 0 {
		return nil
	}
	records, err := marshalReviewMessages(values)
	if err != nil {
		return err
	}
	digest := digestReviewRecords(records)
	frameRaw, err := json.Marshal(reviewFrame{Version: 1, Kind: kind, Records: records, PayloadDigest: digest})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, kind+"-"+strings.TrimPrefix(digest, "sha256:")+".rejected")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeReviewFrameLocked(s.dir, path, frameRaw)
}

func (s *ReviewSpool) append(kind string, values []proto.Message) error {
	if len(values) == 0 {
		return nil
	}
	records, err := marshalReviewMessages(values)
	if err != nil {
		return err
	}
	digest := digestReviewRecords(records)
	frameRaw, err := json.Marshal(reviewFrame{Version: 1, Kind: kind, Records: records, PayloadDigest: digest})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, kind+"-"+strings.TrimPrefix(digest, "sha256:")+".frame")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if over, err := s.overLimitLocked(kind, int64(len(frameRaw))); err != nil {
		return err
	} else if over {
		return errors.New("review spool capacity exceeded")
	}
	return writeReviewFrameLocked(s.dir, path, frameRaw)
}

func writeReviewFrameLocked(directory, path string, frameRaw []byte) error {
	temporary, err := os.CreateTemp(directory, ".review-frame-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck // Rename 成功后临时路径已不存在。
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	written, writeErr := temporary.Write(frameRaw)
	if writeErr == nil && written != len(frameRaw) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return errors.Join(writeErr, temporary.Close())
	}
	if err := temporary.Sync(); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncReviewDirectory(directory)
}

func marshalReviewMessages(values []proto.Message) ([]json.RawMessage, error) {
	records := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		raw, err := protojson.Marshal(value)
		if err != nil {
			return nil, err
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return nil, err
		}
		records = append(records, append(json.RawMessage(nil), compact.Bytes()...))
	}
	return records, nil
}

func digestReviewRecords(records []json.RawMessage) string {
	digest := sha256.New()
	var length [8]byte
	for _, record := range records {
		binary.BigEndian.PutUint64(length[:], uint64(len(record)))
		digest.Write(length[:])
		digest.Write(record)
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

// WindowFiles 返回待上传统计窗帧。
func (s *ReviewSpool) WindowFiles() ([]string, error) { return s.files("windows") }

// CandidateFiles 返回待上传候选帧。
func (s *ReviewSpool) CandidateFiles() ([]string, error) { return s.files("candidates") }

func (s *ReviewSpool) files(kind string) ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), kind+"-") && strings.HasSuffix(entry.Name(), ".frame") {
			out = append(out, filepath.Join(s.dir, entry.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// ReadWindows 校验并读取一个统计窗帧。
func (s *ReviewSpool) ReadWindows(path string) ([]*telemetryv1.TrafficWindow, error) {
	records, err := readReviewFrame(path, "windows")
	if err != nil {
		return nil, err
	}
	out := make([]*telemetryv1.TrafficWindow, 0, len(records))
	for _, raw := range records {
		value := &telemetryv1.TrafficWindow{}
		if err := protojson.Unmarshal(raw, value); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, value)
	}
	return out, nil
}

// ReadCandidates 校验并读取一个候选帧。
func (s *ReviewSpool) ReadCandidates(path string) ([]*telemetryv1.ReviewCandidate, error) {
	records, err := readReviewFrame(path, "candidates")
	if err != nil {
		return nil, err
	}
	out := make([]*telemetryv1.ReviewCandidate, 0, len(records))
	for _, raw := range records {
		value := &telemetryv1.ReviewCandidate{}
		if err := protojson.Unmarshal(raw, value); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, value)
	}
	return out, nil
}

func readReviewFrame(path, kind string) ([]json.RawMessage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var frame reviewFrame
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frame); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("%s: trailing review spool data: %w", path, err)
	}
	if frame.Version != 1 || frame.Kind != kind || len(frame.Records) == 0 || frame.PayloadDigest != digestReviewRecords(frame.Records) {
		return nil, fmt.Errorf("%s: review spool frame checksum mismatch", path)
	}
	return frame.Records, nil
}

// Remove 删除已逐项确认的帧并同步目录元数据。
func (s *ReviewSpool) Remove(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncReviewDirectory(s.dir)
}

// Quarantine 隔离被服务端永久拒绝的帧；隔离文件不占生产额度。
func (s *ReviewSpool) Quarantine(path string) error { return s.quarantine(path, ".rejected") }

// QuarantineCorrupt 隔离校验失败的帧；其他完整帧仍可继续上传。
func (s *ReviewSpool) QuarantineCorrupt(path string) error { return s.quarantine(path, ".corrupt") }

func (s *ReviewSpool) quarantine(path, suffix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := strings.TrimSuffix(path, ".frame") + suffix
	if err := os.Rename(path, target); err != nil {
		return err
	}
	return syncReviewDirectory(s.dir)
}

func (s *ReviewSpool) overLimitLocked(kind string, extra int64) (bool, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return false, err
	}
	var used, candidateUsed int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".frame") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return false, err
		}
		used += info.Size()
		if strings.HasPrefix(entry.Name(), "candidates-") {
			candidateUsed += info.Size()
		}
	}
	if kind == "candidates" && candidateUsed+extra > reviewCandidateSpoolMaxBytes {
		return true, nil
	}
	return used+extra > reviewSpoolMaxBytes, nil
}

func (s *ReviewSpool) cleanupQuarantineLocked(now time.Time) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".rejected") && !strings.HasSuffix(entry.Name(), ".corrupt")) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) >= reviewSpoolQuarantineTTL {
			if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func syncReviewDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
