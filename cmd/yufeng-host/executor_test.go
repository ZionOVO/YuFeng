package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commandv1 "yufeng/proto/gen/commandv1"
)

func TestHostExecutorRestoresFileWhenServiceReloadFails(t *testing.T) {
	executor, artifact := testHostExecutor(t, []byte("replacement"))
	target := filepath.Join(executor.config.AllowedRoots[0], "app.conf")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor.run = func(context.Context, string, string) error { return errors.New("reload failed") }
	command := &commandv1.Command{
		CommandId: "cmd-restore", ArtifactRef: artifact.GetId(), LeaseId: "lease-1", LeaseEpoch: 1,
		Steps: []*commandv1.CommandStep{
			{Primitive: "artifact.stage", ArgsJson: `{}`},
			{Primitive: "file.atomic_replace", ArgsJson: mustJSON(t, map[string]string{"target": target})},
			{Primitive: "service.reload", ArgsJson: `{"service":"nginx"}`},
		},
	}
	var phases []commandv1.StepPhase
	err := executor.Execute(context.Background(), command, func(_ context.Context, receipt *commandv1.StepReceipt) error {
		phases = append(phases, receipt.GetPhase())
		return nil
	})
	if err == nil {
		t.Fatal("reload failure must fail the command")
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "original" {
		t.Fatalf("target=%q want restored original", raw)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("restored target mode=%04o want=0600", info.Mode().Perm())
	}
	if !containsPhase(phases, commandv1.StepPhase_STEP_PHASE_COMPENSATION_STARTED) || !containsPhase(phases, commandv1.StepPhase_STEP_PHASE_COMPENSATED) {
		t.Fatalf("phases=%v do not include durable compensation", phases)
	}
}

func TestHostExecutorCommandDeadlineCancelsServiceAndCompensatesWithParentContext(t *testing.T) {
	executor, artifact := testHostExecutor(t, []byte("replacement"))
	target := filepath.Join(executor.config.AllowedRoots[0], "deadline.conf")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	serviceStarted := make(chan struct{}, 1)
	executor.run = func(ctx context.Context, operation, service string) error {
		if operation != "reload" || service != "nginx" {
			t.Fatalf("operation=%q service=%q", operation, service)
		}
		serviceStarted <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	command := &commandv1.Command{
		CommandId: "cmd-service-deadline", ArtifactRef: artifact.GetId(), LeaseId: "lease-deadline", LeaseEpoch: 1,
		Deadline: timestamppb.New(time.Now().Add(300 * time.Millisecond)),
		Steps: []*commandv1.CommandStep{
			{Primitive: "artifact.stage", ArgsJson: `{}`},
			{Primitive: "file.atomic_replace", ArgsJson: mustJSON(t, map[string]string{"target": target})},
			{Primitive: "service.reload", ArgsJson: `{"service":"nginx"}`},
		},
	}
	parent, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type receiptContextKey struct{}
	parent = context.WithValue(parent, receiptContextKey{}, "parent")
	var phases []commandv1.StepPhase
	err := executor.Execute(parent, command, func(ctx context.Context, receipt *commandv1.StepReceipt) error {
		if err := ctx.Err(); err != nil {
			return errors.New("receipt used the expired command context")
		}
		if ctx.Value(receiptContextKey{}) != "parent" {
			return errors.New("receipt did not use the parent context")
		}
		phases = append(phases, receipt.GetPhase())
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute error=%v want command deadline exceeded", err)
	}
	select {
	case <-serviceStarted:
	default:
		t.Fatal("service runner was not started")
	}
	raw, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != "original" {
		t.Fatalf("target=%q want compensated original", raw)
	}
	if !containsPhase(phases, commandv1.StepPhase_STEP_PHASE_FAILED) ||
		!containsPhase(phases, commandv1.StepPhase_STEP_PHASE_COMPENSATION_STARTED) ||
		!containsPhase(phases, commandv1.StepPhase_STEP_PHASE_COMPENSATED) {
		t.Fatalf("phases=%v do not include deadline failure and durable compensation", phases)
	}
}

func TestHostExecutorCommandDeadlineCancelsServiceActiveVerification(t *testing.T) {
	executor, artifact := testHostExecutor(t, []byte("payload"))
	serviceStarted := make(chan struct{})
	executor.run = func(ctx context.Context, operation, service string) error {
		if operation != "active" || service != "nginx" {
			return errors.New("unexpected service active verification")
		}
		close(serviceStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	command := &commandv1.Command{
		CommandId: "cmd-service-active-deadline", ArtifactRef: artifact.GetId(), LeaseId: "lease-active-deadline", LeaseEpoch: 1,
		Deadline: timestamppb.New(time.Now().Add(time.Second)),
		Steps:    []*commandv1.CommandStep{{Primitive: "verify.service_active", ArgsJson: `{"service":"nginx"}`}},
	}
	parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	executionResult := make(chan error, 1)
	go func() {
		executionResult <- executor.Execute(parent, command, func(ctx context.Context, _ *commandv1.StepReceipt) error {
			return ctx.Err()
		})
	}()
	select {
	case <-serviceStarted:
	case err := <-executionResult:
		t.Fatalf("Execute returned before service active verification started: %v", err)
	case <-parent.Done():
		t.Fatalf("service active verification did not start: %v", parent.Err())
	}
	select {
	case err := <-executionResult:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Execute error=%v want command deadline exceeded", err)
		}
	case <-parent.Done():
		t.Fatalf("Execute did not stop at the command deadline: %v", parent.Err())
	}
}

func TestHostExecutorCommandDeadlineNarrowsArtifactLoadContext(t *testing.T) {
	executor, artifact := testHostExecutor(t, []byte("payload"))
	target := filepath.Join(executor.config.AllowedRoots[0], "artifact-deadline.conf")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	observedDeadline := make(chan time.Time, 1)
	loadCalls := 0
	executor.load = func(ctx context.Context, _ string) (*artifactv1.Artifact, error) {
		loadCalls++
		if loadCalls == 1 {
			return artifact, nil
		}
		got, ok := ctx.Deadline()
		if !ok {
			return nil, errors.New("artifact load context has no deadline")
		}
		observedDeadline <- got
		<-ctx.Done()
		return nil, ctx.Err()
	}
	command := &commandv1.Command{
		CommandId: "cmd-artifact-deadline", ArtifactRef: artifact.GetId(), LeaseId: "lease-artifact-deadline", LeaseEpoch: 1,
		Deadline: timestamppb.New(deadline),
		Steps: []*commandv1.CommandStep{
			{Primitive: "artifact.stage", ArgsJson: `{}`},
			{Primitive: "file.atomic_replace", ArgsJson: mustJSON(t, map[string]string{"target": target})},
			{Primitive: "artifact.stage", ArgsJson: `{}`},
		},
	}
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	var phases []commandv1.StepPhase
	err := executor.Execute(parent, command, func(ctx context.Context, receipt *commandv1.StepReceipt) error {
		if err := ctx.Err(); err != nil {
			return errors.New("receipt used the expired command context")
		}
		phases = append(phases, receipt.GetPhase())
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute error=%v want command deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed >= 700*time.Millisecond {
		t.Fatalf("artifact load elapsed=%s want command deadline before parent deadline", elapsed)
	}
	got := <-observedDeadline
	if delta := got.Sub(deadline); delta < -time.Millisecond || delta > time.Millisecond {
		t.Fatalf("artifact load deadline=%s want command deadline=%s", got, deadline)
	}
	raw, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != "original" {
		t.Fatalf("target=%q want compensated original after artifact deadline", raw)
	}
	if !containsPhase(phases, commandv1.StepPhase_STEP_PHASE_FAILED) ||
		!containsPhase(phases, commandv1.StepPhase_STEP_PHASE_COMPENSATION_STARTED) ||
		!containsPhase(phases, commandv1.StepPhase_STEP_PHASE_COMPENSATED) {
		t.Fatalf("phases=%v do not include artifact deadline failure and durable compensation", phases)
	}
}

func TestHostExecutorExpiredBeforePrepareSkipsArtifactLoadAndCompensates(t *testing.T) {
	executor, artifact := testHostExecutor(t, []byte("replacement"))
	target := filepath.Join(executor.config.AllowedRoots[0], "expired-before-prepare.conf")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	loadCalls := 0
	executor.load = func(context.Context, string) (*artifactv1.Artifact, error) {
		loadCalls++
		return artifact, nil
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	command := &commandv1.Command{
		CommandId: "cmd-expired-before-prepare", ArtifactRef: artifact.GetId(), LeaseId: "lease-before-prepare", LeaseEpoch: 1,
		Deadline: timestamppb.New(deadline),
		Steps: []*commandv1.CommandStep{
			{Primitive: "artifact.stage", ArgsJson: `{}`},
			{Primitive: "file.atomic_replace", ArgsJson: mustJSON(t, map[string]string{"target": target})},
			{Primitive: "artifact.stage", ArgsJson: `{}`},
		},
	}
	phases := make(map[int32][]commandv1.StepPhase)
	err := executor.Execute(context.Background(), command, func(ctx context.Context, receipt *commandv1.StepReceipt) error {
		phases[receipt.GetStepIndex()] = append(phases[receipt.GetStepIndex()], receipt.GetPhase())
		if receipt.GetStepIndex() == 2 && receipt.GetPhase() == commandv1.StepPhase_STEP_PHASE_INTENT_RECORDED {
			delay := time.Until(deadline) + 20*time.Millisecond
			if delay > 0 {
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute error=%v want command deadline exceeded", err)
	}
	if loadCalls != 1 {
		t.Fatalf("artifact load calls=%d want=1 before expired step", loadCalls)
	}
	if containsPhase(phases[2], commandv1.StepPhase_STEP_PHASE_EFFECT_STARTED) {
		t.Fatalf("expired step phases=%v must not cross effect boundary", phases[2])
	}
	state, loadErr := executor.journal.load(command.GetCommandId())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if got := state.Step(2).Phase; got != "failed" {
		t.Fatalf("expired step journal phase=%q want=failed", got)
	}
	assertFileContent(t, target, "original")
	if !containsPhase(phases[1], commandv1.StepPhase_STEP_PHASE_COMPENSATED) {
		t.Fatalf("prior file step phases=%v want compensation", phases[1])
	}
}

func TestHostExecutorDeadlineDuringPrepareDoesNotStartNewEffectAndCompensates(t *testing.T) {
	executor, artifact := testHostExecutor(t, []byte("replacement"))
	firstTarget := filepath.Join(executor.config.AllowedRoots[0], "expired-during-prepare-first.conf")
	secondTarget := filepath.Join(executor.config.AllowedRoots[0], "expired-during-prepare-second.conf")
	if err := os.WriteFile(firstTarget, []byte("first-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondTarget, []byte("second-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	loadCalls := 0
	executor.load = func(ctx context.Context, _ string) (*artifactv1.Artifact, error) {
		loadCalls++
		if loadCalls == 1 {
			return artifact, nil
		}
		<-ctx.Done()
		return artifact, nil
	}
	command := &commandv1.Command{
		CommandId: "cmd-deadline-during-prepare", ArtifactRef: artifact.GetId(), LeaseId: "lease-during-prepare", LeaseEpoch: 1,
		Deadline: timestamppb.New(time.Now().Add(200 * time.Millisecond)),
		Steps: []*commandv1.CommandStep{
			{Primitive: "artifact.stage", ArgsJson: `{}`},
			{Primitive: "file.atomic_replace", ArgsJson: mustJSON(t, map[string]string{"target": firstTarget})},
			{Primitive: "artifact.stage", ArgsJson: `{}`},
			{Primitive: "file.atomic_replace", ArgsJson: mustJSON(t, map[string]string{"target": secondTarget})},
		},
	}
	phases := make(map[int32][]commandv1.StepPhase)
	err := executor.Execute(context.Background(), command, func(_ context.Context, receipt *commandv1.StepReceipt) error {
		phases[receipt.GetStepIndex()] = append(phases[receipt.GetStepIndex()], receipt.GetPhase())
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute error=%v want command deadline exceeded", err)
	}
	if loadCalls != 2 {
		t.Fatalf("artifact load calls=%d want=2", loadCalls)
	}
	if containsPhase(phases[2], commandv1.StepPhase_STEP_PHASE_EFFECT_STARTED) {
		t.Fatalf("expired prepared step phases=%v must not cross effect boundary", phases[2])
	}
	state, loadErr := executor.journal.load(command.GetCommandId())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if got := state.Step(2).Phase; got != "failed" {
		t.Fatalf("expired prepared step journal phase=%q want=failed", got)
	}
	if got := state.Step(3).Phase; got != "" {
		t.Fatalf("later file step journal phase=%q want untouched", got)
	}
	assertFileContent(t, firstTarget, "first-original")
	assertFileContent(t, secondTarget, "second-original")
	if !containsPhase(phases[1], commandv1.StepPhase_STEP_PHASE_COMPENSATED) {
		t.Fatalf("prior file step phases=%v want compensation", phases[1])
	}
}

func TestHostExecutorRestoresCurrentFileAfterRenameDirectorySyncFailure(t *testing.T) {
	executor, artifact := testHostExecutor(t, []byte("replacement"))
	target := filepath.Join(executor.config.AllowedRoots[0], "rename-sync-failure.conf")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	syncFailure := errors.New("directory sync failed after rename")
	syncCalls := 0
	sawReplacementBeforeSyncFailure := false
	executor.syncDir = func(root *os.Root, dir string) error {
		syncCalls++
		if syncCalls == 1 {
			assertFileContent(t, target, "replacement")
			sawReplacementBeforeSyncFailure = true
			return syncFailure
		}
		return syncRootDirectory(root, dir)
	}
	command := &commandv1.Command{
		CommandId: "cmd-rename-sync-failure", ArtifactRef: artifact.GetId(), LeaseId: "lease-rename-sync", LeaseEpoch: 1,
		Steps: []*commandv1.CommandStep{
			{Primitive: "artifact.stage", ArgsJson: `{}`},
			{Primitive: "file.atomic_replace", ArgsJson: mustJSON(t, map[string]string{"target": target})},
		},
	}
	var phases []commandv1.StepPhase
	err := executor.Execute(context.Background(), command, func(_ context.Context, receipt *commandv1.StepReceipt) error {
		if receipt.GetStepIndex() == 1 {
			phases = append(phases, receipt.GetPhase())
		}
		return nil
	})
	if !errors.Is(err, syncFailure) {
		t.Fatalf("Execute error=%v want directory sync failure", err)
	}
	if syncCalls != 2 {
		t.Fatalf("directory sync calls=%d want failed replace plus successful restore", syncCalls)
	}
	if !sawReplacementBeforeSyncFailure {
		t.Fatal("replacement was not observed after rename and before directory sync failure")
	}
	assertFileContent(t, target, "original")
	assertPhases(t, phases,
		commandv1.StepPhase_STEP_PHASE_INTENT_RECORDED,
		commandv1.StepPhase_STEP_PHASE_EFFECT_STARTED,
		commandv1.StepPhase_STEP_PHASE_FAILED,
		commandv1.StepPhase_STEP_PHASE_COMPENSATION_STARTED,
		commandv1.StepPhase_STEP_PHASE_COMPENSATED,
	)
	state, loadErr := executor.journal.load(command.GetCommandId())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	entry := state.Step(1)
	if entry.Phase != "compensated" || entry.CompensationReceiptRef == "" {
		t.Fatalf("current file journal phase=%q compensation_receipt_ref=%q", entry.Phase, entry.CompensationReceiptRef)
	}
}

func TestHostExecutorPrevalidatesEveryPrimitiveBeforeAnyEffect(t *testing.T) {
	tests := []struct {
		name      string
		primitive string
	}{
		{name: "unknown primitive", primitive: "file.unsupported_replace"},
		{name: "empty primitive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor, artifact := testHostExecutor(t, []byte("replacement"))
			target := filepath.Join(executor.config.AllowedRoots[0], "prevalidate.conf")
			if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			loadCalls := 0
			executor.load = func(context.Context, string) (*artifactv1.Artifact, error) {
				loadCalls++
				return artifact, nil
			}
			command := &commandv1.Command{
				CommandId: "cmd-prevalidate-" + strings.ReplaceAll(test.name, " ", "-"), ArtifactRef: artifact.GetId(),
				LeaseId: "lease-prevalidate", LeaseEpoch: 1,
				Steps: []*commandv1.CommandStep{
					{Primitive: "artifact.stage", ArgsJson: `{}`},
					{Primitive: "file.atomic_replace", ArgsJson: mustJSON(t, map[string]string{"target": target})},
					{Primitive: test.primitive, ArgsJson: `{}`},
				},
			}
			var phases []commandv1.StepPhase
			err := executor.Execute(context.Background(), command, func(_ context.Context, receipt *commandv1.StepReceipt) error {
				phases = append(phases, receipt.GetPhase())
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "primitive is not allowed") {
				t.Fatalf("Execute error=%v want primitive rejection", err)
			}
			if loadCalls != 0 {
				t.Fatalf("artifact load calls=%d want=0 before whole-command validation", loadCalls)
			}
			if containsPhase(phases, commandv1.StepPhase_STEP_PHASE_EFFECT_STARTED) {
				t.Fatalf("phases=%v must not cross any effect boundary", phases)
			}
			assertFileContent(t, target, "original")
			if _, statErr := os.Stat(executor.stagePath(artifact.GetId())); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("staged artifact stat error=%v want not exist", statErr)
			}
			state, loadErr := executor.journal.load(command.GetCommandId())
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if state.Step(0).Phase != "" || state.Step(1).Phase != "" || state.Step(2).Phase != "failed" {
				t.Fatalf("journal steps=%+v want only invalid step failed", state.Steps)
			}
		})
	}
}

func TestHostExecutorAtomicReplacePreservesExistingModeAndUsesPrivateDefault(t *testing.T) {
	tests := []struct {
		name     string
		existing bool
		mode     os.FileMode
		want     os.FileMode
	}{
		{name: "existing executable", existing: true, mode: 0o750, want: 0o750},
		{name: "new file", want: 0o600},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor, artifact := testHostExecutor(t, []byte("replacement"))
			target := filepath.Join(executor.config.AllowedRoots[0], "managed-file")
			if test.existing {
				if err := os.WriteFile(target, []byte("original"), test.mode); err != nil {
					t.Fatal(err)
				}
			}
			command := &commandv1.Command{
				CommandId: "cmd-mode-" + strings.ReplaceAll(test.name, " ", "-"), ArtifactRef: artifact.GetId(),
				LeaseId: "lease-mode", LeaseEpoch: 1,
				Steps: []*commandv1.CommandStep{
					{Primitive: "artifact.stage", ArgsJson: `{}`},
					{Primitive: "file.atomic_replace", ArgsJson: mustJSON(t, map[string]string{"target": target})},
				},
			}
			if err := executor.Execute(context.Background(), command, func(context.Context, *commandv1.StepReceipt) error { return nil }); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != test.want {
				t.Fatalf("target mode=%04o want=%04o", info.Mode().Perm(), test.want)
			}
		})
	}
}

func TestHostExecutorRejectsPathTraversalAndSymlink(t *testing.T) {
	executor, artifact := testHostExecutor(t, []byte("replacement"))
	outside := filepath.Join(filepath.Dir(executor.config.AllowedRoots[0]), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(executor.config.AllowedRoots[0], "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{outside, link} {
		command := &commandv1.Command{
			CommandId: "cmd-reject-" + hexDigest(target)[:8], ArtifactRef: artifact.GetId(), LeaseId: "lease-1", LeaseEpoch: 1,
			Steps: []*commandv1.CommandStep{{Primitive: "file.atomic_replace", ArgsJson: mustJSON(t, map[string]string{"target": target})}},
		}
		if err := executor.Execute(context.Background(), command, func(context.Context, *commandv1.StepReceipt) error { return nil }); err == nil {
			t.Fatalf("target %q must be rejected", target)
		}
	}
	raw, err := os.ReadFile(outside)
	if err != nil || string(raw) != "outside" {
		t.Fatalf("outside file was modified: %q err=%v", raw, err)
	}
}

func TestHostExecutorRestartAfterEffectBoundaryReportsUnknownWithoutReplay(t *testing.T) {
	executor, artifact := testHostExecutor(t, []byte("replacement"))
	command := &commandv1.Command{
		CommandId: "cmd-unknown", ArtifactRef: artifact.GetId(), LeaseId: "lease-2", LeaseEpoch: 2,
		Steps: []*commandv1.CommandStep{{Primitive: "service.reload", ArgsJson: `{"service":"nginx"}`}},
	}
	state := &journalState{Steps: map[int]journalStep{0: {Phase: "effect_started"}}}
	if err := executor.journal.save(command.GetCommandId(), state); err != nil {
		t.Fatal(err)
	}
	called := false
	executor.run = func(context.Context, string, string) error { called = true; return nil }
	var phase commandv1.StepPhase
	if err := executor.Execute(context.Background(), command, func(_ context.Context, receipt *commandv1.StepReceipt) error {
		phase = receipt.GetPhase()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if called || phase != commandv1.StepPhase_STEP_PHASE_OUTCOME_UNKNOWN {
		t.Fatalf("called=%v phase=%v want no replay and outcome unknown", called, phase)
	}
}

func TestLoadHostConfigRequiresPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.json")
	if err := os.WriteFile(path, []byte(`{"allowed_roots":["/tmp"],"artifact_public_key_file":"/tmp/key","state_dir":"/tmp/state","init_system":"systemd"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHostConfig(path); err == nil {
		t.Fatal("world-readable host configuration must be rejected")
	}
}

func testHostExecutor(t *testing.T, payload []byte) (*hostExecutor, *artifactv1.Artifact) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_PROCEDURE, Payload: payload, PayloadSchema: "procedure/test"}
	if err := kernel.SignArtifact(artifact, private); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	root, state := filepath.Join(base, "allowed"), filepath.Join(base, "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	executor, err := newHostExecutor(hostConfig{
		AllowedRoots: []string{root}, AllowedServices: []string{"nginx"}, StateDir: state,
		ArtifactPublicKeyFile: filepath.Join(base, "artifact.pub"), InitSystem: "systemd",
	}, public, func(context.Context, string) (*artifactv1.Artifact, error) { return artifact, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(executor.Close)
	return executor, artifact
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func containsPhase(phases []commandv1.StepPhase, target commandv1.StepPhase) bool {
	for _, phase := range phases {
		if phase == target {
			return true
		}
	}
	return false
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != want {
		t.Fatalf("file %q content=%q want=%q", path, raw, want)
	}
}

func assertPhases(t *testing.T, got []commandv1.StepPhase, want ...commandv1.StepPhase) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("phases=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("phases=%v want=%v", got, want)
		}
	}
}
