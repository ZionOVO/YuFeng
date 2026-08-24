package edgeclient

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

func TestReviewSpoolRoundTripReplaceAndQuarantine(t *testing.T) {
	directory := t.TempDir()
	spool, err := NewReviewSpool(directory)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	first := &telemetryv1.TrafficWindow{
		WindowId: "window-1", UnitId: "edge-1", AssetId: "asset-1",
		WindowStart: timestamppb.New(start), WindowEnd: timestamppb.New(start.Add(5 * time.Minute)), RequestCount: 3,
	}
	second := &telemetryv1.TrafficWindow{
		WindowId: "window-2", UnitId: "edge-1", AssetId: "asset-1",
		WindowStart: timestamppb.New(start.Add(5 * time.Minute)), WindowEnd: timestamppb.New(start.Add(10 * time.Minute)), RequestCount: 5,
	}
	if err := spool.AppendWindows([]*telemetryv1.TrafficWindow{nil, first, second}); err != nil {
		t.Fatal(err)
	}
	if err := spool.AppendWindows([]*telemetryv1.TrafficWindow{first, second}); err != nil {
		t.Fatalf("repeat append must be idempotent: %v", err)
	}
	files, err := spool.WindowFiles()
	if err != nil || len(files) != 1 {
		t.Fatalf("window files=%v err=%v", files, err)
	}
	windows, err := spool.ReadWindows(files[0])
	if err != nil || len(windows) != 2 || !proto.Equal(windows[0], first) || !proto.Equal(windows[1], second) {
		t.Fatalf("round trip windows=%v err=%v", windows, err)
	}
	if err := spool.ReplaceWindows(files[0], []*telemetryv1.TrafficWindow{second}); err != nil {
		t.Fatal(err)
	}
	files, err = spool.WindowFiles()
	if err != nil || len(files) != 1 {
		t.Fatalf("replacement files=%v err=%v", files, err)
	}
	windows, err = spool.ReadWindows(files[0])
	if err != nil || len(windows) != 1 || !proto.Equal(windows[0], second) {
		t.Fatalf("replacement windows=%v err=%v", windows, err)
	}
	if err := spool.QuarantineWindows([]*telemetryv1.TrafficWindow{first}); err != nil {
		t.Fatal(err)
	}
	rejected, err := filepath.Glob(filepath.Join(directory, "windows-*.rejected"))
	if err != nil || len(rejected) != 1 {
		t.Fatalf("rejected files=%v err=%v", rejected, err)
	}
	if err := spool.ReplaceWindows(files[0], nil); err != nil {
		t.Fatal(err)
	}
	if files, err := spool.WindowFiles(); err != nil || len(files) != 0 {
		t.Fatalf("confirmed frames=%v err=%v", files, err)
	}
}

func TestReviewSpoolCandidateRoundTripAndPermanentRejection(t *testing.T) {
	directory := t.TempDir()
	spool, err := NewReviewSpool(directory)
	if err != nil {
		t.Fatal(err)
	}
	candidate := &telemetryv1.ReviewCandidate{CandidateId: "candidate-1", WindowId: "window-1", UnitId: "edge-1", AssetId: "asset-1", RiskScore: 80}
	if err := spool.AppendCandidates([]*telemetryv1.ReviewCandidate{candidate}); err != nil {
		t.Fatal(err)
	}
	files, err := spool.CandidateFiles()
	if err != nil || len(files) != 1 {
		t.Fatalf("candidate files=%v err=%v", files, err)
	}
	values, err := spool.ReadCandidates(files[0])
	if err != nil || len(values) != 1 || !proto.Equal(values[0], candidate) {
		t.Fatalf("candidate round trip=%v err=%v", values, err)
	}
	replacement := &telemetryv1.ReviewCandidate{CandidateId: "candidate-2", WindowId: "window-1", UnitId: "edge-1", AssetId: "asset-1", RiskScore: 90}
	if err := spool.ReplaceCandidates(files[0], []*telemetryv1.ReviewCandidate{replacement}); err != nil {
		t.Fatal(err)
	}
	files, err = spool.CandidateFiles()
	if err != nil || len(files) != 1 {
		t.Fatalf("replacement candidate files=%v err=%v", files, err)
	}
	values, err = spool.ReadCandidates(files[0])
	if err != nil || len(values) != 1 || !proto.Equal(values[0], replacement) {
		t.Fatalf("replacement candidate=%v err=%v", values, err)
	}
	if err := spool.QuarantineCandidates([]*telemetryv1.ReviewCandidate{candidate}); err != nil {
		t.Fatal(err)
	}
	if err := spool.Quarantine(files[0]); err != nil {
		t.Fatal(err)
	}
	if files, err := spool.CandidateFiles(); err != nil || len(files) != 0 {
		t.Fatalf("quarantined candidates=%v err=%v", files, err)
	}
	rejected, err := filepath.Glob(filepath.Join(directory, "candidates-*.rejected"))
	if err != nil || len(rejected) != 2 {
		t.Fatalf("candidate rejection files=%v err=%v", rejected, err)
	}
}

func TestReviewSpoolRejectsTrailingOrTamperedFrameData(t *testing.T) {
	for _, test := range []struct {
		name   string
		tamper func([]byte) []byte
	}{
		{name: "trailing object", tamper: func(raw []byte) []byte { return append(raw, []byte(`{}`)...) }},
		{name: "changed record", tamper: func(raw []byte) []byte { return bytes.Replace(raw, []byte(`"window-1"`), []byte(`"window-x"`), 1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			spool, err := NewReviewSpool(directory)
			if err != nil {
				t.Fatal(err)
			}
			if err := spool.AppendWindows([]*telemetryv1.TrafficWindow{{WindowId: "window-1"}}); err != nil {
				t.Fatal(err)
			}
			files, err := spool.WindowFiles()
			if err != nil || len(files) != 1 {
				t.Fatalf("files=%v err=%v", files, err)
			}
			raw, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(files[0], test.tamper(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := spool.ReadWindows(files[0]); err == nil {
				t.Fatal("corrupt frame must fail checksum validation")
			}
			if err := spool.QuarantineCorrupt(files[0]); err != nil {
				t.Fatal(err)
			}
			if files, err := spool.WindowFiles(); err != nil || len(files) != 0 {
				t.Fatalf("corrupt frames=%v err=%v", files, err)
			}
		})
	}
}

func TestReviewSpoolCleansOnlyExpiredQuarantine(t *testing.T) {
	directory := t.TempDir()
	oldPath := filepath.Join(directory, "windows-old.rejected")
	freshPath := filepath.Join(directory, "candidates-fresh.corrupt")
	for _, path := range []string{oldPath, freshPath} {
		if err := os.WriteFile(path, []byte("quarantine"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-reviewSpoolQuarantineTTL - time.Minute)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := NewReviewSpool(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expired quarantine remains: %v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("fresh quarantine removed: %v", err)
	}
}

func TestReviewSpoolReservesCapacityForTrafficWindows(t *testing.T) {
	directory := t.TempDir()
	spool, err := NewReviewSpool(directory)
	if err != nil {
		t.Fatal(err)
	}
	fullCandidates := filepath.Join(directory, "candidates-old.rejected")
	file, err := os.OpenFile(fullCandidates, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(reviewCandidateSpoolMaxBytes); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := spool.AppendCandidates([]*telemetryv1.ReviewCandidate{{CandidateId: "candidate-ignored-quarantine"}}); err != nil {
		t.Fatalf("quarantined files must not consume production capacity: %v", err)
	}
	fullCandidates = filepath.Join(directory, "candidates-old.frame")
	file, err = os.OpenFile(fullCandidates, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(reviewCandidateSpoolMaxBytes); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := spool.AppendCandidates([]*telemetryv1.ReviewCandidate{{CandidateId: "candidate-1"}}); err == nil {
		t.Fatal("candidate append must stop at its production capacity")
	}
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	if err := spool.AppendWindows([]*telemetryv1.TrafficWindow{{
		WindowId: "window-1", UnitId: "edge-1", AssetId: "asset-1",
		WindowStart: timestamppb.New(start), WindowEnd: timestamppb.New(start.Add(5 * time.Minute)), RequestCount: 1,
	}}); err != nil {
		t.Fatalf("traffic window must use reserved spool capacity: %v", err)
	}
}

func TestReviewSpoolReplacementDoesNotConsumeCapacityTwice(t *testing.T) {
	directory := t.TempDir()
	spool, err := NewReviewSpool(directory)
	if err != nil {
		t.Fatal(err)
	}
	first := &telemetryv1.ReviewCandidate{CandidateId: "candidate-1", WindowId: "window-1", UnitId: "edge-1", AssetId: "asset-1", RiskScore: 80}
	if err := spool.AppendCandidates([]*telemetryv1.ReviewCandidate{first}); err != nil {
		t.Fatal(err)
	}
	files, err := spool.CandidateFiles()
	if err != nil || len(files) != 1 {
		t.Fatalf("candidate files=%v err=%v", files, err)
	}
	oldPath := files[0]
	oldInfo, err := os.Stat(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	fillerPath := filepath.Join(directory, "candidates-capacity.frame")
	filler, err := os.OpenFile(fillerPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := filler.Truncate(reviewCandidateSpoolMaxBytes - oldInfo.Size()); err != nil {
		_ = filler.Close()
		t.Fatal(err)
	}
	if err := filler.Close(); err != nil {
		t.Fatal(err)
	}

	replacement := &telemetryv1.ReviewCandidate{CandidateId: "candidate-2", WindowId: "window-1", UnitId: "edge-1", AssetId: "asset-1", RiskScore: 90}
	if err := spool.ReplaceCandidates(oldPath, []*telemetryv1.ReviewCandidate{replacement}); err != nil {
		t.Fatalf("replacement within final capacity must succeed: %v", err)
	}
	if err := spool.ReplaceCandidates(oldPath, []*telemetryv1.ReviewCandidate{replacement}); err != nil {
		t.Fatalf("replacement retry after old-frame removal must stay idempotent: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old frame remains after replacement: %v", err)
	}
	files, err = spool.CandidateFiles()
	if err != nil || len(files) != 2 {
		t.Fatalf("replacement files=%v err=%v", files, err)
	}
	for _, path := range files {
		if path == fillerPath {
			continue
		}
		values, err := spool.ReadCandidates(path)
		if err != nil || len(values) != 1 || !proto.Equal(values[0], replacement) {
			t.Fatalf("replacement candidate=%v err=%v", values, err)
		}
	}
}
