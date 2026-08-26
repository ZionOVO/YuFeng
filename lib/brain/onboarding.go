package brain

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	grantv1 "yufeng/proto/gen/grantv1"
	onboardingv1 "yufeng/proto/gen/onboardingv1"

	"yufeng/lib/kernel"
)

// 引导状态线上只许 proto 全名。库表未落地时视为未完成。
//
// [初次配置引导]: ../../docs/glossary.md#onboarding
const (
	OnboardingStatePending         = "ONBOARDING_STATE_PENDING"
	OnboardingStateModelConfigured = "ONBOARDING_STATE_MODEL_CONFIGURED"
	OnboardingStateModelLive       = "ONBOARDING_STATE_MODEL_LIVE"
	OnboardingStateEdgeLive        = "ONBOARDING_STATE_EDGE_LIVE"
	OnboardingStateCompleted       = "ONBOARDING_STATE_COMPLETED"
	OnboardingStateFailed          = "ONBOARDING_STATE_FAILED"

	defaultJarvisAgentID = "jarvis-1"
)

const modelCredentialSlot = "onboarding.model"

// onboardingSnapshot 是引导行的只读投影。
type onboardingSnapshot struct {
	State        string
	LocalAssetID string
	ModelLive    bool
}

func (s onboardingSnapshot) completed() bool {
	return s.State == OnboardingStateCompleted
}

func loadOnboarding(ctx context.Context, pool *pgxpool.Pool) (onboardingSnapshot, error) {
	var snap onboardingSnapshot
	err := pool.QueryRow(ctx, `SELECT state, local_asset_id FROM deployment_onboarding WHERE id=1`).
		Scan(&snap.State, &snap.LocalAssetID)
	if err != nil {
		if isUndefinedTable(err) || errors.Is(err, pgx.ErrNoRows) {
			return onboardingSnapshot{State: OnboardingStatePending}, nil
		}
		return snap, err
	}
	if snap.State == "" {
		snap.State = OnboardingStatePending
	}
	return snap, nil
}

// SeedOnboardingState 写入恰好一行的引导状态（测试与完成路径共用）。
func SeedOnboardingState(ctx context.Context, pool *pgxpool.Pool, state, localAssetID string) error {
	if err := ensureOnboardingTable(ctx, pool); err != nil {
		return err
	}
	live := state == OnboardingStateModelLive || state == OnboardingStateEdgeLive || state == OnboardingStateCompleted
	_, err := pool.Exec(ctx, `INSERT INTO deployment_onboarding(id, state, local_asset_id, model_live)
		VALUES(1,$1,$2,$3)
		ON CONFLICT (id) DO UPDATE SET state=EXCLUDED.state, local_asset_id=EXCLUDED.local_asset_id, model_live=EXCLUDED.model_live, updated_at=now()`,
		state, localAssetID, live)
	return err
}

func ensureOnboardingTable(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS deployment_onboarding (
		id smallint PRIMARY KEY CHECK (id = 1),
		state text NOT NULL,
		local_asset_id text NOT NULL DEFAULT '',
		base_url text NOT NULL DEFAULT '',
		model text NOT NULL DEFAULT '',
		last_error text NOT NULL DEFAULT '',
		model_live boolean NOT NULL DEFAULT false,
		dialect text NOT NULL DEFAULT 'MODEL_DIALECT_OPENAI_CHAT',
		updated_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE deployment_onboarding ADD COLUMN IF NOT EXISTS model_live boolean NOT NULL DEFAULT false`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE deployment_onboarding ADD COLUMN IF NOT EXISTS dialect text NOT NULL DEFAULT 'MODEL_DIALECT_OPENAI_CHAT'`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE deployment_onboarding ADD COLUMN IF NOT EXISTS deployment_spec jsonb NOT NULL DEFAULT '{}'`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE deployment_onboarding ADD COLUMN IF NOT EXISTS deployment_spec_digest text NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE deployment_onboarding ADD COLUMN IF NOT EXISTS local_unit_id text NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE deployment_onboarding ADD COLUMN IF NOT EXISTS expected_generation_id text NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE deployment_onboarding ADD COLUMN IF NOT EXISTS expected_generation_seq bigint NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE deployment_onboarding ADD COLUMN IF NOT EXISTS expected_listen_plan_version bigint NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS credential_slots (
		slot_id text PRIMARY KEY,
		kind text NOT NULL,
		secret_hash text NOT NULL DEFAULT '',
		secret_ciphertext bytea,
		secret_hint text NOT NULL DEFAULT '',
		created_at timestamptz NOT NULL DEFAULT now(),
		updated_at timestamptz NOT NULL DEFAULT now()
	)`)
	return err
}

func resetOnboardingForTest(ctx context.Context, pool *pgxpool.Pool) {
	if err := ensureOnboardingTable(ctx, pool); err != nil {
		return
	}
	_, _ = pool.Exec(ctx, `UPDATE deployment_onboarding SET
		state=$1, local_asset_id='', base_url='', model='', last_error='', model_live=false,
		dialect='MODEL_DIALECT_OPENAI_CHAT', deployment_spec='{}', deployment_spec_digest='', local_unit_id='',
		expected_generation_id='', expected_generation_seq=0, expected_listen_plan_version=0, updated_at=now() WHERE id=1`,
		OnboardingStatePending)
	_, _ = pool.Exec(ctx, `DELETE FROM credential_slots WHERE slot_id=$1`, modelCredentialSlot)
	_, _ = pool.Exec(ctx, `DELETE FROM model_gateway_calls`)
}

func onboardingIncompleteError() error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("onboarding_incomplete"))
}

func requireCompletedOnboarding(ctx context.Context, pool *pgxpool.Pool) error {
	snap, err := loadOnboarding(ctx, pool)
	if err != nil {
		return err
	}
	if !snap.completed() {
		return onboardingIncompleteError()
	}
	return nil
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

// completeCheck 是 §19.1 两条谓词的输入。
type completeCheck struct {
	AdminUserID   string
	JarvisAgentID string
	LocalAssetID  string
	ModelLive     bool
}

// missingCompletePredicates 返回未满足的 §19.1 谓词编号（1–2，升序）。
func missingCompletePredicates(ctx context.Context, db dbTX, c completeCheck) []int32 {
	var missing []int32
	if !c.ModelLive {
		missing = append(missing, 1)
	}
	if !jarvisOnline(ctx, db, c.JarvisAgentID) {
		missing = append(missing, 2)
	}
	return missing
}

func jarvisOnline(ctx context.Context, db dbTX, agentID string) bool {
	if agentID == "" {
		agentID = defaultJarvisAgentID
	}
	var last *time.Time
	err := db.QueryRow(ctx, `SELECT last_heartbeat_at FROM agents WHERE agent_id=$1`, agentID).Scan(&last)
	if err != nil || last == nil {
		return false
	}
	return time.Since(*last) <= kernel.JarvisOnlineWindow
}

func onboardingSlotChanged(before, after onboardingView) bool {
	return before.SecretPlain != after.SecretPlain || before.BaseURL != after.BaseURL || before.Model != after.Model || before.Dialect != after.Dialect
}

func adminSystemTools() []string {
	return []string{"grant.write", "user.admin", "catalog.manage", "console.read", "case.read", "case.manage", "evidence.approve", "worker.enroll", "worker.capacity.approve", "agent.manage", "asset.create", "asset.update", "asset.delete", "asset.attach", "asset.detach"}
}

// writeAdminSystemGrant 写入引导管理员系统授予；零资产时 Bindings 可以为空。
func writeAdminSystemGrant(ctx context.Context, pool *pgxpool.Pool, adminUserID, localAssetID string) error {
	return writeAdminSystemGrantAssets(ctx, pool, adminUserID, []string{localAssetID})
}

func writeAdminSystemGrantAssets(ctx context.Context, db dbTX, adminUserID string, assetIDs []string) error {
	binds := make([]*grantv1.BindingRef, 0, len(assetIDs))
	seen := map[string]bool{}
	for _, id := range assetIDs {
		if id == "" || id == "bootstrap" || seen[id] {
			continue
		}
		seen[id] = true
		binds = append(binds, &grantv1.BindingRef{Kind: "asset", Id: id})
	}
	tools, err := json.Marshal(adminSystemTools())
	if err != nil {
		return err
	}
	bindsRaw, err := json.Marshal(binds)
	if err != nil {
		return err
	}
	var grantID string
	err = db.QueryRow(ctx, `SELECT grant_id FROM grants
		WHERE subject_kind='user' AND subject_id=$1 AND created_by='system' LIMIT 1`, adminUserID).Scan(&grantID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if grantID == "" {
		grantID, err = newID("gr")
		if err != nil {
			return err
		}
		_, err = db.Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
			VALUES($1,'user',$2,$3::jsonb,$4::jsonb,'system')`, grantID, adminUserID, tools, bindsRaw)
		return err
	}
	_, err = db.Exec(ctx, `UPDATE grants SET tools=$2::jsonb, bindings=$3::jsonb WHERE grant_id=$1`,
		grantID, tools, bindsRaw)
	return err
}

// completeOnboarding 只在 §19.1 两条齐备时写入系统授予并进入 COMPLETED。
func completeOnboarding(ctx context.Context, pool *pgxpool.Pool, c completeCheck) ([]int32, error) {
	missing := missingCompletePredicates(ctx, pool, c)
	if len(missing) > 0 {
		return missing, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("onboarding predicates missing %v", missing))
	}
	err := withTx(ctx, pool, func(tx pgx.Tx) error {
		view, err := loadOnboardingViewTx(ctx, tx, true)
		if err != nil {
			return err
		}
		ids, err := listAssetIDs(ctx, tx)
		if err != nil {
			return err
		}
		if err := writeAdminSystemGrantAssets(ctx, tx, c.AdminUserID, ids); err != nil {
			return err
		}
		return writeOnboardingRow(ctx, tx, OnboardingStateCompleted, c.LocalAssetID, view.BaseURL, view.Model, "", true)
	})
	if err != nil {
		return nil, err
	}
	return nil, nil
}

type onboardingView struct {
	onboardingSnapshot
	BaseURL                   string
	Model                     string
	LastError                 string
	UpdatedAt                 time.Time
	HasSecret                 bool
	SecretHint                string
	SecretPlain               string
	Dialect                   string
	DeploymentSpec            []byte
	DeploymentSpecDigest      string
	LocalUnitID               string
	ExpectedGenerationID      string
	ExpectedGenerationSeq     int64
	ExpectedListenPlanVersion uint64
}

func loadOnboardingView(ctx context.Context, pool *pgxpool.Pool) (onboardingView, error) {
	if err := ensureOnboardingTable(ctx, pool); err != nil {
		return onboardingView{}, err
	}
	return loadOnboardingViewTx(ctx, pool, false)
}

func loadOnboardingViewTx(ctx context.Context, db dbTX, lock bool) (onboardingView, error) {
	var v onboardingView
	var updated time.Time
	q := `SELECT state, local_asset_id, base_url, model, last_error, updated_at, model_live, dialect, deployment_spec, deployment_spec_digest,
		local_unit_id, expected_generation_id, expected_generation_seq, expected_listen_plan_version
		FROM deployment_onboarding WHERE id=1`
	if lock {
		q += " FOR UPDATE"
	}
	err := db.QueryRow(ctx, q).
		Scan(&v.State, &v.LocalAssetID, &v.BaseURL, &v.Model, &v.LastError, &updated, &v.ModelLive, &v.Dialect, &v.DeploymentSpec, &v.DeploymentSpecDigest,
			&v.LocalUnitID, &v.ExpectedGenerationID, &v.ExpectedGenerationSeq, &v.ExpectedListenPlanVersion)
	if err != nil {
		if isUndefinedTable(err) || errors.Is(err, pgx.ErrNoRows) {
			return onboardingView{onboardingSnapshot: onboardingSnapshot{State: OnboardingStatePending}}, nil
		}
		return v, err
	}
	v.UpdatedAt = updated
	if v.State == "" {
		v.State = OnboardingStatePending
	}
	var hint string
	var ct []byte
	err = db.QueryRow(ctx, `SELECT secret_hint, secret_ciphertext FROM credential_slots WHERE slot_id=$1`,
		modelCredentialSlot).Scan(&hint, &ct)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) && !isUndefinedTable(err) {
		return v, err
	}
	if len(ct) > 0 {
		plain, derr := openModelSecret(ct)
		if derr != nil {
			return v, derr
		}
		v.HasSecret = true
		v.SecretHint = hint
		v.SecretPlain = plain
	}
	if d, nerr := normalizeModelDialect(v.Dialect); nerr == nil {
		v.Dialect = d
	}
	return v, nil
}

// writeOnboardingSlot 只改端点、模型名与方言，不碰引导状态。
func writeOnboardingSlot(ctx context.Context, db dbTX, baseURL, model, dialect string) error {
	d, err := normalizeModelDialect(dialect)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `UPDATE deployment_onboarding SET base_url=$1, model=$2, dialect=$3, last_error='', updated_at=now() WHERE id=1`,
		baseURL, model, d)
	return err
}

func writeOnboardingRow(ctx context.Context, db dbTX, state, localAssetID, baseURL, model, lastErr string, modelLive bool) error {
	_, err := db.Exec(ctx, `INSERT INTO deployment_onboarding(id, state, local_asset_id, base_url, model, last_error, model_live)
		VALUES(1,$1,$2,$3,$4,$5,$6)
		ON CONFLICT (id) DO UPDATE SET
			state=EXCLUDED.state,
			local_asset_id=EXCLUDED.local_asset_id,
			base_url=EXCLUDED.base_url,
			model=EXCLUDED.model,
			last_error=EXCLUDED.last_error,
			model_live=EXCLUDED.model_live,
			updated_at=now()`,
		state, localAssetID, baseURL, model, lastErr, modelLive)
	return err
}

func writeModelSecret(ctx context.Context, db dbTX, secret string) error {
	hash, ct, hint, err := sealModelSecret(secret)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO credential_slots(slot_id, kind, secret_hash, secret_ciphertext, secret_hint, updated_at)
		VALUES($1,'model',$2,$3,$4,now())
		ON CONFLICT (slot_id) DO UPDATE SET
			secret_hash=EXCLUDED.secret_hash,
			secret_ciphertext=EXCLUDED.secret_ciphertext,
			secret_hint=EXCLUDED.secret_hint,
			updated_at=now()`,
		modelCredentialSlot, hash, ct, hint)
	return err
}

func clearModelSecret(ctx context.Context, db dbTX) error {
	_, err := db.Exec(ctx, `DELETE FROM credential_slots WHERE slot_id=$1`, modelCredentialSlot)
	return err
}

func sealModelSecret(plain string) (hash string, ct []byte, hint string, err error) {
	hash = hashToken(plain)
	hint = modelSecretHint(plain)
	block, err := aes.NewCipher(modelSlotKey())
	if err != nil {
		return "", nil, "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", nil, "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", nil, "", err
	}
	return hash, gcm.Seal(nonce, nonce, []byte(plain), nil), hint, nil
}

func openModelSecret(ct []byte) (string, error) {
	block, err := aes.NewCipher(modelSlotKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(ct) < ns {
		return "", errors.New("model secret ciphertext is truncated")
	}
	plain, err := gcm.Open(nil, ct[:ns], ct[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func modelSlotKey() []byte {
	sum := sha256.Sum256([]byte("yufeng.credential_slots.model.v1"))
	return sum[:]
}

func modelSecretHint(secret string) string {
	if secret == "" {
		return ""
	}
	if len(secret) <= 4 {
		return strings.Repeat("*", len(secret))
	}
	return "…" + secret[len(secret)-4:]
}

type onboardingAction string

const (
	actionPutModelConfig     onboardingAction = "PutModelConfig"
	actionTestModel          onboardingAction = "TestModelConnectivity"
	actionPutDeploymentSpec  onboardingAction = "PutDeploymentSpecification"
	actionCompleteOnboarding onboardingAction = "CompleteOnboarding"
)

// onboardingEdgeError 判定写远程过程调用是否允许从当前状态出发；非法边不改库。
func onboardingEdgeError(state string, action onboardingAction, modelConfigured, modelLive bool) error {
	if state == OnboardingStateCompleted {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("onboarding already completed"))
	}
	switch action {
	case actionPutModelConfig:
		return nil
	case actionTestModel:
		if !modelConfigured {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("model endpoint is missing"))
		}
		return nil
	case actionPutDeploymentSpec:
		if !modelLive {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("model connectivity is not live"))
		}
		switch state {
		case OnboardingStateModelLive, OnboardingStateFailed, OnboardingStateEdgeLive:
			return nil
		default:
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("illegal onboarding transition"))
		}
	case actionCompleteOnboarding:
		return nil
	default:
		return connect.NewError(connect.CodeUnimplemented, errors.New("unknown onboarding rpc"))
	}
}

func onboardingGateError(missing []int32) error {
	seen := map[int32]bool{}
	var uniq []int32
	for _, n := range missing {
		if n < 1 || n > 2 || seen[n] {
			continue
		}
		seen[n] = true
		uniq = append(uniq, n)
	}
	err := connect.NewError(connect.CodeFailedPrecondition, errors.New("onboarding predicates missing"))
	detail, derr := connect.NewErrorDetail(&onboardingv1.OnboardingGate{MissingPredicates: uniq})
	if derr == nil {
		err.AddDetail(detail)
	}
	return err
}

func protoOnboardingState(name string) onboardingv1.OnboardingState {
	if v, ok := onboardingv1.OnboardingState_value[name]; ok {
		return onboardingv1.OnboardingState(v)
	}
	return onboardingv1.OnboardingState_ONBOARDING_STATE_UNSPECIFIED
}
