package edgeclient

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"
	eventv1 "yufeng/proto/gen/eventv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

func TestApplyUploadAckMixed(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(testEvent("ok")); err != nil {
		t.Fatal(err)
	}
	files, err := s.Files()
	if err != nil || len(files) != 1 {
		t.Fatalf("files %v %v", files, err)
	}
	if err := ApplyUploadAck(s, files[0], []*eventv1.Event{testEvent("ok")}, &telemetryv1.UploadEventsResponse{Accepted: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if files, _ := s.Files(); len(files) != 0 {
		t.Fatal("accepted must delete local")
	}

	if err := s.Append(testEvent("bad")); err != nil {
		t.Fatal(err)
	}
	files, _ = s.Files()
	if err := ApplyUploadAck(s, files[0], []*eventv1.Event{testEvent("bad")}, &telemetryv1.UploadEventsResponse{
		Rejected: []*telemetryv1.RejectedEvent{{EventId: "bad", Code: "invalid_event"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if files, _ := s.Files(); len(files) != 0 {
		t.Fatal("permanent reject must leave upload queue")
	}
	entries, _ := os.ReadDir(dir)
	found := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".corrupt") {
			found = true
		}
	}
	if !found {
		t.Fatal("permanent illegal must quarantine")
	}

	if err := s.Append(testEvent("tmp")); err != nil {
		t.Fatal(err)
	}
	files, _ = s.Files()
	if err := ApplyUploadAck(s, files[0], []*eventv1.Event{testEvent("tmp")}, nil, errors.New("temporarily unavailable")); err == nil {
		t.Fatal("temp error should return")
	}
	if files, _ := s.Files(); len(files) != 1 {
		t.Fatal("temp error must keep file")
	}
}

func TestApplyUploadAckDeduped(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(&eventv1.Event{Id: "d", OccurredAt: timestamppb.Now(), AssetId: "a", Source: "t"}); err != nil {
		t.Fatal(err)
	}
	files, _ := s.Files()
	if err := ApplyUploadAck(s, files[0], nil, &telemetryv1.UploadEventsResponse{Deduped: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if files, _ := s.Files(); len(files) != 0 {
		t.Fatal("deduped must delete")
	}
	_ = filepath.Separator
}

func TestApplyUploadAckEventLevelMixed(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(testEvent("ok")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(testEvent("tmp")); err != nil {
		t.Fatal(err)
	}
	files, _ := s.Files()
	evs, err := s.ReadEvents(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUploadAck(s, files[0], evs, &telemetryv1.UploadEventsResponse{
		Accepted: 1,
		Rejected: []*telemetryv1.RejectedEvent{{EventId: "tmp", Code: "unavailable"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	left, err := s.ReadEvents(files[0])
	if err != nil || len(left) != 1 || left[0].Id != "tmp" {
		t.Fatalf("must keep only temp reject: %v %v", left, err)
	}
}

func TestApplyUploadAckSeparatesPermanentAndTemporaryRejects(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ok", "bad", "retry"} {
		if err := s.Append(testEvent(id)); err != nil {
			t.Fatal(err)
		}
	}
	files, err := s.Files()
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	events, err := s.ReadEvents(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUploadAck(s, files[0], events, &telemetryv1.UploadEventsResponse{
		Accepted: 1,
		Rejected: []*telemetryv1.RejectedEvent{
			{EventId: "bad", Code: "invalid_event"},
			{EventId: "retry", Code: "unavailable"},
		},
	}, nil); err != nil {
		t.Fatal(err)
	}
	left, err := s.ReadEvents(files[0])
	if err != nil || len(left) != 1 || left[0].Id != "retry" {
		t.Fatalf("retry queue=%v err=%v", left, err)
	}
	corrupt := strings.TrimSuffix(files[0], ".ndjson") + ".corrupt"
	quarantined, err := s.ReadEvents(corrupt)
	if err != nil || len(quarantined) != 1 || quarantined[0].Id != "bad" {
		t.Fatalf("quarantine=%v err=%v", quarantined, err)
	}
}
