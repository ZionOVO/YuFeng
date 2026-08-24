package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishPayloadRequiresExplicitFile(t *testing.T) {
	if _, err := publishPayload(""); err == nil {
		t.Fatal("publish payload unexpectedly used an embedded rule")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	want := []byte(`[{"id":"operator-supplied"}]`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := publishPayload(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("payload = %q, want %q", got, want)
	}
	if _, err := publishPayload(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing payload error = %v", err)
	}
}
