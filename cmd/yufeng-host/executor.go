package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commandv1 "yufeng/proto/gen/commandv1"
)

const (
	hostArtifactLimit = 8 << 20
	hostNewFileMode   = os.FileMode(0o600)
)

var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}$`)

type hostConfig struct {
	AllowedRoots          []string `json:"allowed_roots"`
	AllowedServices       []string `json:"allowed_services"`
	ArtifactPublicKeyFile string   `json:"artifact_public_key_file"`
	StateDir              string   `json:"state_dir"`
	InitSystem            string   `json:"init_system"`
}

type artifactLoader func(context.Context, string) (*artifactv1.Artifact, error)
type receiptReporter func(context.Context, *commandv1.StepReceipt) error
type serviceRunner func(context.Context, string, string) error

type hostExecutor struct {
	config   hostConfig
	public   ed25519.PublicKey
	load     artifactLoader
	journal  *hostJournal
	run      serviceRunner
	syncDir  func(*os.Root, string) error
	services map[string]bool
	roots    map[string]*os.Root
}

type preparedStep struct {
	artifact     *artifactv1.Artifact
	compensation *fileCompensation
}

type fileCompensation struct {
	Target     string `json:"target"`
	BackupPath string `json:"backup_path"`
	Existed    bool   `json:"existed"`
	Mode       uint32 `json:"mode,omitempty"`
}

func (c *fileCompensation) fileMode() os.FileMode {
	if c == nil || os.FileMode(c.Mode).Perm() == 0 {
		return hostNewFileMode
	}
	return os.FileMode(c.Mode).Perm()
}

func loadHostConfig(path string) (hostConfig, error) {
	info, err := os.Stat(path)
	if err != nil {
		return hostConfig{}, err
	}
	if info.Mode().Perm() != 0o600 {
		return hostConfig{}, fmt.Errorf("config permissions must be 0600, got %04o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return hostConfig{}, err
	}
	var cfg hostConfig
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return hostConfig{}, err
	}
	if len(cfg.AllowedRoots) == 0 || strings.TrimSpace(cfg.StateDir) == "" || strings.TrimSpace(cfg.ArtifactPublicKeyFile) == "" {
		return hostConfig{}, errors.New("allowed_roots, state_dir and artifact_public_key_file are required")
	}
	if cfg.InitSystem != "systemd" && cfg.InitSystem != "procd" {
		return hostConfig{}, errors.New("init_system must be systemd or procd")
	}
	return cfg, nil
}

func newHostExecutor(cfg hostConfig, public ed25519.PublicKey, load artifactLoader) (*hostExecutor, error) {
	if len(public) != ed25519.PublicKeySize || load == nil {
		return nil, errors.New("artifact verification key and loader are required")
	}
	if err := ensurePrivateDir(cfg.StateDir); err != nil {
		return nil, err
	}
	executor := &hostExecutor{
		config: cfg, public: public, load: load, syncDir: syncRootDirectory,
		services: make(map[string]bool), roots: make(map[string]*os.Root),
	}
	for _, service := range cfg.AllowedServices {
		if !serviceNamePattern.MatchString(service) {
			return nil, fmt.Errorf("invalid allowed service %q", service)
		}
		executor.services[service] = true
	}
	for _, configuredRoot := range cfg.AllowedRoots {
		rootPath, err := filepath.Abs(configuredRoot)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(rootPath)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("allowed root %q must be an existing non-symlink directory", configuredRoot)
		}
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			return nil, err
		}
		executor.roots[rootPath] = root
	}
	journal, err := openHostJournal(filepath.Join(cfg.StateDir, "commands"))
	if err != nil {
		executor.Close()
		return nil, err
	}
	executor.journal = journal
	executor.run = executor.runService
	return executor, nil
}

func (e *hostExecutor) Close() {
	for _, root := range e.roots {
		_ = root.Close()
	}
}

func (e *hostExecutor) Execute(ctx context.Context, command *commandv1.Command, report receiptReporter) error {
	if command == nil || strings.TrimSpace(command.GetCommandId()) == "" || strings.TrimSpace(command.GetLeaseId()) == "" || report == nil {
		return errors.New("command, lease and receipt reporter are required")
	}
	executionCtx := ctx
	cancelExecution := func() {}
	if deadline := command.GetDeadline(); deadline != nil {
		if err := deadline.CheckValid(); err != nil {
			return fmt.Errorf("invalid command deadline: %w", err)
		}
		deadlineAt := deadline.AsTime()
		if !deadlineAt.After(time.Now()) {
			return errors.New("command deadline has expired")
		}
		executionCtx, cancelExecution = context.WithDeadline(ctx, deadlineAt)
	}
	defer cancelExecution()
	for index, step := range command.GetSteps() {
		if step == nil || !allowedHostPrimitive(step.GetPrimitive()) {
			return e.failWithoutEffect(ctx, command, int32(index), step, report, "primitive is not allowed")
		}
	}
	state, err := e.journal.load(command.GetCommandId())
	if err != nil {
		return err
	}
	for index, step := range command.GetSteps() {
		entry := state.Step(index)
		switch entry.Phase {
		case "effect_started", "compensation_started":
			entry.Phase = "outcome_unknown"
			entry.Error = "host restarted after a side-effect boundary without settlement"
			state.SetStep(index, entry)
			if err := e.journal.save(command.GetCommandId(), state); err != nil {
				return err
			}
			return report(ctx, makeReceipt(int32(index), entry))
		case "outcome_unknown", "failed", "compensated":
			return fmt.Errorf("step %d is terminal with phase %s", index, entry.Phase)
		case "succeeded":
			if err := report(ctx, makeReceipt(int32(index), entry)); err != nil {
				return err
			}
			continue
		}
		if entry.Phase == "" {
			entry = journalStep{Phase: "intent_recorded", GuardDigest: digestText(step.GetGuard())}
			state.SetStep(index, entry)
			if err := e.journal.save(command.GetCommandId(), state); err != nil {
				return err
			}
		}
		if err := report(ctx, makeReceipt(int32(index), entry)); err != nil {
			return err
		}
		if err := executionCtx.Err(); err != nil {
			return e.settleAndCompensate(ctx, command, state, index, entry, err, report)
		}
		prepared, err := e.prepare(executionCtx, command, index, step)
		if err != nil {
			return e.settleAndCompensate(ctx, command, state, index, entry, err, report)
		}
		if err := executionCtx.Err(); err != nil {
			return e.settleAndCompensate(ctx, command, state, index, entry, err, report)
		}
		entry.Phase = "effect_started"
		entry.Compensation = prepared.compensation
		state.SetStep(index, entry)
		if err := e.journal.save(command.GetCommandId(), state); err != nil {
			return err
		}
		if err := report(ctx, makeReceipt(int32(index), entry)); err != nil {
			return err
		}
		output, execErr := e.executePrepared(executionCtx, command, step, prepared)
		if execErr != nil {
			return e.settleAndCompensate(ctx, command, state, index, entry, execErr, report)
		}
		entry.Phase, entry.Output, entry.Error = "succeeded", output, ""
		entry.ReceiptRef = receiptReference(command.GetCommandId(), index, entry)
		state.SetStep(index, entry)
		if err := e.journal.save(command.GetCommandId(), state); err != nil {
			return err
		}
		if err := report(ctx, makeReceipt(int32(index), entry)); err != nil {
			return err
		}
	}
	return nil
}

func (e *hostExecutor) settleAndCompensate(ctx context.Context, command *commandv1.Command, state *journalState, index int, entry journalStep, cause error, report receiptReporter) error {
	if err := e.settleFailure(ctx, command, state, index, entry, cause, report); err != nil && !errors.Is(err, cause) {
		return err
	}
	if err := e.compensate(ctx, command, state, index, report); err != nil {
		return fmt.Errorf("execute: %v; compensate: %w", cause, err)
	}
	return cause
}

func (e *hostExecutor) settleFailure(ctx context.Context, command *commandv1.Command, state *journalState, index int, entry journalStep, cause error, report receiptReporter) error {
	entry.Phase, entry.Error = "failed", boundedError(cause)
	state.SetStep(index, entry)
	if err := e.journal.save(command.GetCommandId(), state); err != nil {
		return err
	}
	if err := report(ctx, makeReceipt(int32(index), entry)); err != nil {
		return err
	}
	return cause
}

func (e *hostExecutor) failWithoutEffect(ctx context.Context, command *commandv1.Command, index int32, step *commandv1.CommandStep, report receiptReporter, message string) error {
	state, err := e.journal.load(command.GetCommandId())
	if err != nil {
		return err
	}
	guard := ""
	if step != nil {
		guard = digestText(step.GetGuard())
	}
	entry := journalStep{Phase: "intent_recorded", GuardDigest: guard}
	state.SetStep(int(index), entry)
	if err := e.journal.save(command.GetCommandId(), state); err != nil {
		return err
	}
	if err := report(ctx, makeReceipt(index, entry)); err != nil {
		return err
	}
	return e.settleFailure(ctx, command, state, int(index), entry, errors.New(message), report)
}

func allowedHostPrimitive(primitive string) bool {
	switch primitive {
	case "sys.probe", "artifact.stage", "file.atomic_replace", "service.reload", "verify.file_digest", "verify.service_active":
		return true
	default:
		return false
	}
}

func (e *hostExecutor) prepare(ctx context.Context, command *commandv1.Command, index int, step *commandv1.CommandStep) (preparedStep, error) {
	switch step.GetPrimitive() {
	case "artifact.stage":
		artifact, err := e.load(ctx, command.GetArtifactRef())
		if err != nil {
			return preparedStep{}, err
		}
		if artifact.GetId() != command.GetArtifactRef() {
			return preparedStep{}, errors.New("artifact reference does not match downloaded artifact")
		}
		if err := kernel.VerifyArtifact(artifact, e.public); err != nil {
			return preparedStep{}, err
		}
		if len(artifact.GetPayload()) > hostArtifactLimit {
			return preparedStep{}, errors.New("artifact payload exceeds host limit")
		}
		return preparedStep{artifact: artifact}, nil
	case "file.atomic_replace":
		var args struct {
			Target string `json:"target"`
		}
		if err := decodeArgs(step.GetArgsJson(), &args); err != nil {
			return preparedStep{}, err
		}
		if strings.TrimSpace(command.GetArtifactRef()) == "" {
			return preparedStep{}, errors.New("artifact_ref is required")
		}
		compensation, err := e.prepareFileCompensation(command.GetCommandId(), index, args.Target)
		if err != nil {
			return preparedStep{}, err
		}
		return preparedStep{compensation: compensation}, nil
	default:
		return preparedStep{}, nil
	}
}

func (e *hostExecutor) executePrepared(ctx context.Context, command *commandv1.Command, step *commandv1.CommandStep, prepared preparedStep) (map[string]any, error) {
	switch step.GetPrimitive() {
	case "sys.probe":
		return map[string]any{"operating_system": "linux", "init_system": e.config.InitSystem}, nil
	case "artifact.stage":
		if prepared.artifact == nil {
			return nil, errors.New("verified artifact is missing")
		}
		payload := prepared.artifact.GetPayload()
		if err := atomicWriteFile(e.stagePath(prepared.artifact.GetId()), payload, 0o600); err != nil {
			return nil, err
		}
		return map[string]any{"artifact_id": prepared.artifact.GetId(), "payload_digest": digestBytes(payload)}, nil
	case "file.atomic_replace":
		var args struct {
			Target string `json:"target"`
		}
		if err := decodeArgs(step.GetArgsJson(), &args); err != nil {
			return nil, err
		}
		payload, err := os.ReadFile(e.stagePath(command.GetArtifactRef()))
		if err != nil {
			return nil, errors.New("staged artifact is unavailable")
		}
		if len(payload) > hostArtifactLimit {
			return nil, errors.New("staged artifact exceeds host limit")
		}
		if err := e.atomicReplace(args.Target, payload, prepared.compensation.fileMode()); err != nil {
			return nil, err
		}
		return map[string]any{"path": args.Target, "sha256": digestBytes(payload)}, nil
	case "service.reload":
		service, err := e.serviceArg(step.GetArgsJson())
		if err != nil {
			return nil, err
		}
		if err := e.run(ctx, "reload", service); err != nil {
			return nil, err
		}
		return map[string]any{"service": service, "reloaded": true}, nil
	case "verify.file_digest":
		var args struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		}
		if err := decodeArgs(step.GetArgsJson(), &args); err != nil {
			return nil, err
		}
		raw, err := e.readAllowedFile(args.Path)
		if err != nil {
			return nil, err
		}
		got := digestBytes(raw)
		if !strings.EqualFold(strings.TrimPrefix(args.SHA256, "sha256:"), strings.TrimPrefix(got, "sha256:")) {
			return nil, errors.New("file digest does not match")
		}
		return map[string]any{"path": args.Path, "sha256": got}, nil
	case "verify.service_active":
		service, err := e.serviceArg(step.GetArgsJson())
		if err != nil {
			return nil, err
		}
		if err := e.run(ctx, "active", service); err != nil {
			return nil, err
		}
		return map[string]any{"service": service, "active": true}, nil
	default:
		return nil, errors.New("primitive is not allowed")
	}
}

func (e *hostExecutor) compensate(ctx context.Context, command *commandv1.Command, state *journalState, failedIndex int, report receiptReporter) error {
	for index := failedIndex; index >= 0; index-- {
		entry := state.Step(index)
		currentFailedEffect := index == failedIndex && entry.Phase == "failed"
		priorSucceededEffect := index < failedIndex && entry.Phase == "succeeded"
		if entry.Compensation == nil || (!currentFailedEffect && !priorSucceededEffect) {
			continue
		}
		// 当前步骤可能已完成原子重命名，只在后续持久化屏障失败；
		// 失败回执必须先落地，再与此前成功步骤一起逆序补偿。
		entry.Phase = "compensation_started"
		state.SetStep(index, entry)
		if err := e.journal.save(command.GetCommandId(), state); err != nil {
			return err
		}
		if err := report(ctx, makeReceipt(int32(index), entry)); err != nil {
			return err
		}
		if err := e.restoreFile(entry.Compensation); err != nil {
			entry.Phase, entry.Error = "outcome_unknown", boundedError(err)
			state.SetStep(index, entry)
			if saveErr := e.journal.save(command.GetCommandId(), state); saveErr != nil {
				return saveErr
			}
			if reportErr := report(ctx, makeReceipt(int32(index), entry)); reportErr != nil {
				return reportErr
			}
			return err
		}
		entry.Phase, entry.Error = "compensated", ""
		entry.CompensationReceiptRef = receiptReference(command.GetCommandId(), index, entry)
		state.SetStep(index, entry)
		if err := e.journal.save(command.GetCommandId(), state); err != nil {
			return err
		}
		if err := report(ctx, makeReceipt(int32(index), entry)); err != nil {
			return err
		}
	}
	return nil
}

func (e *hostExecutor) serviceArg(raw string) (string, error) {
	var args struct {
		Service string `json:"service"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return "", err
	}
	if !e.services[args.Service] {
		return "", errors.New("service is not in the local allowlist")
	}
	return args.Service, nil
}

func (e *hostExecutor) runService(ctx context.Context, operation, service string) error {
	if !e.services[service] {
		return errors.New("service is not in the local allowlist")
	}
	var command *exec.Cmd
	switch e.config.InitSystem {
	case "systemd":
		if operation == "reload" {
			command = exec.CommandContext(ctx, "/bin/systemctl", "reload", service)
		} else {
			command = exec.CommandContext(ctx, "/bin/systemctl", "is-active", "--quiet", service)
		}
	case "procd":
		binary := filepath.Join("/etc/init.d", service)
		if operation == "reload" {
			command = exec.CommandContext(ctx, binary, "reload")
		} else {
			command = exec.CommandContext(ctx, binary, "status")
		}
	default:
		return errors.New("unsupported init system")
	}
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func (e *hostExecutor) stagePath(artifactID string) string {
	return filepath.Join(e.config.StateDir, "staged", hexDigest(artifactID)+".payload")
}

func (e *hostExecutor) allowedTarget(target string) (*os.Root, string, error) {
	abs, err := filepath.Abs(target)
	if err != nil || !filepath.IsAbs(target) {
		return nil, "", errors.New("target must be an absolute path")
	}
	var roots []string
	for rootPath := range e.roots {
		roots = append(roots, rootPath)
	}
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	for _, rootPath := range roots {
		rel, err := filepath.Rel(rootPath, abs)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		root := e.roots[rootPath]
		if err := rejectSymlinkComponents(root, rel); err != nil {
			return nil, "", err
		}
		return root, rel, nil
	}
	return nil, "", errors.New("target is outside configured roots")
}

func rejectSymlinkComponents(root *os.Root, rel string) error {
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	for i := range parts {
		name := filepath.Join(parts[:i+1]...)
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink targets are not allowed")
		}
	}
	return nil
}

func (e *hostExecutor) readAllowedFile(path string) ([]byte, error) {
	root, rel, err := e.allowedTarget(path)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(rel)
	if err != nil || !info.Mode().IsRegular() || info.Size() > hostArtifactLimit {
		return nil, errors.New("target must be a bounded regular file")
	}
	return root.ReadFile(rel)
}

func (e *hostExecutor) prepareFileCompensation(commandID string, index int, target string) (*fileCompensation, error) {
	root, rel, err := e.allowedTarget(target)
	if err != nil {
		return nil, err
	}
	backup := filepath.Join(e.config.StateDir, "backups", hexDigest(fmt.Sprintf("%s:%d", commandID, index))+".bak")
	info, err := root.Stat(rel)
	if errors.Is(err, os.ErrNotExist) {
		return &fileCompensation{Target: target, BackupPath: backup, Mode: uint32(hostNewFileMode)}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > hostArtifactLimit {
		return nil, errors.New("existing target must be a bounded regular file")
	}
	raw, err := root.ReadFile(rel)
	if err != nil {
		return nil, err
	}
	if err := atomicWriteFile(backup, raw, 0o600); err != nil {
		return nil, err
	}
	return &fileCompensation{Target: target, BackupPath: backup, Existed: true, Mode: uint32(info.Mode().Perm())}, nil
}

func (e *hostExecutor) atomicReplace(target string, payload []byte, mode os.FileMode) error {
	root, rel, err := e.allowedTarget(target)
	if err != nil {
		return err
	}
	dir, base := filepath.Dir(rel), filepath.Base(rel)
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp := filepath.Join(dir, "."+base+".yufeng-"+hexDigest(time.Now().String())[:12])
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	removeTemp := true
	defer func() {
		_ = file.Close()
		if removeTemp {
			_ = root.Remove(temp)
		}
	}()
	if err := file.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(temp, rel); err != nil {
		return err
	}
	removeTemp = false
	return e.syncDir(root, dir)
}

func (e *hostExecutor) restoreFile(compensation *fileCompensation) error {
	if compensation == nil {
		return nil
	}
	root, rel, err := e.allowedTarget(compensation.Target)
	if err != nil {
		return err
	}
	if !compensation.Existed {
		if err := root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	raw, err := os.ReadFile(compensation.BackupPath)
	if err != nil {
		return err
	}
	return e.atomicReplace(compensation.Target, raw, compensation.fileMode())
}

func decodeArgs(raw string, target any) error {
	if len(raw) > 16<<10 {
		return errors.New("primitive arguments exceed limit")
	}
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid primitive arguments: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("primitive arguments contain trailing values")
	}
	return nil
}

func makeReceipt(index int32, entry journalStep) *commandv1.StepReceipt {
	output, _ := json.Marshal(entry.Output)
	return &commandv1.StepReceipt{
		StepIndex: index, Phase: stepPhase(entry.Phase), Status: strings.ToUpper(entry.Phase), OutputJson: string(output),
		Error: entry.Error, GuardDigest: entry.GuardDigest, ReceiptRef: entry.ReceiptRef,
		CompensationReceiptRef: entry.CompensationReceiptRef, CompletedAt: timestamppb.Now(),
	}
}

func stepPhase(phase string) commandv1.StepPhase {
	switch phase {
	case "intent_recorded":
		return commandv1.StepPhase_STEP_PHASE_INTENT_RECORDED
	case "effect_started":
		return commandv1.StepPhase_STEP_PHASE_EFFECT_STARTED
	case "succeeded":
		return commandv1.StepPhase_STEP_PHASE_SUCCEEDED
	case "failed":
		return commandv1.StepPhase_STEP_PHASE_FAILED
	case "compensation_started":
		return commandv1.StepPhase_STEP_PHASE_COMPENSATION_STARTED
	case "compensated":
		return commandv1.StepPhase_STEP_PHASE_COMPENSATED
	case "outcome_unknown":
		return commandv1.StepPhase_STEP_PHASE_OUTCOME_UNKNOWN
	default:
		return commandv1.StepPhase_STEP_PHASE_UNSPECIFIED
	}
}

func receiptReference(commandID string, index int, entry journalStep) string {
	raw, _ := json.Marshal(entry)
	return "sha256:" + hexDigest(fmt.Sprintf("%s:%d:%s", commandID, index, raw))
}

func digestText(value string) string { return digestBytes([]byte(value)) }

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hexDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func boundedError(err error) string {
	message := err.Error()
	if len(message) > 2048 {
		return message[:2048]
	}
	return message
}
