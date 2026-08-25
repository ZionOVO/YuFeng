package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRefreshFileEmptyDir(t *testing.T) {
	if refreshFile("") != "" || refreshFile("   ") != "" {
		t.Fatal("empty state dir must not persist")
	}
	stateDir := filepath.Join(string(filepath.Separator), "var", "lib", "yufeng", "jarvis")
	got := refreshFile(stateDir)
	if want := filepath.Join(stateDir, "refresh"); got != want {
		t.Fatalf("path=%s", got)
	}
}

func TestSaveLoadRefreshRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := refreshFile(dir)
	if err := saveRefresh(path, "jarvis-1", "refresh-one"); err != nil {
		t.Fatal(err)
	}
	if got := loadRefresh(path, "jarvis-1"); got != "refresh-one" {
		t.Fatalf("got %q", got)
	}
	if got := loadRefresh(path, "other"); got != "" {
		t.Fatalf("other agent must not load, got %q", got)
	}
	if err := saveRefresh(path, "jarvis-1", "refresh-two"); err != nil {
		t.Fatal(err)
	}
	if got := loadRefresh(path, "jarvis-1"); got != "refresh-two" {
		t.Fatalf("rotated got %q", got)
	}
}

func TestLoadRefreshRejectsCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := refreshFile(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadRefresh(path, "jarvis-1"); got != "" {
		t.Fatalf("corrupt file must be ignored, got %q", got)
	}
	if got := loadRefresh(filepath.Join(dir, "missing"), "jarvis-1"); got != "" {
		t.Fatalf("missing file must be empty, got %q", got)
	}
}
