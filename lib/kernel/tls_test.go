package kernel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidateProductionTLS(t *testing.T) {
	if err := ValidateProductionTLS(false, "", ""); err == nil {
		t.Fatal("production without cert must fail closed")
	}
	if err := ValidateProductionTLS(false, "cert.pem", ""); err == nil {
		t.Fatal("missing key must fail")
	}
	if err := ValidateProductionTLS(true, "", ""); err != nil {
		t.Fatalf("dev-insecure should allow empty cert: %v", err)
	}
	if err := ValidateProductionTLS(false, "cert.pem", "key.pem"); err != nil {
		t.Fatalf("cert+key should pass: %v", err)
	}
}

func TestValidateSignerSource(t *testing.T) {
	if err := ValidateSignerSource(true, "/tmp/dev.key"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateProductionAgentBootstrap(t *testing.T) {
	if err := ValidateProductionAgentBootstrap(false, "compose-agent-bootstrap", ""); err == nil {
		t.Fatal("unbound production token must fail")
	}
	if err := ValidateProductionAgentBootstrap(false, "compose-agent-bootstrap", "jarvis-1"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProductionAgentBootstrap(true, DefaultAgentBootstrapToken, ""); err != nil {
		t.Fatal(err)
	}
}

func TestValidateProductionSecrets(t *testing.T) {
	if err := ValidateProductionSecrets(false, "admin", "x", "y"); err == nil {
		t.Fatal("default password must fail")
	}
	if err := ValidateProductionSecrets(false, "SafePass#1", DefaultAgentBootstrapToken, "unit-boot"); err == nil {
		t.Fatal("default agent token must fail")
	}
	if err := ValidateProductionSecrets(false, "SafePass#1", "agent-boot", DefaultUnitBootstrapToken); err == nil {
		t.Fatal("default unit token must fail")
	}
	if err := ValidateProductionSecrets(false, "SafePass#1", "agent-boot", "unit-boot"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProductionSecrets(true, "admin", DefaultAgentBootstrapToken, DefaultUnitBootstrapToken); err != nil {
		t.Fatal(err)
	}
}

func TestValidateProductionSigner(t *testing.T) {
	if err := ValidateProductionSigner(false, "/keys/dev.key.hex", ""); err == nil {
		t.Fatal("production file key must fail")
	}
	if err := ValidateProductionSigner(false, "", ""); err == nil {
		t.Fatal("production without socket must fail")
	}
	if err := ValidateProductionSigner(false, "", "/run/yufeng/signer.sock"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProductionSigner(true, "/keys/dev.key.hex", ""); err != nil {
		t.Fatal(err)
	}
}

func TestValidateProductionMTLS(t *testing.T) {
	if err := ValidateProductionMTLS(false, ""); err == nil {
		t.Fatal("production without client ca must fail")
	}
	if err := ValidateProductionMTLS(true, ""); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProductionMTLS(false, "ca.crt"); err != nil {
		t.Fatal(err)
	}
}

func TestMTLSClientRequiredOnRealServer(t *testing.T) {
	dir := t.TempDir()
	bundle, err := GenerateTLSBundle([]string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteTLSBundle(dir, bundle); err != nil {
		t.Fatal(err)
	}
	srvTLS, err := ServerMTLSConfig(filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(bundle.ServerCert, bundle.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	srvTLS.Certificates = []tls.Certificate{cert}
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	ts.TLS = srvTLS
	ts.StartTLS()
	t.Cleanup(ts.Close)

	bare := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	if _, err := bare.Get(ts.URL + "/"); err == nil {
		t.Fatal("client without certificate must be rejected")
	}
	okClient, err := HTTPClient(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := okClient.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // 只读测试响应在断言完成后尽力清理。
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mtls client status=%d", resp.StatusCode)
	}
}

func TestOptionalClientCertAllowsBareAndVerifiesPresented(t *testing.T) {
	dir := t.TempDir()
	bundle, err := GenerateTLSBundle([]string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteTLSBundle(dir, bundle); err != nil {
		t.Fatal(err)
	}
	srvTLS, err := ServerOptionalClientCertConfig(filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(bundle.ServerCert, bundle.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	srvTLS.Certificates = []tls.Certificate{cert}
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	ts.TLS = srvTLS
	ts.StartTLS()
	t.Cleanup(ts.Close)

	bare := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	bareResp, err := bare.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("browser / curl -k must complete handshake: %v", err)
	}
	defer bareResp.Body.Close() //nolint:errcheck // 只读测试响应在断言完成后尽力清理。
	if bareResp.StatusCode != http.StatusOK {
		t.Fatalf("bare client status=%d", bareResp.StatusCode)
	}

	okClient, err := HTTPClient(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	okResp, err := okClient.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer okResp.Body.Close() //nolint:errcheck // 只读测试响应在断言完成后尽力清理。
	if okResp.StatusCode != http.StatusOK {
		t.Fatalf("presented client cert status=%d", okResp.StatusCode)
	}

	other, err := GenerateTLSBundle([]string{"other"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	otherDir := t.TempDir()
	if err := WriteTLSBundle(otherDir, other); err != nil {
		t.Fatal(err)
	}
	badClient, err := HTTPClient(filepath.Join(dir, "ca.crt"), filepath.Join(otherDir, "client.crt"), filepath.Join(otherDir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badClient.Get(ts.URL + "/"); err == nil {
		t.Fatal("client certificate signed by another ca must be rejected")
	}
}

func TestExistingTLSBundleAcceptsWrittenAndRejectsForeign(t *testing.T) {
	dir := t.TempDir()
	bundle, err := GenerateTLSBundle([]string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteTLSBundle(dir, bundle); err != nil {
		t.Fatal(err)
	}
	got, err := ExistingTLSBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.CACert) != string(bundle.CACert) {
		t.Fatal("existing bundle ca mismatch")
	}
	other, err := GenerateTLSBundle([]string{"other"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server.crt"), other.ServerCert, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExistingTLSBundle(dir); err == nil {
		t.Fatal("server cert from another ca must be rejected")
	}
}

func TestHTTPClientTimeoutCoversMaximumControlPlaneLongPoll(t *testing.T) {
	client, err := HTTPClient("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if client.Timeout <= SessionLongPollMax {
		t.Fatalf("http client timeout %s must exceed maximum long poll %s", client.Timeout, SessionLongPollMax)
	}
}

func TestHTTPClientReloadsRotatedClientCertificate(t *testing.T) {
	dir := t.TempDir()
	bundle, err := GenerateTLSBundle([]string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteTLSBundle(dir, bundle); err != nil {
		t.Fatal(err)
	}

	serverTLS, err := ServerMTLSConfig(filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	serverCertificate, err := tls.X509KeyPair(bundle.ServerCert, bundle.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	serverTLS.Certificates = []tls.Certificate{serverCertificate}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.TLS == nil || len(request.TLS.PeerCertificates) != 1 {
			http.Error(w, "client certificate required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-Client-Certificate-Serial", request.TLS.PeerCertificates[0].SerialNumber.String())
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = serverTLS
	server.StartTLS()
	t.Cleanup(server.Close)

	client, err := HTTPClient(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	requestSerial := func() string {
		t.Helper()
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close() //nolint:errcheck // 测试响应无正文，断言后尽力关闭。
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("status=%d", response.StatusCode)
		}
		return response.Header.Get("X-Client-Certificate-Serial")
	}
	if serial := requestSerial(); serial != "3" {
		t.Fatalf("initial client certificate serial=%q", serial)
	}

	caCertificate, err := parseCertPEM(bundle.CACert)
	if err != nil {
		t.Fatal(err)
	}
	caKeyBlock, _ := pem.Decode(bundle.CAKey)
	if caKeyBlock == nil {
		t.Fatal("ca key pem is invalid")
	}
	caKey, err := x509.ParseECPrivateKey(caKeyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	rotatedKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rotatedTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(99),
		Subject:      pkix.Name{CommonName: "rotated-yufeng-client"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	rotatedDER, err := x509.CreateCertificate(rand.Reader, rotatedTemplate, caCertificate, &rotatedKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	rotatedKeyPEM, err := ecdsaPEM(rotatedKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "client.crt"), certPEM(rotatedDER), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "client.key"), rotatedKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type=%T", client.Transport)
	}
	transport.CloseIdleConnections()

	if serial := requestSerial(); serial != "99" {
		t.Fatalf("rotated client certificate serial=%q", serial)
	}
}
