package edgeclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/observability"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
)

func testEvent(id string) *eventv1.Event {
	return &eventv1.Event{Id: id, OccurredAt: timestamppb.Now(), AssetId: "a-1", Source: "test", Kind: eventv1.Kind_KIND_TRAFFIC}
}

// 追加 → 列表 → 读取 → 删除 的完整循环；同一时间窗口内的事件
// 必须落在同一个分段文件里。
func TestSpoolRoundTrip(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"evt-1", "evt-2", "evt-3"} {
		if err := s.Append(testEvent(id)); err != nil {
			t.Fatal(err)
		}
	}
	files, err := s.Files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("同窗口事件应合并为一个分段，实际 %d 个文件", len(files))
	}
	events, err := s.ReadEvents(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Id != "evt-1" || events[2].Id != "evt-3" {
		t.Fatalf("事件还原异常: %+v", events)
	}
	if err := s.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	if files, _ := s.Files(); len(files) != 0 {
		t.Fatalf("删除后仍有 %d 个分段", len(files))
	}
}

// 损坏分段隔离后不再出现在上传队列里，但文件保留在磁盘上。
func TestSpoolRejectsWhenOverCapacity(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.MaxBytes = 32
	if err := s.Append(testEvent("evt-huge")); err == nil {
		t.Fatal("over capacity must reject")
	}
	if s.Rejected == 0 {
		t.Fatal("rejected counter")
	}
	if observability.Default().Get(observability.MetricTelemetryDropped) < 1 {
		t.Fatal("drop must increment prometheus counter")
	}
}

func TestSpoolProductionStatsDistinguishCriticalAndOrdinary(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	critical := testEvent("evt-critical")
	ordinary := testEvent("evt-ordinary")
	ordinary.Verdict = eventv1.Verdict_VERDICT_ALLOW
	ordinary.Observation = commonv1.ObservationState_OBSERVATION_STATE_SYNC_NO_DETECTION
	if err := s.Append(critical); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ordinary); err != nil {
		t.Fatal(err)
	}
	bufferedCritical, bufferedOrdinary, droppedCritical, droppedOrdinary := s.ProductionStats()
	if bufferedCritical != 1 || bufferedOrdinary != 1 || droppedCritical != 0 || droppedOrdinary != 0 {
		t.Fatalf("production stats=%d,%d,%d,%d", bufferedCritical, bufferedOrdinary, droppedCritical, droppedOrdinary)
	}

	s.MaxBytes = 1
	if err := s.Append(ordinary); err == nil {
		t.Fatal("ordinary sample over capacity must reject")
	}
	_, _, droppedCritical, droppedOrdinary = s.ProductionStats()
	if droppedCritical != 0 || droppedOrdinary != 1 {
		t.Fatalf("drop stats=%d,%d", droppedCritical, droppedOrdinary)
	}
	files, err := s.Files()
	if err != nil || len(files) != 1 {
		t.Fatalf("spool files=%d err=%v", len(files), err)
	}
	if err := s.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	bufferedCritical, bufferedOrdinary, _, _ = s.ProductionStats()
	if bufferedCritical != 0 || bufferedOrdinary != 0 {
		t.Fatalf("removed segment still buffered=%d,%d", bufferedCritical, bufferedOrdinary)
	}
}

func TestSpoolQuarantineKeepsButExcludes(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(testEvent("evt-1")); err != nil {
		t.Fatal(err)
	}
	files, err := s.Files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("期望 1 个分段，实际 %d", len(files))
	}
	if err := s.Quarantine(files[0]); err != nil {
		t.Fatal(err)
	}
	if files, _ := s.Files(); len(files) != 0 {
		t.Fatalf("隔离后不应再进入上传队列，仍有 %d 个", len(files))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".corrupt") {
		t.Fatalf("隔离件应保留在磁盘: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(dir, entries[0].Name())); err != nil {
		t.Fatal(err)
	}
}
