package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/encoding/protojson"

	workerv1 "yufeng/proto/gen/workerv1"
)

// WorkClient 是工作项领取与回执。
type WorkClient interface {
	Poll(ctx context.Context) (*workerv1.WorkItem, error)
	Complete(ctx context.Context, workID, leaseID string, leaseEpoch int64, result, receipt string) error
	Fail(ctx context.Context, workID, leaseID string, leaseEpoch int64, code, message string) error
	Extend(ctx context.Context, workID, leaseID string, leaseEpoch int64) (LeaseExtension, error)
	Progress(ctx context.Context, workID, leaseID string, leaseEpoch int64, stage, payload string) error
	Saga(ctx context.Context, workID, leaseID string, leaseEpoch int64, progress SagaProgress) (SagaSnapshot, error)
}

// LeaseExtension 返回轮换后的能力令牌和持久取消标记。
type LeaseExtension struct {
	CapabilityToken string
	CancelRequested bool
}

const workerPollSlots = 4

// RunWorker 是监督进程主循环：并行维持有界领取槽，最终并发由 Brain 的 worker 档案门禁决定。
func RunWorker(ctx context.Context, client WorkClient, tools ToolCaller, sess *AccessSession, runBin string, maintain ...func(context.Context) error) error {
	var maintenanceMu sync.Mutex
	serializedMaintenance := func(ctx context.Context) error {
		if len(maintain) == 0 || maintain[0] == nil {
			return nil
		}
		maintenanceMu.Lock()
		defer maintenanceMu.Unlock()
		return maintain[0](ctx)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	group, groupCtx := errgroup.WithContext(runCtx)
	if sess != nil {
		group.Go(func() error {
			select {
			case <-sess.Failure():
				return sess.FailureErr()
			case <-groupCtx.Done():
				return nil
			}
		})
	}
	for range workerPollSlots {
		group.Go(func() error {
			return runWorkerSlot(groupCtx, client, tools, sess, runBin, serializedMaintenance)
		})
	}
	return group.Wait()
}

func runWorkerSlot(ctx context.Context, client WorkClient, tools ToolCaller, sess *AccessSession, runBin string, maintain func(context.Context) error) error {
	bin := ResolveRunBin(runBin)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if maintain != nil {
			if err := maintain(ctx); err != nil {
				if errors.Is(err, ErrAccessSessionFailed) {
					return err
				}
				log.Printf("worker 凭据维护失败，暂停领取新任务: %v", err)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Minute):
				}
				continue
			}
		}
		work, err := client.Poll(ctx)
		if err != nil {
			if failure := accessSessionFailure(sess, err); failure != nil {
				return failure
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}
		if work == nil {
			continue
		}
		if snapshot := work.GetBudgetSnapshot(); snapshot != nil && snapshot.GetState() != "active" {
			if err := client.Fail(ctx, work.WorkId, work.LeaseId, work.LeaseEpoch, "resource_exhausted", "run budget is not active"); err != nil {
				if failure := accessSessionFailure(sess, err); failure != nil {
					return failure
				}
				log.Printf("预算失败回执: %v", err)
			}
			continue
		}
		ttl, _ := time.ParseDuration(work.Ttl)
		if ttl <= 0 {
			ttl = 30 * time.Second
		}
		extendEvery := ttl / 2
		if extendEvery < time.Second {
			extendEvery = time.Second
		}
		if extendEvery > 5*time.Second {
			extendEvery = 5 * time.Second
		}
		input := WorkInputFromProto(work.GetInvestigationInput())
		input.ThreadID, input.TurnID, input.StepID = work.GetThreadId(), work.GetTurnId(), work.GetStepId()
		input.ExpectedItemSequence, input.LeaseEpoch = work.GetExpectedItemSequence(), work.GetLeaseEpoch()
		input.LeaseID, input.GenerationID = work.GetLeaseId(), work.GetResumeGenerationId()
		models, _ := tools.(ModelCaller)
		res := Supervise(ctx, SuperviseConfig{
			Bin:             bin,
			WorkID:          work.WorkId,
			RunID:           work.RunId,
			LeaseID:         work.LeaseId,
			LeaseEpoch:      work.LeaseEpoch,
			TTL:             ttl,
			Limits:          ResourceLimit{MemoryBytes: runMaxMemory, CPUSeconds: runMaxCPUSeconds, Files: runMaxFiles},
			Client:          client,
			Tools:           tools,
			Models:          models,
			AccessSession:   sess,
			CapabilityToken: work.CapabilityToken,
			Input:           input,
			SagaSnapshot:    SagaSnapshotFromProto(work.GetSagaSnapshot()),
			ExtendEvery:     extendEvery,
		})
		if failure := accessSessionFailure(sess, res.Err); failure != nil {
			return failure
		}
		if res.Err != nil {
			code := "run_failed"
			if errors.Is(res.Err, ErrOutcomeUnknown) || strings.Contains(res.Err.Error(), ErrOutcomeUnknown.Error()) {
				code = "outcome_unknown"
			} else if res.Err.Error() == "resource_exhausted" {
				code = "resource_exhausted"
			}
			if err := client.Fail(ctx, work.WorkId, work.LeaseId, work.LeaseEpoch, code, res.Err.Error()); err != nil {
				if failure := accessSessionFailure(sess, err); failure != nil {
					return failure
				}
				log.Printf("失败回执: %v", err)
			}
			continue
		}
		resultRef, receipt, err := completionReceipt(work, res.TerminalPayload)
		if err != nil {
			if failErr := client.Fail(ctx, work.WorkId, work.LeaseId, work.LeaseEpoch, "receipt_failed", err.Error()); failErr != nil {
				if failure := accessSessionFailure(sess, failErr); failure != nil {
					return failure
				}
				log.Printf("失败回执: %v", failErr)
			}
			continue
		}
		if err := client.Complete(ctx, work.WorkId, work.LeaseId, work.LeaseEpoch, resultRef, receipt); err != nil {
			if failure := accessSessionFailure(sess, err); failure != nil {
				return failure
			}
			log.Printf("完成工作项失败: %v", err)
		}
	}
}

func accessSessionFailure(sess *AccessSession, callErr error) error {
	if errors.Is(callErr, ErrAccessSessionFailed) {
		return callErr
	}
	if sess != nil {
		return sess.FailureErr()
	}
	return nil
}

func completionReceipt(work *workerv1.WorkItem, terminal string) (string, string, error) {
	if input := work.GetInvestigationInput(); input != nil {
		if input.GetCaseId() != "" {
			sum := sha256.Sum256([]byte(terminal))
			digest := "sha256:" + hex.EncodeToString(sum[:])
			raw, err := json.Marshal(map[string]string{"status": "ok", "traffic_finding_digest": digest})
			return digest, string(raw), err
		}
		var receipt workerv1.InvestigationReceipt
		if err := protojson.Unmarshal([]byte(terminal), &receipt); err != nil {
			return "", "", err
		}
		if err := ValidateInvestigationReceipt(WorkInputFromProto(input), &receipt); err != nil {
			return "", "", err
		}
		return receipt.GetOutputDigest(), terminal, nil
	}
	raw, err := json.Marshal(map[string]string{
		"status": "ok",
		"runner": "yufeng-run",
		"result": terminal,
	})
	return terminal, string(raw), err
}

// ResolveRunBin 解析 yufeng-run 路径。
func ResolveRunBin(explicit string) string {
	if explicit != "" {
		return explicit
	}
	self, err := os.Executable()
	if err == nil {
		name := "yufeng-run"
		if goruntime.GOOS == "windows" {
			name += ".exe"
		}
		candidate := filepath.Join(filepath.Dir(self), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if goruntime.GOOS == "windows" {
		return "yufeng-run.exe"
	}
	return "yufeng-run"
}
