package eventbus

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestJetStreamRestartReplaysUnacked(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "js")
	bus, err := NewEmbeddedStore("127.0.0.1", -1, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.PublishDurable(SubjectEvents, "evt-restart-1", []byte(`{"event_id":"evt-restart-1"}`)); err != nil {
		t.Fatal(err)
	}
	msg, err := bus.FetchDurable(DurableEvents, SubjectEvents, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if string(msg.Data) != `{"event_id":"evt-restart-1"}` {
		t.Fatalf("payload=%s", msg.Data)
	}
	// 不 Ack，模拟进程被杀。
	bus.Close()

	bus2, err := NewEmbeddedStore("127.0.0.1", -1, store)
	if err != nil {
		t.Fatal(err)
	}
	defer bus2.Close()
	again, err := bus2.FetchDurable(DurableEvents, SubjectEvents, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Data) != `{"event_id":"evt-restart-1"}` {
		t.Fatalf("after restart payload=%s", again.Data)
	}
	if err := again.Ack(); err != nil {
		t.Fatal(err)
	}

	if err := bus2.PublishDurable(SubjectEvents, "evt-restart-1", []byte(`{"event_id":"evt-restart-1"}`)); err != nil {
		t.Fatal(err)
	}
	_, err = bus2.FetchDurable(DurableEvents, SubjectEvents, 400*time.Millisecond)
	if err == nil {
		t.Fatal("duplicate msg id must not deliver a second message")
	}
	if !errors.Is(err, nats.ErrTimeout) {
		t.Fatalf("want timeout after ack+dedupe, got %v", err)
	}
}

func TestJetStreamPublishRequiresMsgID(t *testing.T) {
	bus, err := NewEmbedded("127.0.0.1", -1)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	if err := bus.PublishDurable(SubjectEvents, "", []byte("{}")); err == nil {
		t.Fatal("empty msg id must fail")
	}
}
