package scripts

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseMetadataRequiresTheFiveExactFields(t *testing.T) {
	directory := t.TempDir()
	metadata := filepath.Join(directory, "metadata.txt")
	valid := strings.Join([]string{
		"v0.2.0 人工 Edge 生命周期与模型旁路",
		"",
		"release-version=v0.2.0",
		"evidence-commit=0123456789012345678901234567890123456789",
		"evidence-tree=abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		"evidence-sha256=" + strings.Repeat("1", 64),
		"evidence-result=passed",
	}, "\n") + "\n"
	if err := os.WriteFile(metadata, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", "release-metadata.py", "verify", "--file", metadata,
		"--expected-version", "v0.2.0", "--expected-commit", "0123456789012345678901234567890123456789",
		"--expected-tree", "abcdefabcdefabcdefabcdefabcdefabcdefabcd", "--expected-sha256", strings.Repeat("1", 64))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("verify metadata: %v: %s", err, output)
	}
	if err := os.WriteFile(metadata, []byte(valid+"evidence-result=passed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("python3", "release-metadata.py", "verify", "--file", metadata).CombinedOutput(); err == nil {
		t.Fatalf("duplicate metadata passed: %s", output)
	}
}

func TestReleaseEvidenceScannerRejectsSecretsWithoutPrintingThem(t *testing.T) {
	directory := t.TempDir()
	secret := "model-key-value-that-must-stay-private"
	if err := os.WriteFile(filepath.Join(directory, "evidence.log"), []byte("Authorization: Bearer "+secret), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", "release-evidence.py", "scan", "--root", directory)
	command.Env = append(os.Environ(), "YUFENG_MODEL_API_KEY="+secret)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("secret scan unexpectedly passed: %s", output)
	}
	if strings.Contains(string(output), secret) {
		t.Fatal("secret scanner printed the secret value")
	}
}

func TestReleasePreflightAndPromotionRunStaticAndLiveExactlyOnce(t *testing.T) {
	fixture := newReleasePreflightFixture(t)
	verifyPreflight := exec.Command("python3", "release-evidence.py", "verify-preflight",
		"--manifest", fixture.preflightManifest, "--archive", fixture.preflightArchive,
		"--checksum", fixture.preflightChecksum, "--expected-version", "v0.2.0",
		"--expected-base-commit", fixture.baseCommit, "--expected-source-commit", fixture.sourceCommit,
		"--expected-tree", fixture.tree, "--expected-environment-fingerprint", fixture.environmentFingerprint,
		"--now", "2099-01-02T00:00:00Z")
	if output, err := verifyPreflight.CombinedOutput(); err != nil {
		t.Fatalf("verify release preflight: %v: %s", err, output)
	}

	finalArchive := filepath.Join(fixture.directory, "yufeng-v0.2.0-live-evidence.tar.gz")
	finalChecksum := finalArchive + ".sha256"
	finalManifest := filepath.Join(fixture.directory, "yufeng-v0.2.0-live-evidence.json")
	liveLog := []byte("traffic review live ok\n\"throughput_budget_met\": true\n\"committed_row_loss\": 0\n\"source_database_preserved\": true\ndelivery live evidence passed\n")
	liveDigest := sha256.Sum256(liveLog)
	finalReport := map[string]any{
		"schema":          "yufeng.release-evidence-report/v2",
		"release-version": "v0.2.0",
		"evidence-commit": fixture.developCommit,
		"evidence-tree":   fixture.tree,
		"evidence-result": "passed",
		"ci-url":          "https://github.com/ZionOVO/YuFeng/actions/runs/2",
		"merge-parents":   []string{fixture.baseCommit, fixture.sourceCommit},
		"git": map[string]any{
			"branch": "develop", "commit": fixture.developCommit, "tree": fixture.tree, "worktree": "clean",
		},
		"continuous-integration": map[string]any{
			"url": "https://github.com/ZionOVO/YuFeng/actions/runs/2", "status": "completed",
			"conclusion": "success", "head-sha": fixture.developCommit, "head-branch": "develop",
			"event": "push", "workflow-path": ".github/workflows/ci.yml",
		},
		"source-backup": map[string]any{
			"included-in-archive": false, "sha256": strings.Repeat("2", 64), "bytes": 1,
		},
		"static-preflight": map[string]any{
			"manifest": "provenance/preflight-manifest.json", "manifest-sha256": fixture.preflightManifestSHA,
			"report": "provenance/preflight-report.json", "report-sha256": fixture.reportSHA,
			"archive-sha256": fixture.archiveSHA, "environment-fingerprint": fixture.environmentFingerprint,
		},
		"promotion-environment-summary": "environment/promotion-summary.json",
		"live-results": map[string]any{
			"performance": "results/performance.json", "backup-restore": "results/backup-restore.json",
			"traffic-review": "results/traffic-review.json",
		},
		"secret-scan": map[string]any{"result": "passed", "record": "environment/final-secret-scan.txt"},
		"commands": []map[string]any{{
			"name": "live-evidence", "command": "./scripts/delivery-evidence.sh live", "result": "passed", "exit-code": 0,
			"log": "logs/live-evidence.log", "log-sha256": hex.EncodeToString(liveDigest[:]),
		}},
	}
	finalReportRaw, err := json.Marshal(finalReport)
	if err != nil {
		t.Fatal(err)
	}
	finalReportDigest := sha256.Sum256(finalReportRaw)
	writeEvidenceArchive(t, finalArchive, map[string][]byte{
		"yufeng-evidence/report.json":                        finalReportRaw,
		"yufeng-evidence/provenance/preflight-report.json":   fixture.reportRaw,
		"yufeng-evidence/provenance/preflight-manifest.json": fixture.manifestRaw,
		"yufeng-evidence/logs/release-static.log":            fixture.staticLog,
		"yufeng-evidence/logs/hot-path-benchmarks.log":       fixture.benchmarkLog,
		"yufeng-evidence/logs/live-evidence.log":             liveLog,
		"yufeng-evidence/environment/summary.json":           fixture.environmentRaw,
		"yufeng-evidence/environment/promotion-summary.json": fixture.environmentRaw,
		"yufeng-evidence/environment/secret-scan.txt":        []byte("secret scan passed\n"),
		"yufeng-evidence/environment/final-secret-scan.txt":  []byte("secret scan passed\n"),
		"yufeng-evidence/results/performance.json":           fixture.performanceRaw,
		"yufeng-evidence/results/backup-restore.json":        fixture.backupRestoreRaw,
		"yufeng-evidence/results/traffic-review.json":        fixture.trafficReviewRaw,
	})
	finalArchiveRaw, err := os.ReadFile(finalArchive)
	if err != nil {
		t.Fatal(err)
	}
	finalArchiveDigest := sha256.Sum256(finalArchiveRaw)
	finalArchiveSHA := hex.EncodeToString(finalArchiveDigest[:])
	if err := os.WriteFile(finalChecksum, []byte(finalArchiveSHA+"  "+filepath.Base(finalArchive)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema":          "yufeng.release-evidence/v2",
		"release-version": "v0.2.0",
		"evidence-commit": fixture.developCommit,
		"evidence-tree":   fixture.tree,
		"evidence-sha256": finalArchiveSHA,
		"evidence-result": "passed",
		"archive-asset":   filepath.Base(finalArchive),
		"checksum-asset":  filepath.Base(finalChecksum),
		"report-path":     "yufeng-evidence/report.json",
		"report-sha256":   hex.EncodeToString(finalReportDigest[:]),
		"ci-url":          "https://github.com/ZionOVO/YuFeng/actions/runs/2",
		"generated-at":    "2099-01-02T00:00:00Z",
		"merge-parents":   []string{fixture.baseCommit, fixture.sourceCommit},
		"preflight": map[string]any{
			"base-commit":             fixture.baseCommit,
			"source-commit":           fixture.sourceCommit,
			"tree":                    fixture.tree,
			"archive-sha256":          fixture.archiveSHA,
			"manifest-sha256":         fixture.preflightManifestSHA,
			"report-sha256":           fixture.reportSHA,
			"environment-fingerprint": fixture.environmentFingerprint,
			"generated-at":            "2099-01-01T00:00:00Z",
			"expires-at":              "2099-01-04T00:00:00Z",
		},
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalManifest, manifestRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	verifyFinal := exec.Command("python3", "release-evidence.py", "verify",
		"--manifest", finalManifest, "--archive", finalArchive, "--checksum", finalChecksum,
		"--expected-version", "v0.2.0", "--expected-commit", fixture.developCommit,
		"--expected-tree", fixture.tree, "--expected-sha256", finalArchiveSHA,
		"--expected-base-commit", fixture.baseCommit, "--expected-source-commit", fixture.sourceCommit)
	if output, err := verifyFinal.CombinedOutput(); err != nil {
		t.Fatalf("verify promoted release evidence: %v: %s", err, output)
	}
	preflightRaw, err := os.ReadFile(fixture.preflightArchive)
	if err != nil {
		t.Fatal(err)
	}
	if string(finalArchiveRaw) == string(preflightRaw) {
		t.Fatal("final evidence did not add the one live evidence run")
	}
}

func TestReleasePreflightRejectsEnvironmentMismatchAndExpiry(t *testing.T) {
	fixture := newReleasePreflightFixture(t)
	for name, extra := range map[string][]string{
		"environment mismatch": {"--expected-environment-fingerprint", strings.Repeat("f", 64), "--now", "2099-01-02T00:00:00Z"},
		"expired":              {"--expected-environment-fingerprint", fixture.environmentFingerprint, "--now", "2099-01-05T00:00:00Z"},
	} {
		t.Run(name, func(t *testing.T) {
			arguments := []string{"release-evidence.py", "verify-preflight",
				"--manifest", fixture.preflightManifest, "--archive", fixture.preflightArchive,
				"--checksum", fixture.preflightChecksum, "--expected-version", "v0.2.0",
				"--expected-base-commit", fixture.baseCommit, "--expected-source-commit", fixture.sourceCommit,
				"--expected-tree", fixture.tree}
			arguments = append(arguments, extra...)
			if output, err := exec.Command("python3", arguments...).CombinedOutput(); err == nil {
				t.Fatalf("invalid preflight passed: %s", output)
			}
		})
	}
}

type releasePreflightFixture struct {
	directory              string
	preflightArchive       string
	preflightChecksum      string
	preflightManifest      string
	preflightManifestSHA   string
	baseCommit             string
	sourceCommit           string
	developCommit          string
	tree                   string
	environmentFingerprint string
	archiveSHA             string
	reportSHA              string
	reportRaw              []byte
	manifestRaw            []byte
	environmentRaw         []byte
	staticLog              []byte
	benchmarkLog           []byte
	performanceRaw         []byte
	backupRestoreRaw       []byte
	trafficReviewRaw       []byte
}

func newReleasePreflightFixture(t *testing.T) releasePreflightFixture {
	t.Helper()
	directory := t.TempDir()
	archivePath := filepath.Join(directory, "yufeng-v0.2.0-preflight-evidence.tar.gz")
	checksumPath := archivePath + ".sha256"
	manifestPath := filepath.Join(directory, "yufeng-v0.2.0-preflight-evidence.json")
	baseCommit := "0123456789012345678901234567890123456789"
	sourceCommit := "1123456789012345678901234567890123456789"
	developCommit := "2123456789012345678901234567890123456789"
	tree := "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	environmentBody := map[string]any{
		"schema":           "yufeng.release-environment/v1",
		"hardware":         map[string]any{"chip": "Apple M4 Pro"},
		"operating-system": map[string]any{"name": "macOS"},
		"deployment-identities": map[string]any{
			"edge-unit": "local-1", "edge-asset": "local-1", "modelside-id": "local-1-modelside",
		},
		"toolchain":     map[string]any{"go": "go1.27.0"},
		"docker":        map[string]any{"compose": "v2"},
		"compose":       map[string]any{"hashes": []string{"compose"}},
		"model-weights": []map[string]any{{"path": "PVM.h5", "sha256": strings.Repeat("3", 64), "bytes": 1}},
	}
	environmentCanonical, err := json.Marshal(environmentBody)
	if err != nil {
		t.Fatal(err)
	}
	environmentDigest := sha256.Sum256(environmentCanonical)
	environmentFingerprint := hex.EncodeToString(environmentDigest[:])
	environmentBody["fingerprint-sha256"] = environmentFingerprint
	environmentRaw, err := json.Marshal(environmentBody)
	if err != nil {
		t.Fatal(err)
	}
	deliveryLog := []byte("delivery static evidence passed\n")
	benchmarkLog := []byte(strings.Join([]string{
		"BenchmarkReleaseSetImmutableExecutionPlan",
		"BenchmarkEvidenceRingConstantTimePrototype",
		"BenchmarkReleaseProxySelectiveObservation",
		"BenchmarkReleaseProxyParallelHotPath",
		"BenchmarkCorazaReleaseProxyParallel",
		"BenchmarkCorazaSharedCanonicalRequestPrototype",
		"BenchmarkCorazaParallelCapacity",
		"BenchmarkCorazaRegexPrefilter",
		"BenchmarkCorazaBodyProcessorCost",
	}, "\n") + "\n")
	deliveryDigest := sha256.Sum256(deliveryLog)
	benchmarkDigest := sha256.Sum256(benchmarkLog)
	performanceRaw := []byte(`{"schema_version":"model-bypass-capacity/v1","workload":{"target_requests_per_second":2000,"scenarios":["bypass_disabled","modelside_idle","modelside_saturated","brain_disconnected","brain_disk_slow"]},"budgets":{"edge_throughput_rps":2000,"model_bypass_p99_micros":1000},"model_bypass":{"scenarios":[{"name":"bypass_disabled","throughput_requests_per_second":2100,"p99_increase_micros":0},{"name":"modelside_idle","throughput_requests_per_second":2100,"p99_increase_micros":50},{"name":"modelside_saturated","throughput_requests_per_second":2100,"p99_increase_micros":40,"ingress_dropped":100},{"name":"brain_disconnected","throughput_requests_per_second":2100,"p99_increase_micros":60,"result_depth":32,"result_upload_retries":100},{"name":"brain_disk_slow","throughput_requests_per_second":2100,"p99_increase_micros":70,"result_depth":32,"result_upload_retries":100}]},"throughput_budget_met":true,"p99_budget_met":true}`)
	backupRestoreRaw := []byte(`{"backup_restore_deadline_met":true,"committed_row_loss":0,"source_database_preserved":true}`)
	trafficReviewRaw := []byte(`{"result":"passed","worker_id":"agentd-central","assigned_run_id":"run-1","finding_disposition":"TRAFFIC_FINDING_DISPOSITION_SUSPECTED_MISS","verified_release_state":"RELEASE_STATE_SHADOW","cleanup":{"release":"RELEASE_STATE_RETIRED","policy":"TRAFFIC_REVIEW_MODE_OFF","profile":"AGENT_PROFILE_STATE_DISABLED","case":"INVESTIGATION_CASE_STATE_RESOLVED"}}`)
	report := map[string]any{
		"schema":                  "yufeng.release-preflight-report/v1",
		"release-version":         "v0.2.0",
		"base-commit":             baseCommit,
		"source-commit":           sourceCommit,
		"preflight-tree":          tree,
		"preflight-result":        "passed",
		"environment-fingerprint": environmentFingerprint,
		"git": map[string]any{
			"branch": "release/v0.2.0", "base-commit": baseCommit, "source-commit": sourceCommit,
			"tree": tree, "worktree": "clean",
		},
		"environment-summary": "environment/summary.json",
		"secret-scan": map[string]any{
			"result": "passed", "record": "environment/secret-scan.txt",
		},
		"commands": []map[string]any{
			{"name": "release-static", "command": "./scripts/delivery-evidence.sh static", "result": "passed", "exit-code": 0,
				"log": "logs/release-static.log", "log-sha256": hex.EncodeToString(deliveryDigest[:])},
			{"name": "hot-path-benchmarks", "command": "go test ./lib/edgecore -benchmem -benchtime=250ms -count=5", "result": "passed", "exit-code": 0,
				"log": "logs/hot-path-benchmarks.log", "log-sha256": hex.EncodeToString(benchmarkDigest[:])},
		},
	}
	reportRaw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(archiveFile)
	tarWriter := tar.NewWriter(gzipWriter)
	files := map[string][]byte{
		"yufeng-evidence/report.json":                  reportRaw,
		"yufeng-evidence/logs/release-static.log":      deliveryLog,
		"yufeng-evidence/logs/hot-path-benchmarks.log": benchmarkLog,
		"yufeng-evidence/environment/summary.json":     environmentRaw,
		"yufeng-evidence/environment/secret-scan.txt":  []byte("secret scan passed\n"),
	}
	for name, content := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}
	archiveRaw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archiveDigest := sha256.Sum256(archiveRaw)
	reportDigest := sha256.Sum256(reportRaw)
	archiveSHA := hex.EncodeToString(archiveDigest[:])
	manifest := map[string]any{
		"schema":                  "yufeng.release-preflight/v1",
		"release-version":         "v0.2.0",
		"base-commit":             baseCommit,
		"source-commit":           sourceCommit,
		"preflight-tree":          tree,
		"preflight-sha256":        archiveSHA,
		"preflight-result":        "passed",
		"archive-asset":           filepath.Base(archivePath),
		"checksum-asset":          filepath.Base(checksumPath),
		"report-path":             "yufeng-evidence/report.json",
		"report-sha256":           hex.EncodeToString(reportDigest[:]),
		"environment-fingerprint": environmentFingerprint,
		"generated-at":            "2099-01-01T00:00:00Z",
		"expires-at":              "2099-01-04T00:00:00Z",
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checksumPath, []byte(archiveSHA+"  "+filepath.Base(archivePath)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestRaw)
	return releasePreflightFixture{
		directory: directory, preflightArchive: archivePath, preflightChecksum: checksumPath,
		preflightManifest: manifestPath, preflightManifestSHA: hex.EncodeToString(manifestDigest[:]),
		baseCommit: baseCommit, sourceCommit: sourceCommit, developCommit: developCommit, tree: tree,
		environmentFingerprint: environmentFingerprint, archiveSHA: archiveSHA,
		reportSHA: hex.EncodeToString(reportDigest[:]), reportRaw: reportRaw, manifestRaw: manifestRaw,
		environmentRaw: environmentRaw, staticLog: deliveryLog, benchmarkLog: benchmarkLog,
		performanceRaw: performanceRaw, backupRestoreRaw: backupRestoreRaw, trafficReviewRaw: trafficReviewRaw,
	}
}

func writeEvidenceArchive(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	archiveFile, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(archiveFile)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}
}
