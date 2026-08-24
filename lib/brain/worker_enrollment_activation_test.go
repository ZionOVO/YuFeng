package brain

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"yufeng/lib/kernel"
	workerv1 "yufeng/proto/gen/workerv1"
)

func TestApprovedWorkerEnrollmentReturnsOnlyEncryptedActivationPackage(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	if err := writeAdminSystemGrant(ctx, st.Pool(), h.adminID, h.local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, h.local); err != nil {
		t.Fatal(err)
	}

	workerID := "worker-enrollment-" + newTestSuffix()
	certificateRequest, workerPublicKey := makeWorkerEnrollmentCertificateRequest(t, workerID)
	activationKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	activationPublicKey := base64.RawURLEncoding.EncodeToString(activationKey.PublicKey().Bytes())
	authorityDirectory := t.TempDir()
	issuer, err := kernel.LoadOrCreateWorkloadCertificateAuthority(
		filepath.Join(authorityDirectory, "workload-ca.key"),
		filepath.Join(authorityDirectory, "workload-ca.crt"),
	)
	if err != nil {
		t.Fatal(err)
	}
	server := NewWorkerServer(st.Pool(), mustKey(t), false, issuer)

	requested, err := server.RequestWorkerEnrollment(ctx, connect.NewRequest(&workerv1.RequestWorkerEnrollmentRequest{
		WorkerId: workerID, WorkerKind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR,
		WorkerPublicKey: workerPublicKey, Hostname: "worker-host", OperatingSystem: "linux", Architecture: "amd64",
		SandboxCapabilities: []string{"landlock", "seccomp", "resource_limits"},
		CertificateRequest:  certificateRequest, ActivationPublicKey: activationPublicKey,
		Version: "test-v1", MaxConcurrency: 1, MemoryCapacityBytes: 1024, LogicalCpuCapacity: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if requested.Msg.GetEnrollmentId() == "" || requested.Msg.GetState() != "pending" {
		t.Fatalf("unexpected enrollment receipt: %v", requested.Msg)
	}

	decisionRequest := bearerReq(h.adminTok, &workerv1.DecideWorkerEnrollmentRequest{
		EnrollmentId: requested.Msg.GetEnrollmentId(), Approved: true,
		Bindings: []string{"asset:" + h.local}, MaxConcurrency: 1,
	})
	decisionRequest.Header().Set("Idempotency-Key", "worker-enrollment-approve-"+newTestSuffix())
	decided, err := server.DecideWorkerEnrollment(ctx, decisionRequest)
	if err != nil {
		t.Fatal(err)
	}
	if decided.Msg.GetBootstrapToken() != "" || decided.Msg.GetClientCertificate() != "" || decided.Msg.GetCertificateChain() != "" {
		t.Fatal("approval response exposed plaintext activation credentials")
	}
	if decided.Msg.GetState() != "approved" || decided.Msg.GetActivationBundleRef() == "" ||
		decided.Msg.GetApprovedManifestDigest() == "" || decided.Msg.GetCertificateExpiresAt() == nil {
		t.Fatalf("approval response omitted encrypted activation metadata: %v", decided.Msg)
	}

	result, err := server.GetWorkerEnrollmentResult(ctx, connect.NewRequest(&workerv1.GetWorkerEnrollmentResultRequest{
		EnrollmentId: requested.Msg.GetEnrollmentId(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Msg.GetState() != "approved" || result.Msg.GetActivationBundleRef() != decided.Msg.GetActivationBundleRef() ||
		result.Msg.GetApprovedManifestDigest() != decided.Msg.GetApprovedManifestDigest() ||
		len(result.Msg.GetEncryptedActivationBundle()) == 0 || result.Msg.GetExpiresAt() == nil {
		t.Fatalf("unexpected encrypted activation result: %v", result.Msg)
	}
	if bytes.Contains(result.Msg.GetEncryptedActivationBundle(), []byte("BEGIN CERTIFICATE")) {
		t.Fatal("encrypted activation result contains a plaintext certificate")
	}
	plaintext := decryptWorkerActivationForTest(
		t, activationKey, requested.Msg.GetEnrollmentId(), result.Msg.GetEncryptedActivationBundle(),
	)
	var activation workerActivationPackage
	if err := json.Unmarshal(plaintext, &activation); err != nil {
		t.Fatal(err)
	}
	if activation.EnrollmentID != requested.Msg.GetEnrollmentId() ||
		activation.ActivationBundleRef != decided.Msg.GetActivationBundleRef() ||
		activation.ApprovedManifestDigest != decided.Msg.GetApprovedManifestDigest() ||
		activation.BootstrapToken == "" || activation.ClientCertificate == "" || activation.CertificateChain == "" {
		t.Fatalf("decrypted activation package is incomplete: %+v", activation)
	}

	certificateHash, err := kernel.WorkloadCertificateSHA256(activation.ClientCertificate)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := server.RegisterWorkerIdentity(workerCertContext(ctx, certificateHash),
		connect.NewRequest(&workerv1.RegisterWorkerIdentityRequest{
			WorkerId: workerID, WorkerKind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR,
			BootstrapToken: activation.BootstrapToken, WorkerPublicKey: workerPublicKey,
		}))
	if err != nil {
		t.Fatal(err)
	}
	otherWorkerID := "worker-enrollment-other-" + newTestSuffix()
	otherCertificateHash := strings.Repeat("d", 64)
	otherPublicKey := "ed25519:" + otherWorkerID
	otherBootstrap, err := server.CreateWorkerBootstrap(ctx, bearerReq(h.adminTok, &workerv1.CreateWorkerBootstrapRequest{
		WorkerId: otherWorkerID, WorkerKind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR,
		WorkerPublicKey: otherPublicKey, ClientCertSha256: otherCertificateHash,
		Bindings: []string{"asset:" + h.local},
	}))
	if err != nil {
		t.Fatal(err)
	}
	otherIdentity, err := server.RegisterWorkerIdentity(workerCertContext(ctx, otherCertificateHash),
		connect.NewRequest(&workerv1.RegisterWorkerIdentityRequest{
			WorkerId: otherWorkerID, WorkerKind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR,
			BootstrapToken: otherBootstrap.Msg.GetBootstrapToken(), WorkerPublicKey: otherPublicKey,
		}))
	if err != nil {
		t.Fatal(err)
	}
	wrongSubject := connect.NewRequest(&workerv1.AcknowledgeWorkerActivationRequest{
		EnrollmentId: requested.Msg.GetEnrollmentId(), ActivationBundleRef: decided.Msg.GetActivationBundleRef(),
	})
	wrongSubject.Header().Set("Authorization", "Bearer "+otherIdentity.Msg.GetAccessToken())
	if _, err := server.AcknowledgeWorkerActivation(workerCertContext(ctx, otherCertificateHash), wrongSubject); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("wrong worker activation acknowledgement error = %v, want permission_denied", err)
	}
	badAcknowledge := connect.NewRequest(&workerv1.AcknowledgeWorkerActivationRequest{
		EnrollmentId: requested.Msg.GetEnrollmentId(), ActivationBundleRef: "wrong-bundle",
	})
	badAcknowledge.Header().Set("Authorization", "Bearer "+identity.Msg.GetAccessToken())
	if _, err := server.AcknowledgeWorkerActivation(workerCertContext(ctx, certificateHash), badAcknowledge); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("wrong activation reference error = %v, want failed_precondition", err)
	}
	acknowledge := connect.NewRequest(&workerv1.AcknowledgeWorkerActivationRequest{
		EnrollmentId: requested.Msg.GetEnrollmentId(), ActivationBundleRef: decided.Msg.GetActivationBundleRef(),
	})
	acknowledge.Header().Set("Authorization", "Bearer "+identity.Msg.GetAccessToken())
	if _, err := server.AcknowledgeWorkerActivation(workerCertContext(ctx, certificateHash), acknowledge); err != nil {
		t.Fatal(err)
	}
	repeatedAcknowledge := connect.NewRequest(&workerv1.AcknowledgeWorkerActivationRequest{
		EnrollmentId: requested.Msg.GetEnrollmentId(), ActivationBundleRef: decided.Msg.GetActivationBundleRef(),
	})
	repeatedAcknowledge.Header().Set("Authorization", "Bearer "+identity.Msg.GetAccessToken())
	if _, err := server.AcknowledgeWorkerActivation(workerCertContext(ctx, certificateHash), repeatedAcknowledge); err != nil {
		t.Fatalf("repeated activation acknowledgement must be idempotent: %v", err)
	}
	var acknowledgeAuditCount int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_entries
		WHERE action='worker_activation.acknowledge' AND object_type='worker_enrollment' AND object_id=$1`,
		requested.Msg.GetEnrollmentId()).Scan(&acknowledgeAuditCount); err != nil {
		t.Fatal(err)
	}
	if acknowledgeAuditCount != 1 {
		t.Fatalf("activation acknowledgement audit entries = %d, want 1", acknowledgeAuditCount)
	}
	consumed, err := server.GetWorkerEnrollmentResult(ctx, connect.NewRequest(&workerv1.GetWorkerEnrollmentResultRequest{
		EnrollmentId: requested.Msg.GetEnrollmentId(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(consumed.Msg.GetEncryptedActivationBundle()) != 0 {
		t.Fatal("acknowledged activation ciphertext remains retrievable")
	}
}

func makeWorkerEnrollmentCertificateRequest(t *testing.T, workerID string) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	requestDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: workerID},
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: requestDER})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))
}
