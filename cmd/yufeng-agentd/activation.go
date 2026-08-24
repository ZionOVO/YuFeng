package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"connectrpc.com/connect"

	workerv1 "yufeng/proto/gen/workerv1"
	"yufeng/proto/gen/workerv1/workerv1connect"
)

const workerActivationPackageLimit = 2 << 20

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

type encryptedWorkerActivation struct {
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	Nonce              string `json:"nonce"`
	Ciphertext         string `json:"ciphertext"`
}

type workerEnrollmentReceipt struct {
	EnrollmentID string `json:"enrollment_id"`
}

type workerRegistrationApproval struct {
	EnrollmentID           string `json:"enrollment_id"`
	ApprovedManifestDigest string `json:"approved_manifest_digest"`
	SandboxChallengeID     string `json:"sandbox_challenge_id"`
}

// resolveWorkerActivationState 选择可重启的首次激活入口。
// 本地已解密包必须先完成确认与销毁；刷新状态存在时不得重新领取一次性激活材料。
func resolveWorkerActivationState(stateDir, workerID string) (string, bool, error) {
	activationPath := filepath.Join(stateDir, "activation.json")
	if _, err := os.Stat(activationPath); err == nil {
		return activationPath, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	refresh, err := loadWorkerRefresh(workerRefreshFile(stateDir), workerID)
	if err != nil {
		return "", false, err
	}
	return "", refresh == "", nil
}

// retrieveWorkerActivationPackage 等待管理员决定，并把仅本机 X25519 私钥可解密的激活包原子落盘。
func retrieveWorkerActivationPackage(ctx context.Context, client workerv1connect.WorkerServiceClient, stateDir string) (string, error) {
	receiptRaw, err := os.ReadFile(filepath.Join(stateDir, "enrollment.json"))
	if err != nil {
		return "", err
	}
	var receipt workerEnrollmentReceipt
	if err := json.Unmarshal(receiptRaw, &receipt); err != nil || strings.TrimSpace(receipt.EnrollmentID) == "" {
		return "", errors.New("worker enrollment receipt is invalid")
	}
	for {
		response, err := client.GetWorkerEnrollmentResult(ctx, connect.NewRequest(&workerv1.GetWorkerEnrollmentResultRequest{EnrollmentId: receipt.EnrollmentID}))
		if err != nil {
			return "", err
		}
		switch response.Msg.GetState() {
		case "pending":
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(2 * time.Second):
			}
			continue
		case "denied", "expired":
			return "", fmt.Errorf("worker enrollment is %s", response.Msg.GetState())
		case "approved":
		default:
			return "", errors.New("worker enrollment returned an invalid state")
		}
		plaintext, err := decryptWorkerActivation(stateDir, receipt.EnrollmentID, response.Msg.GetEncryptedActivationBundle())
		if err != nil {
			return "", err
		}
		var activation workerActivationPackage
		decoder := json.NewDecoder(strings.NewReader(string(plaintext)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&activation); err != nil {
			return "", fmt.Errorf("decode decrypted worker activation package: %w", err)
		}
		if activation.EnrollmentID != receipt.EnrollmentID || activation.ActivationBundleRef != response.Msg.GetActivationBundleRef() ||
			activation.ApprovedManifestDigest != response.Msg.GetApprovedManifestDigest() || activation.SandboxChallengeID != response.Msg.GetSandboxChallengeId() {
			return "", errors.New("worker activation package binding mismatch")
		}
		path := filepath.Join(stateDir, "activation.json")
		if err := replacePrivateFile(path, plaintext); err != nil {
			return "", err
		}
		return path, nil
	}
}

func decryptWorkerActivation(stateDir, enrollmentID string, raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > workerActivationPackageLimit {
		return nil, errors.New("encrypted worker activation package is empty or too large")
	}
	var encrypted encryptedWorkerActivation
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&encrypted); err != nil {
		return nil, err
	}
	privateEncoded, err := os.ReadFile(workerActivationPrivateKeyPath(stateDir))
	if err != nil {
		return nil, err
	}
	privateRaw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(privateEncoded)))
	if err != nil {
		return nil, errors.New("worker activation private key is invalid")
	}
	privateKey, err := ecdh.X25519().NewPrivateKey(privateRaw)
	if err != nil {
		return nil, err
	}
	peerRaw, err := base64.RawURLEncoding.DecodeString(encrypted.EphemeralPublicKey)
	if err != nil {
		return nil, err
	}
	peer, err := ecdh.X25519().NewPublicKey(peerRaw)
	if err != nil {
		return nil, err
	}
	secret, err := privateKey.ECDH(peer)
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
	nonce, err := base64.RawURLEncoding.DecodeString(encrypted.Nonce)
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(encrypted.Ciphertext)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(enrollmentID))
	if err != nil {
		return nil, errors.New("worker activation package authentication failed")
	}
	return plaintext, nil
}

func replacePrivateFile(path string, content []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".activation-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck // Rename 成功后临时路径已不存在。
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceAgentdFile(temporaryPath, path)
}

// prepareWorkerActivationPackage 校验一次性激活包与本机私钥、worker 标识和客户端用途绑定，再原子安装证书。
//
// 已存在刷新令牌时不回退当前证书；该分支用于首次会话已落盘、但激活包尚未删除的崩溃恢复。
func prepareWorkerActivationPackage(path, stateDir, workerID string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(stateDir) == "" || strings.TrimSpace(workerID) == "" {
		return "", errors.New("worker activation path, state directory and id are required")
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		refresh, refreshErr := loadWorkerRefresh(workerRefreshFile(stateDir), workerID)
		if refreshErr != nil {
			return "", refreshErr
		}
		if refresh == "" {
			return "", errors.New("worker activation package is missing before first session")
		}
		if err := validateCurrentWorkerIdentity(stateDir); err != nil {
			return "", err
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("worker activation package must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("worker activation package must not be readable by group or other users")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close() //nolint:errcheck // 只读激活包校验结束后尽力释放句柄。
	raw, err := io.ReadAll(io.LimitReader(file, workerActivationPackageLimit+1))
	if err != nil {
		return "", err
	}
	if len(raw) > workerActivationPackageLimit {
		return "", errors.New("worker activation package exceeds size limit")
	}
	var activation workerActivationPackage
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&activation); err != nil {
		return "", fmt.Errorf("decode worker activation package: %w", err)
	}
	if strings.TrimSpace(activation.EnrollmentID) == "" || strings.TrimSpace(activation.ActivationBundleRef) == "" ||
		strings.TrimSpace(activation.ApprovedManifestDigest) == "" || strings.TrimSpace(activation.SandboxChallengeID) == "" ||
		strings.TrimSpace(activation.BootstrapToken) == "" ||
		strings.TrimSpace(activation.ClientCertificate) == "" || strings.TrimSpace(activation.CertificateChain) == "" {
		return "", errors.New("worker activation package is incomplete")
	}
	certificateBundle := []byte(activation.ClientCertificate + activation.CertificateChain)
	privateKey, err := os.ReadFile(workerClientKeyPath(stateDir))
	if err != nil {
		return "", err
	}
	pair, err := tls.X509KeyPair(certificateBundle, privateKey)
	if err != nil {
		return "", fmt.Errorf("validate worker activation key binding: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return "", errors.New("worker activation certificate is invalid")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return "", err
	}
	if leaf.Subject.CommonName != workerID {
		return "", errors.New("worker activation certificate subject mismatch")
	}
	if leaf.IsCA || !hasClientAuthenticationUsage(leaf.ExtKeyUsage) {
		return "", errors.New("worker activation certificate is not a client workload certificate")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(activation.CertificateChain)) {
		return "", errors.New("worker activation certificate chain is invalid")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: time.Now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return "", fmt.Errorf("verify worker activation certificate chain: %w", err)
	}
	if activation.CertificateExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339Nano, activation.CertificateExpiresAt)
		if err != nil || expiresAt.Sub(leaf.NotAfter) <= -time.Second || expiresAt.Sub(leaf.NotAfter) >= time.Second {
			return "", errors.New("worker activation certificate expiry mismatch")
		}
	}
	refresh, err := loadWorkerRefresh(workerRefreshFile(stateDir), workerID)
	if err != nil {
		return "", err
	}
	if refresh != "" {
		if err := validateCurrentWorkerIdentity(stateDir); err != nil {
			return "", err
		}
		return activation.BootstrapToken, nil
	}
	if err := replaceWorkerCertificate(workerClientCertificatePath(stateDir), certificateBundle); err != nil {
		return "", err
	}
	return activation.BootstrapToken, nil
}

func validateCurrentWorkerIdentity(stateDir string) error {
	currentCertificate, err := os.ReadFile(workerClientCertificatePath(stateDir))
	if err != nil {
		return errors.New("worker refresh exists without a client certificate")
	}
	privateKey, err := os.ReadFile(workerClientKeyPath(stateDir))
	if err != nil {
		return errors.New("worker refresh exists without a client private key")
	}
	if _, err := tls.X509KeyPair(currentCertificate, privateKey); err != nil {
		return fmt.Errorf("validate current worker client certificate: %w", err)
	}
	return nil
}

func hasClientAuthenticationUsage(usages []x509.ExtKeyUsage) bool {
	for _, usage := range usages {
		if usage == x509.ExtKeyUsageClientAuth {
			return true
		}
	}
	return false
}

// consumeWorkerActivationPackage 在一次性引导令牌被服务端消费且刷新状态落盘后删除精确的激活包。
func consumeWorkerActivationPackage(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("worker activation package path is required")
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// consumeWorkerActivationState 在服务端确认清除密文后销毁本地解密包和一次性 X25519 密钥。
// 激活包最后删除，使中途失败的重启仍会进入确认清理分支。
func consumeWorkerActivationState(path, stateDir string) error {
	if strings.TrimSpace(stateDir) == "" {
		return errors.New("worker activation state directory is required")
	}
	for _, keyPath := range []string{workerActivationPrivateKeyPath(stateDir), workerActivationPublicKeyPath(stateDir)} {
		if err := os.Remove(keyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return consumeWorkerActivationPackage(path)
}

func acknowledgeWorkerActivation(ctx context.Context, client workerv1connect.WorkerServiceClient, path, accessToken string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var activation workerActivationPackage
	if err := json.Unmarshal(raw, &activation); err != nil {
		return err
	}
	req := connect.NewRequest(&workerv1.AcknowledgeWorkerActivationRequest{
		EnrollmentId: activation.EnrollmentID, ActivationBundleRef: activation.ActivationBundleRef,
	})
	req.Header().Set("Authorization", "Bearer "+accessToken)
	_, err = client.AcknowledgeWorkerActivation(ctx, req)
	return err
}

func persistWorkerRegistrationApproval(stateDir, activationPath string) (workerRegistrationApproval, error) {
	raw, err := os.ReadFile(activationPath)
	if err != nil {
		return workerRegistrationApproval{}, err
	}
	var activation workerActivationPackage
	if err := json.Unmarshal(raw, &activation); err != nil {
		return workerRegistrationApproval{}, err
	}
	approval := workerRegistrationApproval{EnrollmentID: activation.EnrollmentID,
		ApprovedManifestDigest: activation.ApprovedManifestDigest, SandboxChallengeID: activation.SandboxChallengeID}
	encoded, err := json.Marshal(approval)
	if err != nil {
		return workerRegistrationApproval{}, err
	}
	if err := replacePrivateFile(filepath.Join(stateDir, "approved-enrollment.json"), encoded); err != nil {
		return workerRegistrationApproval{}, err
	}
	return approval, nil
}

func loadWorkerRegistrationApproval(stateDir string) (workerRegistrationApproval, error) {
	raw, err := os.ReadFile(filepath.Join(stateDir, "approved-enrollment.json"))
	if errors.Is(err, os.ErrNotExist) {
		return workerRegistrationApproval{}, nil
	}
	if err != nil {
		return workerRegistrationApproval{}, err
	}
	var approval workerRegistrationApproval
	if err := json.Unmarshal(raw, &approval); err != nil {
		return workerRegistrationApproval{}, err
	}
	if approval.EnrollmentID == "" || approval.ApprovedManifestDigest == "" || approval.SandboxChallengeID == "" {
		return workerRegistrationApproval{}, errors.New("approved worker enrollment state is incomplete")
	}
	return approval, nil
}
