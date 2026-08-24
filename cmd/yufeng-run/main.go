// yufeng-run 是由 yufeng-agentd 孵化的短命执行进程。
// 只连已连接的本地监督代理；不持访问令牌、刷新令牌或能力令牌。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/agents/runtime"
	"yufeng/lib/kernel"
	commonv1 "yufeng/proto/gen/commonv1"
	modelv1 "yufeng/proto/gen/modelv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

func main() {
	os.Exit(run())
}

func run() (exitCode int) {
	var (
		workID    = flag.String("work-id", os.Getenv("YUFENG_WORK_ID"), "工作项标识")
		ttl       = flag.Duration("ttl", 30*time.Second, "存活时限")
		fail      = flag.Bool("fail", false, "注入失败以走补偿")
		dangerous = flag.Bool("dangerous", false, "注入危险步骤以验证无沙箱拒绝")
	)
	flag.Parse()
	if err := runtime.LimitResources(runtime.LimitsFromEnv()); err != nil {
		log.Printf("limit process: %v", err)
		return 1
	}
	if err := runtime.WatchSupervisor(supervisorFD()); err != nil {
		log.Printf("watch supervisor: %v", err)
		return 1
	}
	nonce := strings.TrimSpace(os.Getenv("YUFENG_NONCE"))
	cli, err := runtime.DialBroker(brokerFD(), nonce)
	if err != nil {
		log.Printf("local supervisor broker is required: %v", err)
		return 1
	}
	defer func() {
		if err := cli.Close(); err != nil {
			log.Printf("close supervisor broker: %v", err)
			exitCode = 1
		}
	}()
	if err := cli.Hello(processEnvKeys()); err != nil {
		log.Printf("broker hello: %v", err)
		return 1
	}

	ctx, stop := runSignalContext(context.Background())
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *ttl)
	defer cancel()
	if err := runtime.WatchCancellation(cancelFD(), cancel); err != nil {
		log.Printf("watch cancellation: %v", err)
		return 1
	}
	input, err := cli.Input()
	if err != nil {
		reportFailure(cli, err)
		return 1
	}
	if input.IsInvestigation() {
		if err := runtime.ApplyInvestigationSandbox(); err != nil {
			reportFailure(cli, fmt.Errorf("apply investigation sandbox: %w", err))
			return 1
		}
		if input.IsTrafficCase() {
			return runTrafficCase(ctx, cli, input, *workID)
		}
		return runInvestigation(ctx, cli, input, *workID)
	}

	var rec runtime.RunRecord
	steps := []runtime.Step{
		{Name: "prepare", Replay: runtime.ReplaySafe, CompensationReplay: runtime.ReplayIdempotent, Run: func(context.Context) error { return nil }, Compensate: func(context.Context) error {
			return cli.Audit("compensate:prepare", *workID)
		}},
		{Name: "apply", Fail: *fail, Dangerous: *dangerous, Replay: runtime.ReplayNever, CompensationReplay: runtime.ReplayIdempotent, Compensate: func(context.Context) error {
			return cli.Audit("compensate:apply", *workID)
		}},
		{Name: "verify", Replay: runtime.ReplaySafe, Run: func(context.Context) error { return nil }},
	}
	err = runtime.ExecuteRecoverable(ctx, steps, false, cli, &rec)
	enc := json.NewEncoder(os.Stdout)
	if encodeErr := enc.Encode(map[string]any{"work_id": *workID, "events": rec.Events, "error": errString(err)}); encodeErr != nil {
		reportFailure(cli, encodeErr)
		return 1
	}
	if err != nil {
		reportFailure(cli, err)
		return 1
	}
	if err := cli.Done("ok"); err != nil {
		log.Printf("report completion: %v", err)
		return 1
	}
	return 0
}

func runSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

func runTrafficCase(ctx context.Context, cli *runtime.BrokerClient, input runtime.WorkInput, workID string) int {
	if err := input.Validate(); err != nil {
		reportFailure(cli, err)
		return 1
	}
	generationID := input.GenerationID
	if generationID == "" {
		sum := sha256.Sum256([]byte(input.CaseID + "\x00" + input.TurnID + "\x00" + input.StepID))
		generationID = "traffic-" + fmt.Sprintf("%x", sum[:16])
	}
	response, err := cli.Generate(&modelv1.GenerateRequest{
		ThreadId: input.ThreadID, TurnId: input.TurnID, StepId: input.StepID, GenerationId: generationID,
		ExpectedItemSequence: input.ExpectedItemSequence, LeaseId: input.LeaseID, LeaseEpoch: input.LeaseEpoch,
		ContextManifest: &modelv1.ContextManifest{}, GenerationLimits: &modelv1.GenerationLimits{MaxOutputTokens: 1024, JsonMode: true},
		InputItems: []*modelv1.GenerateInputItem{{Role: "user", TrustLevel: "untrusted_traffic", SensitiveContentRef: &modelv1.SensitiveContentReference{
			RefId: input.SensitiveContentRef, ContentDigest: input.SensitiveContentDigest, ApprovalId: input.EvidenceApprovalID,
			CaseId: input.CaseID, MaxBytes: input.SensitiveMaxBytes, ExpiresAt: timestamppb.New(input.SensitiveExpiresAt),
		}}},
	})
	if err != nil {
		reportFailure(cli, err)
		return 1
	}
	if len(response.GetOutputItems()) != 1 || response.GetOutputItems()[0].GetKind() != modelv1.GenerateOutputKind_GENERATE_OUTPUT_KIND_TEXT {
		reportFailure(cli, errors.New("traffic finding response is not a single text item"))
		return 1
	}
	content := response.GetOutputItems()[0].GetContent()
	var finding modelv1.TrafficFinding
	if err := protojson.Unmarshal([]byte(content), &finding); err != nil || finding.GetDisposition() == modelv1.TrafficFindingDisposition_TRAFFIC_FINDING_DISPOSITION_UNSPECIFIED {
		reportFailure(cli, errors.New("traffic finding response is invalid"))
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"work_id": workID, "finding": &finding}); err != nil {
		reportFailure(cli, err)
		return 1
	}
	if err := cli.Done(content); err != nil {
		log.Printf("report traffic investigation completion: %v", err)
		return 1
	}
	return 0
}

type investigationBroker interface {
	Invoke(tool, args string) (string, error)
	Audit(kind, payload string) error
	Done(payload string) error
	Fail(payload string) error
}

func runInvestigation(ctx context.Context, cli investigationBroker, input runtime.WorkInput, workID string) int {
	receipt, err := executeInvestigation(ctx, cli, input)
	if err != nil {
		reportInvestigationFailure(cli, err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"work_id": workID, "receipt": json.RawMessage(receipt)}); err != nil {
		reportInvestigationFailure(cli, err)
		return 1
	}
	if err := cli.Done(receipt); err != nil {
		log.Printf("report investigation completion: %v", err)
		return 1
	}
	return 0
}

func executeInvestigation(ctx context.Context, cli investigationBroker, input runtime.WorkInput) (string, error) {
	if err := input.Validate(); err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if input.Ticket.GetForward() != commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_AGENT_INVESTIGATE {
		return "", fmt.Errorf("investigation ticket forward policy is invalid")
	}
	digest, err := kernel.CheckTicketDigest(input.Ticket)
	if err != nil || digest != input.TicketDigest {
		return "", fmt.Errorf("investigation ticket digest mismatch")
	}
	reads := make([]*workerv1.InvestigationToolRead, 0, 2)
	args, err := json.Marshal(map[string]string{"event_id": input.Ticket.GetEventId(), "ticket_digest": input.TicketDigest})
	if err != nil {
		return "", err
	}
	result, err := cli.Invoke("ticket.get", string(args))
	if err != nil {
		return "", fmt.Errorf("read investigation ticket: %w", err)
	}
	reads = append(reads, &workerv1.InvestigationToolRead{ToolName: "ticket.get", ResultDigest: contentDigest(result)})
	if input.ClusterID != "" {
		clusterArgs, err := json.Marshal(map[string]string{"cluster_id": input.ClusterID})
		if err != nil {
			return "", err
		}
		cluster, err := cli.Invoke("cluster.get", string(clusterArgs))
		if err != nil {
			return "", fmt.Errorf("read investigation cluster: %w", err)
		}
		reads = append(reads, &workerv1.InvestigationToolRead{ToolName: "cluster.get", ResultDigest: contentDigest(cluster)})
	}
	if err := cli.Audit("investigation.ticket_consumed", input.TicketDigest); err != nil {
		return "", err
	}
	receipt := &workerv1.InvestigationReceipt{
		EventId: input.Ticket.GetEventId(), TicketDigest: input.TicketDigest,
		Status: "succeeded", Reads: reads, OutputDigest: runtime.InvestigationOutputDigest(reads),
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(receipt)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func contentDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func reportInvestigationFailure(cli investigationBroker, runErr error) {
	if err := cli.Fail(runErr.Error()); err != nil {
		log.Printf("report investigation failure: %v", err)
	}
}

func reportFailure(cli *runtime.BrokerClient, runErr error) {
	if err := cli.Fail(runErr.Error()); err != nil {
		log.Printf("report failure: %v", err)
	}
}

func brokerFD() int {
	v := strings.TrimSpace(os.Getenv("YUFENG_BROKER_FD"))
	if v == "" {
		return 3
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 3 {
		return -1
	}
	return n
}

func supervisorFD() int {
	v := strings.TrimSpace(os.Getenv("YUFENG_SUPERVISOR_FD"))
	if v == "" {
		return -1
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 3 {
		return -1
	}
	return n
}

func cancelFD() int {
	v := strings.TrimSpace(os.Getenv("YUFENG_CANCEL_FD"))
	if v == "" {
		return -1
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 3 {
		return -1
	}
	return n
}

func processEnvKeys() []string {
	env := os.Environ()
	keys := make([]string, 0, len(env))
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		if name != "" {
			keys = append(keys, name)
		}
	}
	return keys
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
