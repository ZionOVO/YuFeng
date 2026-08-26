package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "yufeng/proto/gen/auditv1"
	"yufeng/proto/gen/auditv1/auditv1connect"

	"yufeng/lib/kernel"
)

// auditLockKey 是审计链尾串行化的 advisory lock 键（字节序列 "YFUF"）。
const auditLockKey = 0x59465546

const auditSchemaVersion = "audit/v2"

type auditCoordinates struct {
	RunID         string
	TurnID        string
	LeaseEpoch    int64
	BudgetID      string
	PayloadDigest string
}

// appendAudit 追加一条审计链记录。使用事务内 advisory lock 串行化链尾读取。
func appendAudit(ctx context.Context, pool *pgxpool.Pool, actorType, actorID, action, objectType, objectID string, details map[string]any) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := appendAuditTx(ctx, tx, actorType, actorID, action, objectType, objectID, details); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func appendAuditTx(ctx context.Context, db dbTX, actorType, actorID, action, objectType, objectID string, details map[string]any) error {
	return appendAuditRecordTx(ctx, db, actorType, actorID, action, objectType, objectID, auditCoordinates{}, details)
}

func appendAgentAuditTx(ctx context.Context, db dbTX, actorType, actorID, action, objectType, objectID string,
	coordinates auditCoordinates, details map[string]any) error {
	return appendAuditRecordTx(ctx, db, actorType, actorID, action, objectType, objectID, coordinates, details)
}

func appendAuditRecordTx(ctx context.Context, db dbTX, actorType, actorID, action, objectType, objectID string,
	coordinates auditCoordinates, details map[string]any) error {
	if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, auditLockKey); err != nil {
		return err
	}
	var prev string
	if err := db.QueryRow(ctx, `SELECT entry_hash FROM audit_entries ORDER BY sequence DESC LIMIT 1`).Scan(&prev); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// PostgreSQL 的 timestamptz 精度是微秒；先截断再计算哈希，避免写入后读回时精度变化。
	now := time.Now().UTC().Truncate(time.Microsecond)
	detailJSON, err := json.Marshal(details)
	if err != nil {
		return err
	}
	entryHash := auditEntryHashV2(now, actorType, actorID, action, objectType, objectID, string(detailJSON), prev, coordinates)
	_, err = db.Exec(ctx, `INSERT INTO audit_entries(
		occurred_at, actor_type, actor_id, action, object_type, object_id, details, previous_hash, entry_hash,
		schema_version, run_id, turn_id, lease_epoch, budget_id, payload_digest)
	VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11,$12,$13,$14,$15)`, now, actorType, actorID, action,
		objectType, objectID, string(detailJSON), prev, entryHash, auditSchemaVersion, coordinates.RunID,
		coordinates.TurnID, coordinates.LeaseEpoch, coordinates.BudgetID, coordinates.PayloadDigest)
	return err
}

// AuditServer 是审计查询与链校验服务。
type AuditServer struct {
	pool *pgxpool.Pool
}

// NewAuditServer 构造审计服务。
func NewAuditServer(pool *pgxpool.Pool) *AuditServer { return &AuditServer{pool: pool} }

// Handler 返回 Connect 服务端处理器。
func (s *AuditServer) Handler() (string, http.Handler) {
	return auditv1connect.NewAuditServiceHandler(s, handlerOptions()...)
}

// ListAuditEntries 按调用者授权范围分页读取只追加审计链。
func (s *AuditServer) ListAuditEntries(ctx context.Context, req *connect.Request[auditv1.ListAuditEntriesRequest]) (*connect.Response[auditv1.ListAuditEntriesResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := requireCompletedOnboarding(ctx, s.pool); err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	scope := scopeFromAccess(access)
	if !scope.hasTool("console.read") || scope.emptyObjects() {
		return connect.NewResponse(&auditv1.ListAuditEntriesResponse{}), nil
	}
	limit := ClampPageSize(req.Msg.GetPageSize())
	offset, err := decodePageOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	actor := strings.TrimSpace(req.Msg.GetActor())
	var since, until *time.Time
	if ts := req.Msg.GetSince(); ts != nil {
		t := ts.AsTime()
		since = &t
	}
	if ts := req.Msg.GetUntil(); ts != nil {
		t := ts.AsTime()
		until = &t
	}
	resp := &auditv1.ListAuditEntriesResponse{}
	rows, err := s.pool.Query(ctx, `WITH visible_entries AS (
		SELECT a.sequence, a.occurred_at, a.actor_type, a.actor_id, a.action, a.object_type, a.object_id,
			a.details::text, a.previous_hash, a.entry_hash, a.run_id, a.turn_id, a.lease_epoch, a.budget_id, a.payload_digest, a.schema_version,
			COALESCE(
			  (a.object_type='asset' AND a.object_id=ANY($10)) OR
			  (a.object_type='release' AND (a.object_id=ANY($12) OR EXISTS(
				SELECT 1 FROM release_assets ra WHERE ra.release_id=a.object_id AND ra.asset_id=ANY($10)))) OR
			  (a.object_type='event' AND EXISTS(SELECT 1 FROM events ev WHERE ev.event_id=a.object_id AND ev.asset_id=ANY($10))) OR
			  (a.object_type='case' AND EXISTS(SELECT 1 FROM investigation_cases c WHERE c.case_id=a.object_id AND c.asset_id=ANY($10))) OR
			  (a.object_type='evidence_approval' AND EXISTS(SELECT 1 FROM evidence_approvals ea WHERE ea.approval_id=a.object_id AND ea.asset_id=ANY($10))) OR
			  (a.object_type='agent_profile' AND EXISTS(SELECT 1 FROM managed_agent_profiles map,
				LATERAL jsonb_array_elements(map.bindings) AS binding(value)
				WHERE map.agent_id=a.object_id AND value->>'kind'='asset' AND value->>'id'=ANY($10))) OR
			  (a.object_type IN ('worker','worker_enrollment') AND $13) OR
			  (a.object_type='worker_capacity_change' AND $14) OR
			  ((a.object_type='run' OR a.run_id<>'') AND EXISTS(SELECT 1 FROM runs r,
				LATERAL jsonb_array_elements_text(r.bindings) AS binding(value)
				WHERE r.run_id=CASE WHEN a.run_id<>'' THEN a.run_id ELSE a.object_id END
				AND ((split_part(value,':',1)='asset' AND split_part(value,':',2)=ANY($10))
				  OR (split_part(value,':',1)='unit' AND split_part(value,':',2)=ANY($11))
				  OR (split_part(value,':',1)='release' AND split_part(value,':',2)=ANY($12))))) OR
			  ((a.object_type='turn' OR a.turn_id<>'') AND EXISTS(SELECT 1 FROM agent_turns at
				JOIN agent_threads th USING(thread_id)
				WHERE at.turn_id=CASE WHEN a.turn_id<>'' THEN a.turn_id ELSE a.object_id END
				AND (th.source_ref=ANY($10) OR th.source_ref=ANY($11) OR th.source_ref=ANY($12)))) OR
			  a.object_id=ANY($10) OR a.object_id=ANY($11) OR a.object_id=ANY($12) OR
			  a.details->>'asset_id'=ANY($10) OR EXISTS(SELECT 1 FROM jsonb_array_elements_text(COALESCE(a.details->'asset_ids','[]'::jsonb)) AS detail_asset(value) WHERE value=ANY($10))
			,false) AS visible
		FROM audit_entries a WHERE ($1='' OR a.object_type=$1) AND ($2='' OR a.object_id=$2)
		  AND ($5='' OR actor_id=$5)
		  AND ($6::timestamptz IS NULL OR occurred_at >= $6)
		  AND ($7::timestamptz IS NULL OR occurred_at <= $7)
		  AND ($8='' OR run_id=$8)
		  AND ($9='' OR turn_id=$9)
	)
	SELECT sequence,occurred_at,actor_type,actor_id,action,object_type,object_id,details,previous_hash,entry_hash,
		run_id,turn_id,lease_epoch,budget_id,payload_digest,schema_version
	FROM visible_entries WHERE visible ORDER BY sequence DESC LIMIT $3 OFFSET $4`,
		req.Msg.ObjectType, req.Msg.ObjectId, limit+1, offset, actor, since, until, req.Msg.GetRunId(), req.Msg.GetTurnId(),
		scope.assetIDs(), mapKeys(scope.units), mapKeys(scope.releases), scope.hasTool("worker.enroll"),
		scope.hasTool("worker.capacity.approve"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e auditv1.AuditEntry
		var at time.Time
		if err := rows.Scan(&e.Sequence, &at, &e.ActorType, &e.ActorId, &e.Action, &e.ObjectType, &e.ObjectId,
			&e.Details, &e.PreviousHash, &e.EntryHash, &e.RunId, &e.TurnId, &e.LeaseEpoch, &e.BudgetId,
			&e.PayloadDigest, &e.SchemaVersion); err != nil {
			return nil, err
		}
		e.OccurredAt = timestamppb.New(at)
		resp.Entries = append(resp.Entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(resp.Entries) > limit {
		resp.Entries = resp.Entries[:limit]
		resp.NextPageToken = encodePageOffset(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

// VerifyChain 重新计算指定范围的哈希链并报告首个断裂位置。
func (s *AuditServer) VerifyChain(ctx context.Context, req *connect.Request[auditv1.VerifyChainRequest]) (*connect.Response[auditv1.VerifyChainResponse], error) {
	if _, err := requireUser(ctx, s.pool, req); err != nil {
		return nil, err
	}
	verified, err := verifyAuditRange(ctx, s.pool, req.Msg.StartSequence, req.Msg.EndSequence)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(verified), nil
}

func verifyAuditRange(ctx context.Context, pool *pgxpool.Pool, startSequence, endSequence int64) (*auditv1.VerifyChainResponse, error) {
	var prev string
	err := pool.QueryRow(ctx, `SELECT entry_hash FROM audit_entries WHERE sequence<$1 ORDER BY sequence DESC LIMIT 1`, startSequence).Scan(&prev)
	if errors.Is(err, pgx.ErrNoRows) {
		err = pool.QueryRow(ctx, `SELECT last_hash FROM audit_partition_anchors WHERE last_sequence<$1
			ORDER BY last_sequence DESC LIMIT 1`, startSequence).Scan(&prev)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	rows, err := pool.Query(ctx, `SELECT sequence, occurred_at, actor_type, actor_id, action, object_type, object_id,
		details::text, previous_hash, entry_hash, run_id, turn_id, lease_epoch, budget_id, payload_digest, schema_version
	FROM audit_entries WHERE sequence BETWEEN $1 AND $2 ORDER BY sequence`, startSequence, endSequence)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var startHash string
	var checked int32
	valid := true
	for rows.Next() {
		var e auditv1.AuditEntry
		var at time.Time
		if err := rows.Scan(&e.Sequence, &at, &e.ActorType, &e.ActorId, &e.Action, &e.ObjectType, &e.ObjectId,
			&e.Details, &e.PreviousHash, &e.EntryHash, &e.RunId, &e.TurnId, &e.LeaseEpoch, &e.BudgetId,
			&e.PayloadDigest, &e.SchemaVersion); err != nil {
			return nil, err
		}
		if e.PreviousHash != prev {
			valid = false
		}
		want := auditEntryHash(at, e.ActorType, e.ActorId, e.Action, e.ObjectType, e.ObjectId, e.Details, e.PreviousHash)
		if e.GetSchemaVersion() == auditSchemaVersion {
			want = auditEntryHashV2(at, e.ActorType, e.ActorId, e.Action, e.ObjectType, e.ObjectId, e.Details,
				e.PreviousHash, auditCoordinates{RunID: e.RunId, TurnID: e.TurnId, LeaseEpoch: e.LeaseEpoch,
					BudgetID: e.BudgetId, PayloadDigest: e.PayloadDigest})
		}
		if want != e.EntryHash {
			valid = false
		}
		if checked == 0 {
			startHash = e.EntryHash
		}
		prev = e.EntryHash
		checked++
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	if checked == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no audit entries in range"))
	}
	return &auditv1.VerifyChainResponse{Valid: valid, StartHash: startHash, EndHash: prev, EntriesChecked: checked}, nil
}

// LatestAuditHead 读取审计链当前头。
func LatestAuditHead(ctx context.Context, pool *pgxpool.Pool) (int64, string, error) {
	var seq int64
	var head string
	err := pool.QueryRow(ctx, `SELECT sequence, entry_hash FROM audit_entries ORDER BY sequence DESC LIMIT 1`).Scan(&seq, &head)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", nil
	}
	return seq, head, err
}

// AppendAuditCheckpointFile 把当前链头追加到进程外只追加文件。
func AppendAuditCheckpointFile(ctx context.Context, pool *pgxpool.Pool, path string) error {
	return appendAuditCheckpointFile(ctx, pool, path, nil)
}

// AppendSignedAuditCheckpointFile 把签名账本链头追加到进程外只追加文件。
func AppendSignedAuditCheckpointFile(ctx context.Context, pool *pgxpool.Pool, path string, signer kernel.Signer) error {
	if signer == nil {
		return errors.New("audit checkpoint signer is required")
	}
	return appendAuditCheckpointFile(ctx, pool, path, signer)
}

func appendAuditCheckpointFile(ctx context.Context, pool *pgxpool.Pool, path string, signer kernel.Signer) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("audit checkpoint path is required")
	}
	seq, head, err := LatestAuditHead(ctx, pool)
	if err != nil {
		return err
	}
	if head == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	var writeErr error
	if signer == nil {
		writeErr = kernel.WriteAuditCheckpoint(f, seq, head)
	} else {
		checkpoint := &kernel.AuditCheckpoint{Sequence: seq, Head: head, CreatedAt: time.Now().UTC()}
		if writeErr = kernel.SignAuditCheckpointWithSigner(checkpoint, signer); writeErr == nil {
			writeErr = kernel.WriteSignedAuditCheckpoint(f, checkpoint)
		}
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	return errors.Join(writeErr, f.Close())
}

// StartAuditCheckpointLoop 按周期把链头写入只追加介质。
func StartAuditCheckpointLoop(ctx context.Context, pool *pgxpool.Pool, path string, period time.Duration, signers ...kernel.Signer) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if period <= 0 {
		period = kernel.AuditCheckpointPeriod
	}
	go func() {
		var signer kernel.Signer
		if len(signers) > 0 {
			signer = signers[0]
		}
		tick := time.NewTicker(period)
		defer tick.Stop()
		_ = appendAuditCheckpointFile(ctx, pool, path, signer)
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				_ = appendAuditCheckpointFile(ctx, pool, path, signer)
			}
		}
	}()
}

// auditEntryHash 是追加与校验共用的规范哈希。
func auditEntryHash(at time.Time, actorType, actorID, action, objectType, objectID, detailsJSON, prev string) string {
	var details any
	// 审计哈希只关心规范化后的 JavaScript 对象表示法形状；details 无法解析时按 null 参与哈希，
	// 追加与校验两侧行为一致即可。
	_ = json.Unmarshal([]byte(detailsJSON), &details)
	canonicalDetails, _ := json.Marshal(details)
	raw := strings.Join([]string{
		at.UTC().Format(time.RFC3339Nano), actorType, actorID, action, objectType, objectID, string(canonicalDetails), prev,
	}, "\n")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func auditEntryHashV2(at time.Time, actorType, actorID, action, objectType, objectID, detailsJSON, prev string,
	coordinates auditCoordinates) string {
	var details any
	_ = json.Unmarshal([]byte(detailsJSON), &details)
	canonicalDetails, _ := json.Marshal(details)
	raw := strings.Join([]string{
		auditSchemaVersion,
		at.UTC().Format(time.RFC3339Nano), actorType, actorID, action, objectType, objectID,
		coordinates.RunID, coordinates.TurnID, strconv.FormatInt(coordinates.LeaseEpoch, 10),
		coordinates.BudgetID, coordinates.PayloadDigest, string(canonicalDetails), prev,
	}, "\n")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func auditPayloadDigest(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == len("sha256:")+sha256.Size*2 && strings.HasPrefix(value, "sha256:") {
		if _, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:")); err == nil {
			return value
		}
	}
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
