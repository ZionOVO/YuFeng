package kernel

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkloadCertificateRequestBindsWorkerAndPublicKey(t *testing.T) {
	request, publicKey := makeWorkloadCertificateRequest(t, "worker-a")
	fingerprint, err := ValidateWorkloadCertificateRequest("worker-a", request, publicKey)
	if err != nil {
		t.Fatalf("validate matching request: %v", err)
	}
	if !strings.HasPrefix(fingerprint, "sha256:") || len(fingerprint) != len("sha256:")+64 {
		t.Fatalf("unexpected fingerprint %q", fingerprint)
	}

	_, otherPublicKey := makeWorkloadCertificateRequest(t, "worker-a")
	if _, err := ValidateWorkloadCertificateRequest("worker-a", request, otherPublicKey); err == nil {
		t.Fatal("mismatched public key must be rejected")
	}
	if _, err := ValidateWorkloadCertificateRequest("worker-b", request, publicKey); err == nil {
		t.Fatal("mismatched worker subject must be rejected")
	}
}

func TestWorkloadCertificateAuthorityIssuesBoundClientCertificate(t *testing.T) {
	dir := t.TempDir()
	authority, err := LoadOrCreateWorkloadCertificateAuthority(
		filepath.Join(dir, "workload-ca.key"), filepath.Join(dir, "workload-ca.crt"),
	)
	if err != nil {
		t.Fatalf("create workload certificate authority: %v", err)
	}
	request, _ := makeWorkloadCertificateRequest(t, "worker-a")
	before := time.Now().UTC()
	issued, err := authority.Issue("worker-a", request, 24*time.Hour)
	if err != nil {
		t.Fatalf("issue workload certificate: %v", err)
	}
	if issued.ExpiresAt.Before(before.Add(23*time.Hour+59*time.Minute)) || issued.ExpiresAt.After(before.Add(24*time.Hour+time.Minute)) {
		t.Fatalf("unexpected certificate expiry %s", issued.ExpiresAt)
	}
	block, _ := pem.Decode([]byte(issued.Certificate))
	if block == nil {
		t.Fatal("issued certificate is not PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse issued certificate: %v", err)
	}
	if certificate.Subject.CommonName != "worker-a" {
		t.Fatalf("certificate subject = %q", certificate.Subject.CommonName)
	}
	if len(certificate.ExtKeyUsage) != 1 || certificate.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("unexpected extended key usage %v", certificate.ExtKeyUsage)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(issued.Chain)) {
		t.Fatal("certificate chain is invalid")
	}
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("verify issued certificate: %v", err)
	}
	if _, err := authority.Issue("worker-a", request, 24*time.Hour+time.Second); err == nil {
		t.Fatal("certificate longer than 24 hours must be rejected")
	}
	if _, err := WorkloadCertificateSHA256(issued.Certificate); err != nil {
		t.Fatalf("hash issued certificate: %v", err)
	}
}

func TestWorkloadCertificateAuthorityRefusesPartialMaterial(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "workload-ca.key")
	certificatePath := filepath.Join(dir, "workload-ca.crt")
	if err := writeExclusiveFile(keyPath, []byte("incomplete"), 0o600); err != nil {
		t.Fatalf("write partial authority: %v", err)
	}
	if _, err := LoadOrCreateWorkloadCertificateAuthority(keyPath, certificatePath); err == nil {
		t.Fatal("partial certificate authority material must fail closed")
	}
}

func TestWorkloadCertificateAuthorityRejectsMismatchedKeyAndCertificate(t *testing.T) {
	firstDirectory := t.TempDir()
	secondDirectory := t.TempDir()
	firstKey := filepath.Join(firstDirectory, "workload-ca.key")
	firstCertificate := filepath.Join(firstDirectory, "workload-ca.crt")
	secondKey := filepath.Join(secondDirectory, "workload-ca.key")
	secondCertificate := filepath.Join(secondDirectory, "workload-ca.crt")
	if _, err := LoadOrCreateWorkloadCertificateAuthority(firstKey, firstCertificate); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateWorkloadCertificateAuthority(secondKey, secondCertificate); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateWorkloadCertificateAuthority(firstKey, secondCertificate); err == nil {
		t.Fatal("workload certificate authority must reject a certificate from another private key")
	}
}

func TestSocketWorkloadCertificateIssuerRoundTrip(t *testing.T) {
	directory := t.TempDir()
	authority, err := LoadOrCreateWorkloadCertificateAuthority(
		filepath.Join(directory, "workload-ca.key"), filepath.Join(directory, "workload-ca.crt"),
	)
	if err != nil {
		t.Fatal(err)
	}
	client, _ := newTestWorkloadCertificateIssuer(t, authority)
	request, _ := makeWorkloadCertificateRequest(t, "worker-a")
	issued, err := client.Issue("worker-a", request, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(issued.Certificate))
	if block == nil {
		t.Fatal("socket issuer returned an invalid certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if certificate.Subject.CommonName != "worker-a" || issued.Chain != authority.certificatePEM {
		t.Fatalf("issued certificate subject=%q chain_match=%v", certificate.Subject.CommonName, issued.Chain == authority.certificatePEM)
	}
	if _, err := client.Issue("worker-b", request, time.Hour); err == nil {
		t.Fatal("socket issuer must reject a certificate request bound to another worker")
	}
	if _, err := NewSocketWorkloadCertificateIssuer(""); err == nil {
		t.Fatal("empty issuer socket must be rejected")
	}
}

func TestSocketWorkloadCertificateIssuerRejectsCertificateForAnotherWorker(t *testing.T) {
	directory := t.TempDir()
	authority, err := LoadOrCreateWorkloadCertificateAuthority(
		filepath.Join(directory, "workload-ca.key"), filepath.Join(directory, "workload-ca.crt"),
	)
	if err != nil {
		t.Fatal(err)
	}
	otherRequest, _ := makeWorkloadCertificateRequest(t, "worker-b")
	otherCertificate, err := authority.Issue("worker-b", otherRequest, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	issuer := workloadCertificateIssuerFunc(func(string, string, time.Duration) (WorkloadCertificate, error) {
		return otherCertificate, nil
	})
	client, _ := newTestWorkloadCertificateIssuer(t, issuer)
	request, _ := makeWorkloadCertificateRequest(t, "worker-a")
	if _, err := client.Issue("worker-a", request, time.Hour); err == nil {
		t.Fatal("socket issuer client must reject a certificate bound to another worker")
	}
}

func TestWorkloadCertificateIssuerRejectsTrailingRequestData(t *testing.T) {
	directory := t.TempDir()
	authority, err := LoadOrCreateWorkloadCertificateAuthority(
		filepath.Join(directory, "workload-ca.key"), filepath.Join(directory, "workload-ca.crt"),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, address := newTestWorkloadCertificateIssuer(t, authority)
	request, _ := makeWorkloadCertificateRequest(t, "worker-a")
	raw, err := json.Marshal(workloadIssueRequest{WorkerID: "worker-a", CertificateRequest: request, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.Dial("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close() //nolint:errcheck // 测试请求结束后尽力清理。
	if _, err := connection.Write(append(raw, []byte(`{}`)...)); err != nil {
		t.Fatal(err)
	}
	if err := connection.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	var response workloadIssueResponse
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error == "" || response.Certificate.Certificate != "" {
		t.Fatalf("trailing issuer request was accepted: %+v", response)
	}
}

func TestWorkloadCertificateIssuerRejectsDataBeyondMessageLimit(t *testing.T) {
	called := make(chan struct{}, 1)
	issuer := workloadCertificateIssuerFunc(func(string, string, time.Duration) (WorkloadCertificate, error) {
		called <- struct{}{}
		return WorkloadCertificate{Certificate: "unexpected"}, nil
	})
	_, address := newTestWorkloadCertificateIssuer(t, issuer)
	request, _ := makeWorkloadCertificateRequest(t, "worker-a")
	raw, err := json.Marshal(workloadIssueRequest{WorkerID: "worker-a", CertificateRequest: request, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= workloadIssuerMessageLimit {
		t.Fatalf("test request unexpectedly exceeds message limit: %d", len(raw))
	}
	connection, err := net.Dial("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close() //nolint:errcheck // 测试请求结束后尽力清理。
	payload := append(raw, bytes.Repeat([]byte(" "), workloadIssuerMessageLimit-len(raw))...)
	payload = append(payload, []byte(`{}`)...)
	_, _ = connection.Write(payload)
	_ = connection.(*net.UnixConn).CloseWrite()
	var response workloadIssueResponse
	decodeErr := json.NewDecoder(connection).Decode(&response)
	select {
	case <-called:
		t.Fatal("oversized issuer request must not reach the certificate authority")
	default:
	}
	// Windows 可以在服务端拒绝仍有未读数据的连接时返回重置；错误响应和传输关闭都表示拒绝。
	if decodeErr == nil && (response.Error == "" || response.Certificate.Certificate != "") {
		t.Fatalf("request beyond issuer message limit was accepted: %+v", response)
	}
}

type workloadCertificateIssuerFunc func(string, string, time.Duration) (WorkloadCertificate, error)

func (f workloadCertificateIssuerFunc) Issue(workerID, request string, ttl time.Duration) (WorkloadCertificate, error) {
	return f(workerID, request, ttl)
}

func TestSocketWorkloadCertificateIssuerRejectsTrailingResponseData(t *testing.T) {
	directory, err := os.MkdirTemp("", "yufeng-workload-response-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	address := filepath.Join(directory, "issuer.sock")
	listener, err := net.Listen("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close() //nolint:errcheck // 测试服务端写完畸形响应后尽力释放连接。
		var request workloadIssueRequest
		if decodeErr := json.NewDecoder(connection).Decode(&request); decodeErr != nil {
			return
		}
		response, marshalErr := json.Marshal(workloadIssueResponse{Certificate: WorkloadCertificate{Certificate: "unexpected"}})
		if marshalErr != nil {
			return
		}
		_, _ = connection.Write(append(response, []byte(`{}`)...))
	}()

	client, err := NewSocketWorkloadCertificateIssuer(address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Issue("worker-a", "request", time.Hour); err == nil {
		t.Fatal("issuer response with trailing data must be rejected")
	}
}

func makeWorkloadCertificateRequest(t *testing.T, workerID string) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate workload key: %v", err)
	}
	requestDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: workerID},
	}, privateKey)
	if err != nil {
		t.Fatalf("create workload certificate request: %v", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatalf("marshal workload public key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: requestDER})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))
}

func newTestWorkloadCertificateIssuer(t *testing.T, issuer WorkloadCertificateIssuer) (*SocketWorkloadCertificateIssuer, string) {
	t.Helper()
	directory, err := os.MkdirTemp("", "yufeng-workload-issuer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	address := filepath.Join(directory, "issuer.sock")
	listener, err := net.Listen("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() { _ = ServeWorkloadCertificateIssuer(listener, issuer) }()
	client, err := NewSocketWorkloadCertificateIssuer(address)
	if err != nil {
		t.Fatal(err)
	}
	return client, address
}
