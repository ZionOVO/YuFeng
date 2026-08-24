package brain

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "yufeng/proto/gen/commonv1"
	grantv1 "yufeng/proto/gen/grantv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

const (
	workerBootstrapDefaultTTL = 30 * time.Minute
	workerBootstrapMaxTTL     = 24 * time.Hour
)

type workerPrincipal struct {
	ID   string
	Kind workerv1.WorkerKind
}

// SeedCentralWorkerBootstrap 为标准 Compose 的固定中央监督进程写入一次性工作负载引导。
// 已完成身份注册时保持现有身份；未消费的同一引导可跨中台重启继续使用。
func SeedCentralWorkerBootstrap(ctx context.Context, pool *pgxpool.Pool, workerID, publicKey, certificateSHA256, token string) error {
	workerID, publicKey, token = strings.TrimSpace(workerID), strings.TrimSpace(publicKey), strings.TrimSpace(token)
	certificateSHA256, err := normalizeCertificateSHA256(certificateSHA256)
	if workerID == "" || publicKey == "" || token == "" || err != nil {
		return errors.New("central worker bootstrap binding is invalid")
	}
	return withTx(ctx, pool, func(tx pgx.Tx) error {
		var identityExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM worker_identities WHERE worker_id=$1)`, workerID).Scan(&identityExists); err != nil {
			return err
		}
		if !identityExists {
			var existingHash string
			err := tx.QueryRow(ctx, `SELECT token_hash FROM worker_bootstrap WHERE worker_id=$1 AND used_at IS NULL AND expires_at>now()`, workerID).Scan(&existingHash)
			if errors.Is(err, pgx.ErrNoRows) {
				_, err = tx.Exec(ctx, `INSERT INTO worker_bootstrap(token_hash, worker_id, worker_kind, public_key, client_cert_sha256, expires_at)
					VALUES($1,$2,'RUN_SUPERVISOR',$3,$4,$5)`, hashToken(token), workerID, publicKey, certificateSHA256, time.Now().Add(24*time.Hour))
			} else if err == nil && existingHash != hashToken(token) {
				return errors.New("central worker bootstrap token changed before consumption")
			}
			if err != nil {
				return err
			}
		}
		var grantExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM grants WHERE subject_kind='worker' AND subject_id=$1)`, workerID).Scan(&grantExists); err != nil {
			return err
		}
		if grantExists {
			return nil
		}
		grantID, err := newID("gr")
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
			VALUES($1,'worker',$2,'[]','[]','system')`, grantID, workerID)
		return err
	})
}

// CreateWorkerBootstrap 为管理员范围内的固定工作进程身份签发一次性引导令牌。
func (s *WorkerServer) CreateWorkerBootstrap(ctx context.Context, req *connect.Request[workerv1.CreateWorkerBootstrapRequest]) (*connect.Response[workerv1.CreateWorkerBootstrapResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if user.GetRole() != commonv1.UserRole_USER_ROLE_ADMIN {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker bootstrap requires administrator"))
	}
	if err := authorizeWrite(ctx, s.pool, user, "grant.write", "asset", "", false); err != nil {
		return nil, err
	}
	workerID := strings.TrimSpace(req.Msg.GetWorkerId())
	publicKey := strings.TrimSpace(req.Msg.GetWorkerPublicKey())
	certHash, err := normalizeCertificateSHA256(req.Msg.GetClientCertSha256())
	if workerID == "" || publicKey == "" || err != nil || !validWorkerKind(req.Msg.GetWorkerKind()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("worker identity binding is invalid"))
	}
	bindings, refs, err := workerBindingGrant(req.Msg.GetBindings())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := checkGrantScope(ctx, s.pool, user, refs); err != nil {
		return nil, err
	}
	ttl := time.Duration(req.Msg.GetExpiresInSeconds()) * time.Second
	if ttl <= 0 {
		ttl = workerBootstrapDefaultTTL
	}
	if ttl < time.Minute || ttl > workerBootstrapMaxTTL {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("worker bootstrap expiry is out of range"))
	}
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().Add(ttl)
	bindingsJSON, err := json.Marshal(bindings)
	if err != nil {
		return nil, err
	}
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM worker_identities WHERE worker_id=$1)`, workerID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return connect.NewError(connect.CodeAlreadyExists, errors.New("worker identity already exists"))
		}
		if _, err := tx.Exec(ctx, `DELETE FROM worker_bootstrap
			WHERE worker_id=$1 AND (used_at IS NOT NULL OR expires_at <= now())`, workerID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO worker_bootstrap(
			token_hash, worker_id, worker_kind, public_key, client_cert_sha256, expires_at)
			VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`,
			tokenHash, workerID, workerKindName(req.Msg.GetWorkerKind()), publicKey, certHash, expiresAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return connect.NewError(connect.CodeAlreadyExists, errors.New("worker bootstrap already exists"))
		}
		if _, err := tx.Exec(ctx, `DELETE FROM grants WHERE subject_kind='worker' AND subject_id=$1`, workerID); err != nil {
			return err
		}
		grantID, err := newID("gr")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
			VALUES($1,'worker',$2,'[]',$3::jsonb,$4)`, grantID, workerID, bindingsJSON, user.GetUserId()); err != nil {
			return err
		}
		for _, ref := range refs {
			if err := appendAuditTx(ctx, tx, "user", user.GetUserId(), "worker.bootstrap", "asset", ref.GetId(),
				map[string]any{"worker_id": workerID, "worker_kind": workerKindName(req.Msg.GetWorkerKind())}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workerv1.CreateWorkerBootstrapResponse{
		BootstrapToken: token,
		ExpiresAt:      timestamppb.New(expiresAt),
	}), nil
}

// RegisterWorkerIdentity 消费精确绑定的引导令牌并签发独立工作负载凭据。
func (s *WorkerServer) RegisterWorkerIdentity(ctx context.Context, req *connect.Request[workerv1.RegisterWorkerIdentityRequest]) (*connect.Response[workerv1.RegisterWorkerIdentityResponse], error) {
	if err := requireWorkerClientCertificate(ctx); err != nil {
		return nil, err
	}
	workerID := strings.TrimSpace(req.Msg.GetWorkerId())
	publicKey := strings.TrimSpace(req.Msg.GetWorkerPublicKey())
	bootstrap := strings.TrimSpace(req.Msg.GetBootstrapToken())
	if workerID == "" || publicKey == "" || bootstrap == "" || !validWorkerKind(req.Msg.GetWorkerKind()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid worker bootstrap"))
	}
	refresh, refreshHash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	access, accessHash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var boundID, boundKind, boundKey, boundCert string
		var expires time.Time
		var used *time.Time
		err := tx.QueryRow(ctx, `SELECT worker_id, worker_kind, public_key, client_cert_sha256, expires_at, used_at
			FROM worker_bootstrap WHERE token_hash=$1 FOR UPDATE`, hashToken(bootstrap)).
			Scan(&boundID, &boundKind, &boundKey, &boundCert, &expires, &used)
		if errors.Is(err, pgx.ErrNoRows) {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or used worker bootstrap"))
		}
		if err != nil {
			return err
		}
		if used != nil || !expires.After(time.Now()) {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or used worker bootstrap"))
		}
		if boundID != workerID || boundKind != workerKindName(req.Msg.GetWorkerKind()) || boundKey != publicKey || boundCert != clientCertHash(ctx) {
			return connect.NewError(connect.CodePermissionDenied, errors.New("worker bootstrap binding mismatch"))
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM worker_identities WHERE worker_id=$1)`, workerID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return connect.NewError(connect.CodeAlreadyExists, errors.New("worker identity already exists"))
		}
		tag, err := tx.Exec(ctx, `UPDATE worker_bootstrap SET used_at=now() WHERE token_hash=$1 AND used_at IS NULL`, hashToken(bootstrap))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("worker bootstrap already used"))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO worker_identities(
			worker_id, worker_kind, public_key, client_cert_sha256, refresh_token_hash, refresh_expires_at)
			VALUES($1,$2,$3,$4,$5,$6)`, workerID, boundKind, publicKey, boundCert,
			refreshHash, time.Now().Add(s.refreshTTL)); err != nil {
			return err
		}
		return storeWorkerAccessToken(ctx, tx, accessHash, workerID, publicKey, boundCert, s.accessTTL)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workerv1.RegisterWorkerIdentityResponse{
		WorkerId: workerID, WorkerKind: req.Msg.GetWorkerKind(), RefreshToken: refresh,
		AccessToken: access, ExpiresIn: int64(s.accessTTL.Seconds()),
	}), nil
}

// RefreshWorkerAccessToken 轮换工作进程刷新令牌并签发新的短期访问令牌。
func (s *WorkerServer) RefreshWorkerAccessToken(ctx context.Context, req *connect.Request[workerv1.RefreshWorkerAccessTokenRequest]) (*connect.Response[workerv1.RefreshWorkerAccessTokenResponse], error) {
	if err := requireWorkerClientCertificate(ctx); err != nil {
		return nil, err
	}
	workerID := strings.TrimSpace(req.Msg.GetWorkerId())
	oldRefresh := strings.TrimSpace(req.Msg.GetRefreshToken())
	if workerID == "" || oldRefresh == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid worker refresh token"))
	}
	refresh, refreshHash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	access, accessHash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var publicKey, certHash string
		err := tx.QueryRow(ctx, `UPDATE worker_identities SET refresh_token_hash=$1,
			refresh_expires_at=$2, updated_at=now()
			WHERE worker_id=$3 AND refresh_token_hash=$4 AND refresh_expires_at > now()
			  AND revoked_at IS NULL AND client_cert_sha256=$5
			RETURNING public_key, client_cert_sha256`,
			refreshHash, time.Now().Add(s.refreshTTL), workerID, hashToken(oldRefresh), clientCertHash(ctx)).Scan(&publicKey, &certHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("invalid worker refresh token"))
		}
		if err != nil {
			return err
		}
		return storeWorkerAccessToken(ctx, tx, accessHash, workerID, publicKey, certHash, s.accessTTL)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workerv1.RefreshWorkerAccessTokenResponse{
		RefreshToken: refresh, AccessToken: access, ExpiresIn: int64(s.accessTTL.Seconds()),
	}), nil
}

// RevokeWorkerIdentity 在管理员范围内终止工作进程身份、令牌和资产授予。
func (s *WorkerServer) RevokeWorkerIdentity(ctx context.Context, req *connect.Request[workerv1.RevokeWorkerIdentityRequest]) (*connect.Response[workerv1.RevokeWorkerIdentityResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if user.GetRole() != commonv1.UserRole_USER_ROLE_ADMIN {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker revocation requires administrator"))
	}
	if err := authorizeWrite(ctx, s.pool, user, "grant.write", "asset", "", false); err != nil {
		return nil, err
	}
	workerID := strings.TrimSpace(req.Msg.GetWorkerId())
	if workerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("worker_id is required"))
	}
	refs, err := loadWorkerBindingRefs(ctx, s.pool, workerID)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("worker identity not found"))
	}
	if err := checkGrantScope(ctx, s.pool, user, refs); err != nil {
		return nil, err
	}
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var revoked *time.Time
		if err := tx.QueryRow(ctx, `SELECT revoked_at FROM worker_identities WHERE worker_id=$1 FOR UPDATE`, workerID).Scan(&revoked); errors.Is(err, pgx.ErrNoRows) {
			return connect.NewError(connect.CodeNotFound, errors.New("worker identity not found"))
		} else if err != nil {
			return err
		}
		if revoked != nil {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE worker_identities SET revoked_at=now(), updated_at=now() WHERE worker_id=$1`, workerID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM worker_access_tokens WHERE worker_id=$1`, workerID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE grants SET expires_at=now()
			WHERE subject_kind='worker' AND subject_id=$1 AND (expires_at IS NULL OR expires_at > now())`, workerID); err != nil {
			return err
		}
		for _, ref := range refs {
			if err := appendAuditTx(ctx, tx, "user", user.GetUserId(), "worker.revoke", "asset", ref.GetId(),
				map[string]any{"worker_id": workerID}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workerv1.RevokeWorkerIdentityResponse{}), nil
}

func (s *WorkerServer) authenticateWorker(ctx context.Context, req interface{ Header() http.Header }) (workerPrincipal, string, error) {
	raw := bearerToken(req.Header().Get("Authorization"))
	if raw == "" {
		return workerPrincipal{}, "", connect.NewError(connect.CodeUnauthenticated, errors.New("missing worker access token"))
	}
	principal, err := requireWorkerToken(ctx, s.pool, raw)
	if err == nil {
		return principal, "workload", nil
	}
	if !s.allowAgentCompat || connect.CodeOf(err) != connect.CodeUnauthenticated {
		return workerPrincipal{}, "", err
	}
	agentID, agentErr := requireAgentToken(ctx, s.pool, raw)
	if agentErr != nil {
		return workerPrincipal{}, "", err
	}
	return workerPrincipal{ID: agentID, Kind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR}, "agent_compat", nil
}

func (s *WorkerServer) requireRunWorker(ctx context.Context, req interface{ Header() http.Header }) (workerPrincipal, error) {
	principal, _, err := s.authenticateWorker(ctx, req)
	if err != nil {
		return workerPrincipal{}, err
	}
	if principal.Kind != workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR {
		return workerPrincipal{}, connect.NewError(connect.CodePermissionDenied, errors.New("analysis worker cannot use run work lane"))
	}
	return principal, nil
}

type workerQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func requireWorkerToken(ctx context.Context, db workerQueryer, raw string) (workerPrincipal, error) {
	if err := requireWorkerClientCertificate(ctx); err != nil {
		return workerPrincipal{}, err
	}
	var workerID, kind, storedPubHash, storedCertHash, publicKey string
	var revoked *time.Time
	err := db.QueryRow(ctx, `SELECT t.worker_id, i.worker_kind, t.public_key_hash,
		t.client_cert_sha256, i.public_key, i.revoked_at
		FROM worker_access_tokens t JOIN worker_identities i USING(worker_id)
		WHERE t.token_hash=$1 AND t.expires_at > now()`, hashToken(raw)).
		Scan(&workerID, &kind, &storedPubHash, &storedCertHash, &publicKey, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return workerPrincipal{}, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired worker token"))
	}
	if err != nil {
		return workerPrincipal{}, err
	}
	if revoked != nil || storedPubHash != hashToken(publicKey) || storedCertHash == "" || storedCertHash != clientCertHash(ctx) {
		return workerPrincipal{}, connect.NewError(connect.CodeUnauthenticated, errors.New("worker token binding is invalid"))
	}
	workerKind, ok := parseWorkerKind(kind)
	if !ok {
		return workerPrincipal{}, connect.NewError(connect.CodeUnauthenticated, errors.New("worker kind is invalid"))
	}
	return workerPrincipal{ID: workerID, Kind: workerKind}, nil
}

func storeWorkerAccessToken(ctx context.Context, db dbTX, tokenHash, workerID, publicKey, certHash string, ttl time.Duration) error {
	if _, err := db.Exec(ctx, `DELETE FROM worker_access_tokens WHERE worker_id=$1 AND expires_at <= now()`, workerID); err != nil {
		return err
	}
	_, err := db.Exec(ctx, `INSERT INTO worker_access_tokens(
		token_hash, worker_id, expires_at, public_key_hash, client_cert_sha256)
		VALUES($1,$2,$3,$4,$5)`, tokenHash, workerID, time.Now().Add(ttl), hashToken(publicKey), certHash)
	return err
}

func requireWorkerClientCertificate(ctx context.Context) error {
	if strings.TrimSpace(clientCertHash(ctx)) == "" {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("worker client certificate is required"))
	}
	return nil
}

func normalizeCertificateSHA256(raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("client certificate fingerprint must be sha256")
	}
	return raw, nil
}

func validWorkerKind(kind workerv1.WorkerKind) bool {
	return kind == workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR
}

func workerKindName(kind workerv1.WorkerKind) string {
	switch kind {
	case workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR:
		return "RUN_SUPERVISOR"
	case workerv1.WorkerKind_WORKER_KIND_ANALYSIS_SCORER:
		return "ANALYSIS_SCORER"
	default:
		return ""
	}
}

func parseWorkerKind(raw string) (workerv1.WorkerKind, bool) {
	switch raw {
	case "RUN_SUPERVISOR":
		return workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR, true
	case "ANALYSIS_SCORER":
		return workerv1.WorkerKind_WORKER_KIND_ANALYSIS_SCORER, true
	default:
		return workerv1.WorkerKind_WORKER_KIND_UNSPECIFIED, false
	}
}

func workerBindingGrant(raw []string) ([]map[string]string, []*grantv1.BindingRef, error) {
	if len(raw) == 0 {
		return nil, nil, errors.New("worker bindings are required")
	}
	seen := map[string]bool{}
	bindings := make([]map[string]string, 0, len(raw))
	refs := make([]*grantv1.BindingRef, 0, len(raw))
	for _, binding := range raw {
		id, kind := parseBindingString(strings.TrimSpace(binding))
		if kind != "asset" || id == "" || id == "*" || id == "bootstrap" {
			return nil, nil, errors.New("worker bindings must be concrete assets")
		}
		key := "asset:" + id
		if seen[key] {
			continue
		}
		seen[key] = true
		bindings = append(bindings, map[string]string{"kind": "asset", "id": id})
		refs = append(refs, &grantv1.BindingRef{Kind: "asset", Id: id})
	}
	return bindings, refs, nil
}

func loadWorkerBindingRefs(ctx context.Context, db dbTX, workerID string) ([]*grantv1.BindingRef, error) {
	rows, err := db.Query(ctx, `SELECT bindings FROM grants WHERE subject_kind='worker' AND subject_id=$1`, workerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var refs []*grantv1.BindingRef
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var stored []*grantv1.BindingRef
		if err := json.Unmarshal(raw, &stored); err != nil {
			return nil, fmt.Errorf("worker grant bindings: %w", err)
		}
		for _, ref := range stored {
			if ref == nil || ref.GetKind() != "asset" || ref.GetId() == "" || seen[ref.GetId()] {
				continue
			}
			seen[ref.GetId()] = true
			refs = append(refs, &grantv1.BindingRef{Kind: "asset", Id: ref.GetId()})
		}
	}
	return refs, rows.Err()
}
