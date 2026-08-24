package brain

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"
	agentv1 "yufeng/proto/gen/agentv1"
)

const checkpointMaxBytes = 64 << 10

// GetTurn 返回当前租约覆盖的持久回合投影。
func (s *AgentServer) GetTurn(ctx context.Context, req *connect.Request[agentv1.GetTurnRequest]) (*connect.Response[agentv1.GetTurnResponse], error) {
	turnID := strings.TrimSpace(req.Msg.GetTurnId())
	if turnID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("turn_id is required"))
	}
	if _, _, err := s.authorizeTurnLease(ctx, req.Header(), turnID); err != nil {
		return nil, err
	}
	turn, err := loadAgentTurn(ctx, s.pool, turnID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.GetTurnResponse{Turn: turn}), nil
}

// ListTurnItems 按只增序号分页返回当前租约覆盖的回合账本。
func (s *AgentServer) ListTurnItems(ctx context.Context, req *connect.Request[agentv1.ListTurnItemsRequest]) (*connect.Response[agentv1.ListTurnItemsResponse], error) {
	turnID := strings.TrimSpace(req.Msg.GetTurnId())
	if turnID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("turn_id is required"))
	}
	if _, _, err := s.authorizeTurnLease(ctx, req.Header(), turnID); err != nil {
		return nil, err
	}
	limit := int(req.Msg.GetPageSize())
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `SELECT item_id, turn_id, step_id, item_sequence, kind,
		content_ref, content_digest, payload, created_at FROM agent_items
		WHERE turn_id=$1 AND item_sequence>$2 ORDER BY item_sequence LIMIT $3`,
		turnID, req.Msg.GetAfterSequence(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &agentv1.ListTurnItemsResponse{}
	for rows.Next() {
		var item agentv1.AgentItem
		var kind string
		var payload []byte
		var created time.Time
		if err := rows.Scan(&item.ItemId, &item.TurnId, &item.StepId, &item.ItemSequence,
			&kind, &item.ContentRef, &item.ContentDigest, &payload, &created); err != nil {
			return nil, err
		}
		item.Kind = protoAgentItemKind(kind)
		item.PayloadJson = string(payload)
		item.CreatedAt = timestamppb.New(created)
		resp.Items = append(resp.Items, &item)
		resp.NextAfterSequence = item.ItemSequence
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// YieldTurn 原子保存检查点、释放租约并撤销本次能力令牌。
func (s *AgentServer) YieldTurn(ctx context.Context, req *connect.Request[agentv1.YieldTurnRequest]) (*connect.Response[agentv1.YieldTurnResponse], error) {
	turnID := strings.TrimSpace(req.Msg.GetTurnId())
	if turnID == "" || strings.TrimSpace(req.Msg.GetInstructionId()) == "" || strings.TrimSpace(req.Msg.GetLeaseId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("instruction_id, turn_id and lease_id are required"))
	}
	state, ok := waitingTurnState(req.Msg.GetWaitState())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("wait_state is not a waiting state"))
	}
	checkpoint := []byte(strings.TrimSpace(req.Msg.GetCheckpointJson()))
	if len(checkpoint) == 0 {
		checkpoint = []byte("{}")
	}
	if len(checkpoint) > checkpointMaxBytes || !json.Valid(checkpoint) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("checkpoint_json is invalid or too large"))
	}
	agentID, claims, err := s.authorizeTurnLease(ctx, req.Header(), turnID)
	if err != nil {
		return nil, err
	}
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var expected int64
		var capability string
		err := tx.QueryRow(ctx, `SELECT t.next_item_sequence, i.capability_token
			FROM agent_turns t JOIN agent_instructions i ON i.turn_id=t.turn_id
			WHERE t.turn_id=$1 AND i.instruction_id=$2 AND i.agent_id=$3
			  AND i.lease_id=$4 AND i.lease_epoch=$5 AND i.status='leased'
			  AND i.lease_expires_at>now() FOR UPDATE OF t, i`,
			turnID, req.Msg.GetInstructionId(), agentID, req.Msg.GetLeaseId(), req.Msg.GetLeaseEpoch()).
			Scan(&expected, &capability)
		if errors.Is(err, pgx.ErrNoRows) {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("instruction lease is expired or not held"))
		}
		if err != nil {
			return err
		}
		if expected != req.Msg.GetExpectedItemSequence() {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("expected_item_sequence does not match turn"))
		}
		itemID, err := newID("item")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_items(
			item_id, turn_id, item_sequence, kind, content_digest, payload)
			VALUES($1,$2,$3,'checkpoint',$4,$5::jsonb)`,
			itemID, turnID, expected, agentContentDigest(checkpoint), checkpoint); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_turns SET state=$2, checkpoint=$3::jsonb,
			next_item_sequence=$4, updated_at=now() WHERE turn_id=$1`, turnID, state, checkpoint, expected+1); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_instructions SET status='waiting', lease_id='', lease_expires_at=NULL
			WHERE instruction_id=$1`, req.Msg.GetInstructionId()); err != nil {
			return err
		}
		if claims.TokenID != "" {
			return revokeStoredCapability(ctx, tx, capability, s.signingKey.Public().(ed25519.PublicKey))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	turn, err := loadAgentTurn(ctx, s.pool, turnID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.YieldTurnResponse{Turn: turn}), nil
}

func (s *AgentServer) authorizeTurnLease(ctx context.Context, header http.Header, turnID string) (string, kernel.Claims, error) {
	tokens, err := ParseDualTokens(header)
	if err != nil {
		return "", kernel.Claims{}, err
	}
	agentID, err := requireAgentToken(ctx, s.pool, tokens.Access)
	if err != nil {
		return "", kernel.Claims{}, err
	}
	claims, err := kernel.VerifyCapabilityToken(tokens.Capability, s.signingKey.Public().(ed25519.PublicKey), time.Now())
	if err != nil {
		return "", kernel.Claims{}, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if claims.Audience != "tools" || claims.Subject != agentID {
		return "", kernel.Claims{}, connect.NewError(connect.CodePermissionDenied, errors.New("capability token does not belong to agent"))
	}
	if err := BindDualTokens(agentID, claims); err != nil {
		return "", kernel.Claims{}, err
	}
	if err := requireLiveCapability(ctx, s.pool, claims, tokens.Capability); err != nil {
		return "", kernel.Claims{}, err
	}
	var live int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM agent_instructions
		WHERE turn_id=$1 AND agent_id=$2 AND budget_id=$3 AND lease_epoch=$4
		  AND status='leased' AND lease_expires_at>now()`, turnID, agentID, claims.BudgetID, claims.LeaseEpoch).Scan(&live); err != nil {
		return "", kernel.Claims{}, err
	}
	if live != 1 {
		return "", kernel.Claims{}, connect.NewError(connect.CodePermissionDenied, errors.New("turn is outside current instruction lease"))
	}
	return agentID, claims, nil
}

func loadAgentTurn(ctx context.Context, db dbTX, turnID string) (*agentv1.AgentTurn, error) {
	var out agentv1.AgentTurn
	var state string
	var cursor, checkpoint []byte
	var created, updated time.Time
	err := db.QueryRow(ctx, `SELECT th.thread_id, t.turn_id, th.source_kind, th.source_ref,
		t.source_cursor, t.state, t.budget_id, t.next_item_sequence, t.next_input_sequence,
		t.checkpoint, t.created_at, t.updated_at
		FROM agent_turns t JOIN agent_threads th ON th.thread_id=t.thread_id WHERE t.turn_id=$1`, turnID).
		Scan(&out.ThreadId, &out.TurnId, &out.SourceKind, &out.SourceRef, &cursor, &state,
			&out.BudgetId, &out.NextItemSequence, &out.NextInputSequence, &checkpoint, &created, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("turn not found"))
	}
	if err != nil {
		return nil, err
	}
	out.SourceCursorJson = string(cursor)
	out.CheckpointJson = string(checkpoint)
	out.State = protoAgentTurnState(state)
	out.CreatedAt = timestamppb.New(created)
	out.UpdatedAt = timestamppb.New(updated)
	return &out, nil
}

func waitingTurnState(state agentv1.AgentTurnState) (string, bool) {
	switch state {
	case agentv1.AgentTurnState_AGENT_TURN_STATE_WAITING_TOOL:
		return "waiting_tool", true
	case agentv1.AgentTurnState_AGENT_TURN_STATE_WAITING_CHILD:
		return "waiting_child", true
	case agentv1.AgentTurnState_AGENT_TURN_STATE_WAITING_APPROVAL:
		return "waiting_approval", true
	case agentv1.AgentTurnState_AGENT_TURN_STATE_WAITING_INPUT:
		return "waiting_input", true
	default:
		return "", false
	}
}

func protoAgentTurnState(state string) agentv1.AgentTurnState {
	name := "AGENT_TURN_STATE_" + strings.ToUpper(state)
	if value, ok := agentv1.AgentTurnState_value[name]; ok {
		return agentv1.AgentTurnState(value)
	}
	return agentv1.AgentTurnState_AGENT_TURN_STATE_UNSPECIFIED
}

func protoAgentItemKind(kind string) agentv1.AgentItemKind {
	name := "AGENT_ITEM_KIND_" + strings.ToUpper(kind)
	if value, ok := agentv1.AgentItemKind_value[name]; ok {
		return agentv1.AgentItemKind(value)
	}
	return agentv1.AgentItemKind_AGENT_ITEM_KIND_UNSPECIFIED
}
