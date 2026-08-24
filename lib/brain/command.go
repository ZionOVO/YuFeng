package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	commandv1 "yufeng/proto/gen/commandv1"
	"yufeng/proto/gen/commandv1/commandv1connect"
)

// CommandServer 是执行单元指令契约后端。
type CommandServer struct {
	pool *pgxpool.Pool
}

// NewCommandServer 构造指令服务。
func NewCommandServer(pool *pgxpool.Pool) *CommandServer { return &CommandServer{pool: pool} }

// Handler 返回 Connect 服务端处理器。
func (s *CommandServer) Handler() (string, http.Handler) {
	return commandv1connect.NewCommandServiceHandler(s, handlerOptions()...)
}

// PollCommands 长轮询属于该单元的待执行指令。
func (s *CommandServer) PollCommands(ctx context.Context, req *connect.Request[commandv1.PollCommandsRequest]) (*connect.Response[commandv1.PollCommandsResponse], error) {
	unitID, err := requireUnit(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	wait := time.Duration(req.Msg.LongPollSeconds) * time.Second
	if wait <= 0 {
		wait = 20 * time.Second
	}
	if wait > pollMaxWait {
		wait = pollMaxWait
	}
	deadline := time.Now().Add(wait)
	for {
		commands, err := s.leaseCommands(ctx, unitID)
		if err != nil {
			return nil, err
		}
		if len(commands) > 0 {
			return connect.NewResponse(&commandv1.PollCommandsResponse{Commands: commands}), nil
		}
		if time.Now().After(deadline) {
			return connect.NewResponse(&commandv1.PollCommandsResponse{}), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollTick):
		}
	}
}

// ReportStep 幂等接收设备执行步骤回执，并拒绝与当前计划或租约不符的结果。
func (s *CommandServer) ReportStep(ctx context.Context, req *connect.Request[commandv1.ReportStepRequest]) (*connect.Response[commandv1.ReportStepResponse], error) {
	unitID, err := requireUnit(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetUnitId() != "" && req.Msg.GetUnitId() != unitID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("unit_id does not match authenticated unit"))
	}
	if strings.TrimSpace(req.Msg.GetCommandId()) == "" || strings.TrimSpace(req.Msg.GetLeaseId()) == "" || req.Msg.GetLeaseEpoch() <= 0 || len(req.Msg.GetReceipts()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("command_id, lease and receipts are required"))
	}
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var boundUnit, status, leaseID string
		var leaseEpoch int64
		var leaseDeadline *time.Time
		var stepsRaw []byte
		if err := tx.QueryRow(ctx, `SELECT unit_id,status,lease_id,lease_epoch,lease_deadline,steps
			FROM commands WHERE command_id=$1 FOR UPDATE`, req.Msg.GetCommandId()).
			Scan(&boundUnit, &status, &leaseID, &leaseEpoch, &leaseDeadline, &stepsRaw); errors.Is(err, pgx.ErrNoRows) {
			return connect.NewError(connect.CodeNotFound, errors.New("command not found"))
		} else if err != nil {
			return err
		}
		if boundUnit != unitID || leaseID != req.Msg.GetLeaseId() || leaseEpoch != req.Msg.GetLeaseEpoch() ||
			leaseDeadline == nil || !leaseDeadline.After(time.Now()) {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("command lease is invalid or expired"))
		}
		if status != "leased" && status != "running" && status != "failed" {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("command is not accepting step receipts"))
		}
		var steps []*commandv1.CommandStep
		if err := json.Unmarshal(stepsRaw, &steps); err != nil {
			return err
		}
		seen := make(map[int32]bool)
		for _, receipt := range req.Msg.GetReceipts() {
			if receipt == nil || receipt.GetStepIndex() < 0 || int(receipt.GetStepIndex()) >= len(steps) || seen[receipt.GetStepIndex()] ||
				len(receipt.GetOutputJson()) > 16<<10 || len(receipt.GetError()) > 2048 || len(receipt.GetReceiptRef()) > 256 ||
				len(receipt.GetCompensationReceiptRef()) > 256 || !json.Valid([]byte(defaultJSONObject(receipt.GetOutputJson()))) {
				return connect.NewError(connect.CodeInvalidArgument, errors.New("step receipt is invalid or exceeds limits"))
			}
			seen[receipt.GetStepIndex()] = true
			phase := commandStepPhaseName(receipt.GetPhase())
			if phase == "" {
				return connect.NewError(connect.CodeInvalidArgument, errors.New("step receipt phase is required"))
			}
			var current string
			err := tx.QueryRow(ctx, `SELECT phase FROM command_step_receipts WHERE command_id=$1 AND step_index=$2 FOR UPDATE`,
				req.Msg.GetCommandId(), receipt.GetStepIndex()).Scan(&current)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil && current == phase {
				continue
			}
			if !validCommandStepTransition(current, phase) {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("step receipt phase transition is invalid"))
			}
			if _, err := tx.Exec(ctx, `INSERT INTO command_step_receipts(
				command_id,step_index,phase,guard_digest,receipt_ref,compensation_receipt_ref,output_json,error)
				VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8)
				ON CONFLICT(command_id,step_index) DO UPDATE SET phase=EXCLUDED.phase,
				guard_digest=EXCLUDED.guard_digest,receipt_ref=EXCLUDED.receipt_ref,
				compensation_receipt_ref=EXCLUDED.compensation_receipt_ref,output_json=EXCLUDED.output_json,
				error=EXCLUDED.error,updated_at=now()`, req.Msg.GetCommandId(), receipt.GetStepIndex(), phase,
				receipt.GetGuardDigest(), receipt.GetReceiptRef(), receipt.GetCompensationReceiptRef(),
				defaultJSONObject(receipt.GetOutputJson()), receipt.GetError()); err != nil {
				return err
			}
		}
		var succeeded, failed, unknown, recorded int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE phase='succeeded'),
			count(*) FILTER (WHERE phase IN ('failed','compensated')),count(*) FILTER (WHERE phase='outcome_unknown'),count(*)
			FROM command_step_receipts WHERE command_id=$1`, req.Msg.GetCommandId()).Scan(&succeeded, &failed, &unknown, &recorded); err != nil {
			return err
		}
		next := "running"
		switch {
		case unknown > 0:
			next = "outcome_unknown"
		case failed > 0:
			next = "failed"
		case recorded == len(steps) && succeeded == len(steps):
			next = "succeeded"
		}
		raw, err := json.Marshal(req.Msg.GetReceipts())
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE commands SET status=$1,result_json=$2,updated_at=now() WHERE command_id=$3`,
			next, string(raw), req.Msg.GetCommandId())
		return err
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&commandv1.ReportStepResponse{}), nil
}

func defaultJSONObject(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}

func commandStepPhaseName(phase commandv1.StepPhase) string {
	switch phase {
	case commandv1.StepPhase_STEP_PHASE_INTENT_RECORDED:
		return "intent_recorded"
	case commandv1.StepPhase_STEP_PHASE_EFFECT_STARTED:
		return "effect_started"
	case commandv1.StepPhase_STEP_PHASE_SUCCEEDED:
		return "succeeded"
	case commandv1.StepPhase_STEP_PHASE_FAILED:
		return "failed"
	case commandv1.StepPhase_STEP_PHASE_COMPENSATION_STARTED:
		return "compensation_started"
	case commandv1.StepPhase_STEP_PHASE_COMPENSATED:
		return "compensated"
	case commandv1.StepPhase_STEP_PHASE_OUTCOME_UNKNOWN:
		return "outcome_unknown"
	default:
		return ""
	}
}

func validCommandStepTransition(current, next string) bool {
	switch current {
	case "":
		return next == "intent_recorded"
	case "intent_recorded":
		return next == "effect_started" || next == "failed"
	case "effect_started":
		return next == "succeeded" || next == "failed" || next == "outcome_unknown"
	case "succeeded":
		return next == "compensation_started"
	case "failed":
		return next == "compensation_started"
	case "compensation_started":
		return next == "compensated" || next == "outcome_unknown"
	default:
		return false
	}
}

func (s *CommandServer) leaseCommands(ctx context.Context, unitID string) ([]*commandv1.Command, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT command_id, run_id, procedure_ref, artifact_ref, target_asset_id, steps::text,
		deadline, idempotency_key,lease_epoch
	FROM commands WHERE unit_id=$1 AND (status='pending' OR (status='leased' AND lease_deadline<now()))
	ORDER BY created_at LIMIT 20 FOR UPDATE SKIP LOCKED`, unitID)
	if err != nil {
		return nil, err
	}
	var ids []string
	var out []*commandv1.Command
	type rowCmd struct {
		id, runID, proc, art, asset, stepsRaw, idem string
		deadline                                    *time.Time
		leaseEpoch                                  int64
	}
	var rowsData []rowCmd
	for rows.Next() {
		var rc rowCmd
		if err := rows.Scan(&rc.id, &rc.runID, &rc.proc, &rc.art, &rc.asset, &rc.stepsRaw, &rc.deadline, &rc.idem, &rc.leaseEpoch); err != nil {
			rows.Close()
			return nil, err
		}
		rowsData = append(rowsData, rc)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	leaseUntil := time.Now().Add(10 * time.Minute)
	for _, rc := range rowsData {
		var steps []*commandv1.CommandStep
		if err := json.Unmarshal([]byte(rc.stepsRaw), &steps); err != nil {
			return nil, err
		}
		ids = append(ids, rc.id)
		commandLeaseID, err := newID("lease")
		if err != nil {
			return nil, err
		}
		cmd := &commandv1.Command{
			CommandId: rc.id, RunId: rc.runID, ProcedureRef: rc.proc, ArtifactRef: rc.art,
			TargetAssetId: rc.asset, Steps: steps, IdempotencyKey: rc.idem,
			LeaseId: commandLeaseID, LeaseEpoch: rc.leaseEpoch + 1, LeaseDeadline: timestamppb.New(leaseUntil),
		}
		if rc.deadline != nil {
			cmd.Deadline = timestamppb.New(*rc.deadline)
		}
		out = append(out, cmd)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	for _, cmd := range out {
		if _, err := tx.Exec(ctx, `UPDATE commands SET status='leased',lease_id=$2,lease_epoch=$3,lease_deadline=$4,updated_at=now()
			WHERE command_id=$1`, cmd.GetCommandId(), cmd.GetLeaseId(), cmd.GetLeaseEpoch(), leaseUntil); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
