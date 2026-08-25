package edgecore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

func TestEvidenceVaultEncryptsAndExpires(t *testing.T) {
	dir := t.TempDir()
	vault, err := NewEvidenceVault(dir, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Hour)
	secret := []byte(`{"query":"token=do-not-persist-plain"}`)
	handle, digest, expires, err := vault.Put(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "evidence-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	onDisk, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), "do-not-persist-plain") {
		t.Fatal("vault persisted plaintext")
	}
	got, gotDigest, ok, err := vault.Get(handle, now.Add(time.Minute))
	if err != nil || !ok || !bytes.Equal(got, secret) || gotDigest != digest {
		t.Fatalf("got=%q digest=%q ok=%v err=%v", got, gotDigest, ok, err)
	}
	if _, _, ok, err := vault.Get(handle, expires.Add(time.Second)); err != nil || ok {
		t.Fatalf("expired ok=%v err=%v", ok, err)
	}
}

func TestEvidenceVaultRejectsWrongKeyLength(t *testing.T) {
	if _, err := NewEvidenceVault(t.TempDir(), []byte("short")); err == nil {
		t.Fatal("want key length error")
	}
}

func TestEvidenceVaultSurvivesRestartAndRejectsWrongKey(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{9}, 32)
	now := time.Now().UTC().Truncate(time.Hour)
	first, err := NewEvidenceVault(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	handle, digest, _, err := first.Put([]byte("restart-sensitive-evidence"), now)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewEvidenceVault(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, gotDigest, ok, err := restarted.Get(handle, now.Add(time.Minute))
	if err != nil || !ok || string(raw) != "restart-sensitive-evidence" || gotDigest != digest {
		t.Fatalf("restart raw=%q digest=%q ok=%v err=%v", raw, gotDigest, ok, err)
	}
	wrongKey, err := NewEvidenceVault(dir, bytes.Repeat([]byte{10}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := wrongKey.Get(handle, now.Add(time.Minute)); err == nil {
		t.Fatal("wrong evidence key must fail closed")
	}
}

func TestEvidenceVaultCapacityFailureKeepsExistingEvidence(t *testing.T) {
	dir := t.TempDir()
	vault, err := NewEvidenceVault(dir, bytes.Repeat([]byte{11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultTrafficReviewPolicy()
	policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL
	policy.MaxEvidenceBytes = 256
	policy.VaultMaxBytes = 1024
	if err := vault.Configure(policy); err != nil {
		t.Fatalf("configure small vault: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Hour)
	handle, _, _, err := vault.Put(bytes.Repeat([]byte("a"), 128), now)
	if err != nil {
		t.Fatalf("write first evidence: %v", err)
	}
	exhausted := false
	for index := 0; index < 10; index++ {
		if _, _, _, err := vault.Put(bytes.Repeat([]byte{byte('b' + index)}, 256), now); err != nil {
			exhausted = true
			break
		}
	}
	if !exhausted {
		t.Fatal("bounded vault did not report capacity exhaustion")
	}
	raw, _, ok, err := vault.Get(handle, now.Add(time.Minute))
	if err != nil || !ok || !bytes.Equal(raw, bytes.Repeat([]byte("a"), 128)) {
		t.Fatalf("existing evidence was lost raw=%q ok=%v err=%v", raw, ok, err)
	}
}

func TestEvidenceVaultReservesCapacityForHighRiskEvidence(t *testing.T) {
	vault, err := NewEvidenceVault(t.TempDir(), bytes.Repeat([]byte{12}, 32))
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultTrafficReviewPolicy()
	policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL
	policy.MaxEvidenceBytes = 256
	policy.VaultMaxBytes = 2048
	if err := vault.Configure(policy); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Hour)
	reserved := false
	for index := 0; index < 20; index++ {
		_, _, _, err := vault.PutRisk(bytes.Repeat([]byte{byte('a' + index%10)}, 128), 0, now)
		if err == ErrEvidenceVaultLowRiskCapacity {
			reserved = true
			break
		}
		if err != nil {
			t.Fatalf("low risk write failed before reserve: %v", err)
		}
	}
	if !reserved {
		t.Fatal("low risk evidence consumed the high risk reserve")
	}
	if _, _, _, err := vault.PutRisk(bytes.Repeat([]byte("z"), 128), 90, now); err != nil {
		t.Fatalf("high risk evidence could not use reserved capacity: %v", err)
	}
}

func TestEvidenceVaultRebuildsHandleIndexAndReclaimsExpiredSegment(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{13}, 32)
	vault, err := NewEvidenceVault(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	vault.maxBytes = 4096
	vault.maxEntry = 128
	vault.ttl = 10 * time.Minute
	now := time.Now().UTC()
	handles := make([]string, 0, 8)
	for index := 0; index < 8; index++ {
		handle, _, _, err := vault.Put(bytes.Repeat([]byte{byte('a' + index)}, 96), now.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatalf("put %d: %v", index, err)
		}
		handles = append(handles, handle)
	}
	restarted, err := NewEvidenceVault(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.index) != len(handles) {
		t.Fatalf("rebuilt index entries=%d want %d", len(restarted.index), len(handles))
	}
	for index, handle := range handles {
		raw, _, ok, err := restarted.Get(handle, now.Add(time.Minute))
		if err != nil || !ok || len(raw) != 96 {
			t.Fatalf("indexed get %d ok=%v bytes=%d err=%v", index, ok, len(raw), err)
		}
	}
	restarted.maxBytes = 4096
	restarted.maxEntry = 128
	restarted.ttl = 10 * time.Minute
	if _, _, _, err := restarted.PutRisk(bytes.Repeat([]byte("z"), 96), 100, now.Add(time.Hour)); err != nil {
		t.Fatalf("expired segment capacity was not reclaimed: %v", err)
	}
	if _, _, ok, err := restarted.Get(handles[0], now.Add(time.Hour)); err != nil || ok {
		t.Fatalf("expired indexed handle ok=%v err=%v", ok, err)
	}
}

func TestEvidenceVaultGCWaitsUntilEverySegmentEntryExpires(t *testing.T) {
	directory := t.TempDir()
	vault, err := NewEvidenceVault(directory, bytes.Repeat([]byte{20}, 32))
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultTrafficReviewPolicy()
	policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL
	policy.EvidenceTtlSeconds = int64(time.Hour / time.Second)
	if err := vault.Configure(policy); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	first, _, _, err := vault.Put([]byte("first"), start)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := vault.Put([]byte("second"), start.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.GC(start.Add(61 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := vault.Get(first, start.Add(61*time.Minute)); err != nil || ok {
		t.Fatalf("expired first entry ok=%v err=%v", ok, err)
	}
	if raw, _, ok, err := vault.Get(second, start.Add(61*time.Minute)); err != nil || !ok || string(raw) != "second" {
		t.Fatalf("live second entry raw=%q ok=%v err=%v", raw, ok, err)
	}
	files, err := filepath.Glob(filepath.Join(directory, "evidence-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("segment removed while one entry remained: files=%v err=%v", files, err)
	}
	if err := vault.GC(start.Add(91 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	files, err = filepath.Glob(filepath.Join(directory, "evidence-*.jsonl"))
	if err != nil || len(files) != 0 {
		t.Fatalf("fully expired segment remains: files=%v err=%v", files, err)
	}
}

func TestEvidenceVaultRecoversOnlyUncommittedSegmentTail(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{14}, 32)
	now := time.Now().UTC().Truncate(time.Hour)
	vault, err := NewEvidenceVault(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	handle, _, _, err := vault.Put([]byte("committed"), now)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "evidence-*.jsonl"))
	committed, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"version":2,"handle":"partial"`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewEvidenceVault(dir, key)
	if err != nil {
		t.Fatalf("partial tail must be recovered: %v", err)
	}
	info, err := os.Stat(files[0])
	if err != nil || info.Size() != committed.Size() {
		t.Fatalf("recovered size=%d want=%d err=%v", info.Size(), committed.Size(), err)
	}
	if raw, _, ok, err := restarted.Get(handle, now.Add(time.Minute)); err != nil || !ok || string(raw) != "committed" {
		t.Fatalf("committed record raw=%q ok=%v err=%v", raw, ok, err)
	}
}

func TestEvidenceVaultTreatsCompleteJSONWithoutNewlineAsUncommitted(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{15}, 32)
	now := time.Now().UTC().Truncate(time.Hour)
	vault, err := NewEvidenceVault(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := vault.Put([]byte("committed"), now); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "evidence-*.jsonl"))
	committed, _ := os.Stat(files[0])
	file, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	complete := vaultRecord{Version: 2, Handle: "evh-uncommitted", Digest: "sha256:00", Nonce: "AA", Cipher: "AA", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	raw, _ := json.Marshal(complete)
	if _, err := file.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEvidenceVault(dir, key); err != nil {
		t.Fatalf("complete tail without commit newline must be recovered: %v", err)
	}
	info, _ := os.Stat(files[0])
	if info.Size() != committed.Size() {
		t.Fatalf("recovered size=%d want=%d", info.Size(), committed.Size())
	}
}

func TestEvidenceVaultIndexesCommittedWriteMissingFromMemory(t *testing.T) {
	key := bytes.Repeat([]byte{16}, 32)
	now := time.Now().UTC().Truncate(time.Hour)
	targetDir := t.TempDir()
	target, err := NewEvidenceVault(targetDir, key)
	if err != nil {
		t.Fatal(err)
	}
	firstHandle, _, _, err := target.Put([]byte("first"), now)
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := t.TempDir()
	source, err := NewEvidenceVault(sourceDir, key)
	if err != nil {
		t.Fatal(err)
	}
	secondHandle, _, _, err := source.Put([]byte("written-before-crash"), now)
	if err != nil {
		t.Fatal(err)
	}
	targetFiles, _ := filepath.Glob(filepath.Join(targetDir, "evidence-*.jsonl"))
	sourceFiles, _ := filepath.Glob(filepath.Join(sourceDir, "evidence-*.jsonl"))
	line, err := os.ReadFile(sourceFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(targetFiles[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(line); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewEvidenceVault(targetDir, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ handle, want string }{{firstHandle, "first"}, {secondHandle, "written-before-crash"}} {
		raw, _, ok, err := restarted.Get(item.handle, now.Add(time.Minute))
		if err != nil || !ok || string(raw) != item.want {
			t.Fatalf("handle=%s raw=%q ok=%v err=%v", item.handle, raw, ok, err)
		}
	}
}

func TestEvidenceVaultAuthenticatesExpiryAndRiskMetadata(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{17}, 32)
	now := time.Now().UTC().Truncate(time.Hour)
	vault, err := NewEvidenceVault(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	handle, _, _, err := vault.PutRisk([]byte("authenticated-metadata"), 91, now)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "evidence-*.jsonl"))
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var record vaultRecord
	if err := json.Unmarshal(bytes.TrimSpace(raw), &record); err != nil {
		t.Fatal(err)
	}
	record.ExpiresAt = record.ExpiresAt.Add(time.Hour)
	record.RiskScore = 1
	tampered, _ := json.Marshal(record)
	tampered = append(tampered, '\n')
	if err := os.WriteFile(files[0], tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewEvidenceVault(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := restarted.Get(handle, now.Add(time.Minute)); err == nil {
		t.Fatal("tampered authenticated metadata must fail closed")
	}
}

func TestEvidenceVaultQuarantinesOnlyCorruptSegment(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{18}, 32)
	now := time.Now().UTC().Truncate(time.Hour)
	vault, err := NewEvidenceVault(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := vault.Put([]byte("first-segment"), now); err != nil {
		t.Fatal(err)
	}
	secondHandle, _, _, err := vault.Put([]byte("second-segment"), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "evidence-*.jsonl"))
	if err != nil || len(files) != 2 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	if err := os.WriteFile(files[0], []byte("{invalid-json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewEvidenceVault(dir, key)
	if err != nil {
		t.Fatalf("unrelated corrupt segment must be isolated: %v", err)
	}
	raw, _, ok, err := restarted.Get(secondHandle, now.Add(time.Hour+time.Minute))
	if err != nil || !ok || string(raw) != "second-segment" {
		t.Fatalf("preserved segment raw=%q ok=%v err=%v", raw, ok, err)
	}
	quarantined, err := filepath.Glob(filepath.Join(dir, "evidence-*.corrupt"))
	if err != nil || len(quarantined) != 1 {
		t.Fatalf("quarantined=%v err=%v", quarantined, err)
	}
}

func TestEvidenceVaultRestrictsBroadDirectoryPermissions(t *testing.T) {
	if !enforcesPOSIXVaultPermissions {
		t.Skip("Windows uses access control lists instead of POSIX permission bits")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEvidenceVault(dir, bytes.Repeat([]byte{19}, 32)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("permissions=%o", info.Mode().Perm())
	}
}
