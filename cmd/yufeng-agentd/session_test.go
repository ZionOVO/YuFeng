package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWorkerRefreshRoundTripAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agentd")
	path := workerRefreshFile(dir)
	if err := saveWorkerRefresh(path, "agentd-one", "refresh-one"); err != nil {
		t.Fatal(err)
	}
	refresh, err := loadWorkerRefresh(path, "agentd-one")
	if err != nil || refresh != "refresh-one" {
		t.Fatalf("refresh=%q err=%v", refresh, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%#o", info.Mode().Perm())
	}
	if _, err := loadWorkerRefresh(path, "agentd-two"); err == nil {
		t.Fatal("different worker must not load persisted refresh")
	}
}

func TestWorkerRefreshRejectsMissingStateDirectoryAndCorruption(t *testing.T) {
	if _, err := loadWorkerRefresh("", "agentd-one"); err == nil {
		t.Fatal("empty state path must fail closed")
	}
	dir := t.TempDir()
	path := workerRefreshFile(dir)
	refresh, err := loadWorkerRefresh(path, "agentd-one")
	if err != nil || refresh != "" {
		t.Fatalf("missing refresh=%q err=%v", refresh, err)
	}
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadWorkerRefresh(path, "agentd-one"); err == nil {
		t.Fatal("corrupt refresh state must fail closed")
	}
}
