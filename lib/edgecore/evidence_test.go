package edgecore

import (
	"testing"
	"time"

	"yufeng/lib/kernel"
)

func TestEvidenceRingTTL(t *testing.T) {
	r := NewEvidenceRing()
	now := time.Now()
	r.Put("e1", []byte("raw-query=secret"), now)
	got, ok := r.Get("e1", now.Add(time.Second))
	if !ok || string(got) != "raw-query=secret" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	if _, ok := r.Get("e1", now.Add(kernel.EvidenceRingTTL+time.Second)); ok {
		t.Fatal("expired evidence must not be readable")
	}
}
