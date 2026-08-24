package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	"yufeng/agents/runtime"
	"yufeng/proto/gen/agentv1"
	"yufeng/proto/gen/agentv1/agentv1connect"
	"yufeng/proto/gen/workerv1"
	"yufeng/proto/gen/workerv1/workerv1connect"
)

type persistedWorkerRefresh struct {
	WorkerID string `json:"worker_id"`
	Refresh  string `json:"refresh"`
}

func workerRefreshFile(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "worker-refresh")
}

func loadWorkerRefresh(path, workerID string) (string, error) {
	if path == "" {
		return "", errors.New("worker state directory is required")
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var persisted persistedWorkerRefresh
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return "", fmt.Errorf("decode worker refresh: %w", err)
	}
	if persisted.WorkerID != workerID || strings.TrimSpace(persisted.Refresh) == "" {
		return "", errors.New("worker refresh identity mismatch")
	}
	return persisted.Refresh, nil
}

func saveWorkerRefresh(path, workerID, refresh string) error {
	if path == "" || strings.TrimSpace(workerID) == "" || strings.TrimSpace(refresh) == "" {
		return errors.New("worker refresh state is incomplete")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(persistedWorkerRefresh{WorkerID: workerID, Refresh: refresh})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".worker-refresh-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck // Rename 成功后临时路径已不存在。
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceAgentdFile(tmpPath, path)
}

func workerAccessRenewer(client workerv1connect.WorkerServiceClient, workerID, statePath string,
	save func(path, workerID, refresh string) error) func(context.Context, string) (string, string, error) {
	return func(ctx context.Context, refresh string) (string, string, error) {
		resp, err := client.RefreshWorkerAccessToken(ctx, connect.NewRequest(&workerv1.RefreshWorkerAccessTokenRequest{
			WorkerId: workerID, RefreshToken: refresh,
		}))
		if err != nil {
			return "", "", err
		}
		access, rotatedRefresh := resp.Msg.GetAccessToken(), resp.Msg.GetRefreshToken()
		if strings.TrimSpace(access) == "" || strings.TrimSpace(rotatedRefresh) == "" {
			return "", "", errors.New("worker refresh response is incomplete")
		}
		if save == nil {
			return "", "", fmt.Errorf("%w: worker refresh saver is not configured", runtime.ErrAccessRefreshPersistence)
		}
		if err := save(statePath, workerID, rotatedRefresh); err != nil {
			return "", "", fmt.Errorf("%w: %w", runtime.ErrAccessRefreshPersistence, err)
		}
		return access, rotatedRefresh, nil
	}
}

func establishWorkerSession(ctx context.Context, client workerv1connect.WorkerServiceClient,
	workerID, bootstrap, publicKey, statePath string, sess *runtime.AccessSession) error {
	refresh, err := loadWorkerRefresh(statePath, workerID)
	if err != nil {
		return err
	}
	if refresh != "" {
		err = retryAgentdCall(ctx, func(ctx context.Context) error {
			resp, callErr := client.RefreshWorkerAccessToken(ctx, connect.NewRequest(&workerv1.RefreshWorkerAccessTokenRequest{
				WorkerId: workerID, RefreshToken: refresh,
			}))
			if callErr != nil {
				return callErr
			}
			sess.SetTokens(resp.Msg.AccessToken, resp.Msg.RefreshToken)
			return nil
		})
	} else {
		if strings.TrimSpace(bootstrap) == "" {
			return errors.New("worker bootstrap token is required when no refresh state exists")
		}
		err = retryAgentdCall(ctx, func(ctx context.Context) error {
			resp, callErr := client.RegisterWorkerIdentity(ctx, connect.NewRequest(&workerv1.RegisterWorkerIdentityRequest{
				WorkerId: workerID, WorkerKind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR,
				BootstrapToken: bootstrap, WorkerPublicKey: publicKey,
			}))
			if callErr != nil {
				return callErr
			}
			sess.SetTokens(resp.Msg.AccessToken, resp.Msg.RefreshToken)
			return nil
		})
	}
	if err != nil {
		return err
	}
	_, currentRefresh := sess.Tokens()
	return saveWorkerRefresh(statePath, workerID, currentRefresh)
}

func establishCompatSession(ctx context.Context, client agentv1connect.AgentControlServiceClient,
	workerID, bootstrap, publicKey string, sess *runtime.AccessSession) error {
	return retryAgentdCall(ctx, func(ctx context.Context) error {
		resp, err := client.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
			AgentId: workerID, BootstrapToken: bootstrap, AgentPublicKey: publicKey,
		}))
		if err != nil {
			return err
		}
		sess.SetTokens(resp.Msg.AccessToken, resp.Msg.RefreshToken)
		return nil
	})
}

func registerRunWorker(ctx context.Context, client workerv1connect.WorkerServiceClient, workerID, stateDir, access string, approval workerRegistrationApproval) error {
	capabilities := runtime.VerifiedSandboxCapabilities()
	var attestation *workerv1.SandboxAttestation
	if approval.ApprovedManifestDigest != "" {
		passedProbes := []string{"child_escape_denied", "filesystem_read_denied", "filesystem_write_denied", "network_denied"}
		signature, err := signSandboxAttestation(stateDir, approval.SandboxChallengeID, approval.ApprovedManifestDigest, passedProbes)
		if err != nil {
			return err
		}
		attestation = &workerv1.SandboxAttestation{ChallengeId: approval.SandboxChallengeID,
			ManifestDigest: approval.ApprovedManifestDigest, PassedProbes: passedProbes, Signature: signature}
	}
	return retryAgentdCall(ctx, func(ctx context.Context) error {
		req := connect.NewRequest(&workerv1.RegisterWorkerRequest{
			WorkerId: workerID, WorkerKind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR,
			Version: version, OperatingSystem: goruntime.GOOS, Architecture: goruntime.GOARCH,
			SandboxCapabilities: capabilities, MaxConcurrency: 1, LogicalCpuCapacity: int32(goruntime.NumCPU()),
			ApprovedManifestDigest: approval.ApprovedManifestDigest, SandboxAttestation: attestation,
		})
		req.Header().Set("Authorization", "Bearer "+access)
		_, err := client.RegisterWorker(ctx, req)
		return err
	})
}

func signSandboxAttestation(stateDir, challengeID, manifestDigest string, passedProbes []string) (string, error) {
	raw, err := os.ReadFile(workerClientKeyPath(stateDir))
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		return "", errors.New("worker client private key is invalid")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", err
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return "", errors.New("worker sandbox attestation requires an ed25519 identity key")
	}
	slices.Sort(passedProbes)
	payload := []byte(challengeID + "\x00" + manifestDigest + "\x00" + strings.Join(passedProbes, "\n"))
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, payload)), nil
}

func retryAgentdCall(ctx context.Context, call func(context.Context) error) error {
	wait := 200 * time.Millisecond
	const maxWait = 30 * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := call(ctx)
		if err == nil {
			return nil
		}
		switch connect.CodeOf(err) {
		case connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeInvalidArgument,
			connect.CodeAlreadyExists, connect.CodeFailedPrecondition:
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
		if wait > maxWait {
			wait = maxWait
		}
	}
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
