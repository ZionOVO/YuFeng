package brain

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	workerv1 "yufeng/proto/gen/workerv1"
)

const workerActivationBundleTTL = 30 * time.Minute

type encryptedWorkerActivation struct {
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	Nonce              string `json:"nonce"`
	Ciphertext         string `json:"ciphertext"`
}

type workerActivationPackage struct {
	EnrollmentID           string `json:"enrollmentId"`
	ActivationBundleRef    string `json:"activationBundleRef"`
	ApprovedManifestDigest string `json:"approvedManifestDigest"`
	SandboxChallengeID     string `json:"sandboxChallengeId"`
	BootstrapToken         string `json:"bootstrapToken"`
	ClientCertificate      string `json:"clientCertificate"`
	CertificateChain       string `json:"certificateChain"`
	CertificateExpiresAt   string `json:"certificateExpiresAt,omitempty"`
}

func validateActivationPublicKey(encoded string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(raw) != 32 {
		return "", errors.New("activation_public_key must be a base64url X25519 public key")
	}
	publicKey, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		return "", errors.New("activation_public_key is invalid")
	}
	probeRaw := make([]byte, 32)
	probeRaw[0] = 1
	probe, err := ecdh.X25519().NewPrivateKey(probeRaw)
	if err != nil {
		return "", errors.New("activation_public_key validation failed")
	}
	if _, err := probe.ECDH(publicKey); err != nil {
		return "", errors.New("activation_public_key is invalid")
	}
	digest := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

func encryptWorkerActivation(publicKey, enrollmentID string, plaintext []byte) ([]byte, error) {
	publicRaw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(publicKey))
	if err != nil {
		return nil, err
	}
	peer, err := ecdh.X25519().NewPublicKey(publicRaw)
	if err != nil {
		return nil, err
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	secret, err := ephemeral.ECDH(peer)
	if err != nil {
		return nil, err
	}
	material := append([]byte("yufeng-worker-activation-v1\x00"+enrollmentID+"\x00"), secret...)
	key := sha256.Sum256(material)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plaintext, []byte(enrollmentID))
	return json.Marshal(encryptedWorkerActivation{
		EphemeralPublicKey: base64.RawURLEncoding.EncodeToString(ephemeral.PublicKey().Bytes()),
		Nonce:              base64.RawURLEncoding.EncodeToString(nonce), Ciphertext: base64.RawURLEncoding.EncodeToString(sealed),
	})
}

// GetWorkerEnrollmentResult 返回可重复取得的加密激活包，不返回任何明文凭据。
func (s *WorkerServer) GetWorkerEnrollmentResult(ctx context.Context, req *connect.Request[workerv1.GetWorkerEnrollmentResultRequest]) (*connect.Response[workerv1.GetWorkerEnrollmentResultResponse], error) {
	enrollmentID := strings.TrimSpace(req.Msg.GetEnrollmentId())
	if enrollmentID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("enrollment_id is required"))
	}
	resp := &workerv1.GetWorkerEnrollmentResultResponse{EnrollmentId: enrollmentID}
	var ciphertext []byte
	var expiresAt *time.Time
	err := s.pool.QueryRow(ctx, `SELECT e.state,COALESCE(b.bundle_ref,''),COALESCE(b.ciphertext,''::bytea),b.expires_at,
		e.approved_manifest_digest,e.sandbox_challenge_id
		FROM worker_enrollments e LEFT JOIN worker_activation_bundles b ON b.enrollment_id=e.enrollment_id
		WHERE e.enrollment_id=$1`, enrollmentID).
		Scan(&resp.State, &resp.ActivationBundleRef, &ciphertext, &expiresAt, &resp.ApprovedManifestDigest, &resp.SandboxChallengeId)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("worker enrollment not found"))
	}
	if err != nil {
		return nil, err
	}
	if expiresAt != nil && time.Now().Before(*expiresAt) {
		resp.EncryptedActivationBundle = ciphertext
		resp.ExpiresAt = timestamppb.New(*expiresAt)
	} else if resp.State == "approved" {
		resp.State = "expired"
	}
	return connect.NewResponse(resp), nil
}

// AcknowledgeWorkerActivation 只允许已经完成身份注册的目标 worker 清除激活密文。
func (s *WorkerServer) AcknowledgeWorkerActivation(ctx context.Context, req *connect.Request[workerv1.AcknowledgeWorkerActivationRequest]) (*connect.Response[workerv1.AcknowledgeWorkerActivationResponse], error) {
	principal, _, err := s.authenticateWorker(ctx, req)
	if err != nil {
		return nil, err
	}
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var workerID string
		var acknowledgedAt *time.Time
		var ciphertext []byte
		err := tx.QueryRow(ctx, `SELECT e.worker_id,b.acknowledged_at,b.ciphertext
			FROM worker_enrollments e
			JOIN worker_activation_bundles b ON b.enrollment_id=e.enrollment_id
			WHERE e.enrollment_id=$1 AND b.bundle_ref=$2
			FOR UPDATE OF e,b`, req.Msg.GetEnrollmentId(), req.Msg.GetActivationBundleRef()).
			Scan(&workerID, &acknowledgedAt, &ciphertext)
		if errors.Is(err, pgx.ErrNoRows) {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("worker activation bundle is not active"))
		}
		if err != nil {
			return err
		}
		if principal.ID != workerID {
			return connect.NewError(connect.CodePermissionDenied, errors.New("worker activation subject mismatch"))
		}
		if acknowledgedAt != nil {
			if len(ciphertext) == 0 {
				return nil
			}
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("worker activation bundle is inconsistent"))
		}
		tag, err := tx.Exec(ctx, `UPDATE worker_activation_bundles SET acknowledged_at=now(),ciphertext=''::bytea
			WHERE enrollment_id=$1 AND bundle_ref=$2 AND acknowledged_at IS NULL`, req.Msg.GetEnrollmentId(), req.Msg.GetActivationBundleRef())
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("worker activation bundle is not active"))
		}
		return appendAuditTx(ctx, tx, "worker", workerID, "worker_activation.acknowledge", "worker_enrollment", req.Msg.GetEnrollmentId(), nil)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workerv1.AcknowledgeWorkerActivationResponse{}), nil
}
