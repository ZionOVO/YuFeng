package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"yufeng/lib/kernel"
)

func TestLoadOrCreateEnrollmentMaterialPersistsMatchingIdentity(t *testing.T) {
	dir := t.TempDir()
	first, err := loadOrCreateEnrollmentMaterial(dir, "worker-a")
	if err != nil {
		t.Fatalf("create enrollment material: %v", err)
	}
	second, err := loadOrCreateEnrollmentMaterial(dir, "worker-a")
	if err != nil {
		t.Fatalf("reload enrollment material: %v", err)
	}
	if first.PublicKey != second.PublicKey || first.CertificateRequest != second.CertificateRequest {
		t.Fatal("enrollment identity changed after reload")
	}
	if _, err := kernel.ValidateWorkloadCertificateRequest("worker-a", first.CertificateRequest, first.PublicKey); err != nil {
		t.Fatalf("validate persisted enrollment identity: %v", err)
	}
	for _, path := range []string{workerClientKeyPath(dir), workerPublicKeyPath(dir), filepath.Join(dir, "client.csr")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat enrollment material: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("enrollment material mode = %o", info.Mode().Perm())
		}
	}
}

func TestLoadOrCreateEnrollmentMaterialRefusesPartialIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(workerClientKeyPath(dir), []byte("partial"), 0o600); err != nil {
		t.Fatalf("write partial identity: %v", err)
	}
	if _, err := loadOrCreateEnrollmentMaterial(dir, "worker-a"); err == nil {
		t.Fatal("partial enrollment identity must fail closed")
	}
}

func TestSeedWorkerTLSMaterialCopiesOnceIntoPrivateState(t *testing.T) {
	source := t.TempDir()
	state := t.TempDir()
	bundle, err := kernel.GenerateTLSBundle([]string{"brain"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("generate tls seed: %v", err)
	}
	certificateSource := filepath.Join(source, "client.crt")
	keySource := filepath.Join(source, "client.key")
	if err := os.WriteFile(certificateSource, bundle.ClientCert, 0o600); err != nil {
		t.Fatalf("write certificate seed: %v", err)
	}
	if err := os.WriteFile(keySource, bundle.ClientKey, 0o600); err != nil {
		t.Fatalf("write key seed: %v", err)
	}
	if err := seedWorkerTLSMaterial(state, certificateSource, keySource); err != nil {
		t.Fatalf("seed private tls material: %v", err)
	}
	persisted, err := os.ReadFile(workerClientCertificatePath(state))
	if err != nil {
		t.Fatalf("read private certificate: %v", err)
	}
	if !bytes.Equal(persisted, bundle.ClientCert) {
		t.Fatal("private certificate differs from initial seed")
	}
	if err := os.WriteFile(certificateSource, []byte("invalid replacement"), 0o600); err != nil {
		t.Fatalf("replace external seed: %v", err)
	}
	if err := seedWorkerTLSMaterial(state, certificateSource, keySource); err != nil {
		t.Fatalf("existing private tls material must not reread rotated seed: %v", err)
	}
}

func TestPrepareWorkerActivationPackageBindsLocalIdentityAndConsumesOnce(t *testing.T) {
	state := t.TempDir()
	material, err := loadOrCreateEnrollmentMaterial(state, "worker-a")
	if err != nil {
		t.Fatalf("create enrollment material: %v", err)
	}
	certificateDirectory := t.TempDir()
	issuer, err := kernel.LoadOrCreateWorkloadCertificateAuthority(
		filepath.Join(certificateDirectory, "workload-ca.key"), filepath.Join(certificateDirectory, "workload-ca.crt"))
	if err != nil {
		t.Fatalf("create workload certificate authority: %v", err)
	}
	certificate, err := issuer.Issue("worker-a", material.CertificateRequest, 24*time.Hour)
	if err != nil {
		t.Fatalf("issue workload certificate: %v", err)
	}
	activation := workerActivationPackage{EnrollmentID: "enroll-a", ActivationBundleRef: "bundle-a",
		ApprovedManifestDigest: "manifest-a", SandboxChallengeID: "challenge-a", BootstrapToken: "bootstrap-once",
		ClientCertificate: certificate.Certificate, CertificateChain: certificate.Chain,
		CertificateExpiresAt: certificate.ExpiresAt.Format(time.RFC3339Nano)}
	raw, err := json.Marshal(activation)
	if err != nil {
		t.Fatalf("encode activation package: %v", err)
	}
	path := filepath.Join(t.TempDir(), "activation.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write activation package: %v", err)
	}
	bootstrap, err := prepareWorkerActivationPackage(path, state, "worker-a")
	if err != nil {
		t.Fatalf("prepare activation package: %v", err)
	}
	if bootstrap != "bootstrap-once" {
		t.Fatalf("bootstrap = %q", bootstrap)
	}
	if _, err := tls.LoadX509KeyPair(workerClientCertificatePath(state), workerClientKeyPath(state)); err != nil {
		t.Fatalf("load installed client identity: %v", err)
	}
	if err := saveWorkerRefresh(workerRefreshFile(state), "worker-a", "refresh-after-activation"); err != nil {
		t.Fatalf("persist refresh after activation: %v", err)
	}
	if err := consumeWorkerActivationState(path, state); err != nil {
		t.Fatalf("consume activation state: %v", err)
	}
	for _, consumedPath := range []string{path, workerActivationPrivateKeyPath(state), workerActivationPublicKeyPath(state)} {
		if _, err := os.Stat(consumedPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("activation state remains after consumption at %s: %v", consumedPath, err)
		}
	}
	if _, err := os.Stat(workerClientKeyPath(state)); err != nil {
		t.Fatalf("permanent worker client key was removed: %v", err)
	}
	if bootstrap, err := prepareWorkerActivationPackage(path, state, "worker-a"); err != nil || bootstrap != "" {
		t.Fatalf("restart after activation bootstrap=%q err=%v", bootstrap, err)
	}
	if err := consumeWorkerActivationState(path, state); err != nil {
		t.Fatalf("consuming already removed activation state must be idempotent: %v", err)
	}
}

func TestPrepareWorkerActivationPackageRejectsWrongWorkerAndOpenPermissions(t *testing.T) {
	state := t.TempDir()
	material, err := loadOrCreateEnrollmentMaterial(state, "worker-a")
	if err != nil {
		t.Fatalf("create enrollment material: %v", err)
	}
	certificateDirectory := t.TempDir()
	issuer, err := kernel.LoadOrCreateWorkloadCertificateAuthority(
		filepath.Join(certificateDirectory, "workload-ca.key"), filepath.Join(certificateDirectory, "workload-ca.crt"))
	if err != nil {
		t.Fatalf("create workload certificate authority: %v", err)
	}
	certificate, err := issuer.Issue("worker-a", material.CertificateRequest, time.Hour)
	if err != nil {
		t.Fatalf("issue workload certificate: %v", err)
	}
	raw, err := json.Marshal(workerActivationPackage{EnrollmentID: "enroll-a", ActivationBundleRef: "bundle-a",
		ApprovedManifestDigest: "manifest-a", SandboxChallengeID: "challenge-a", BootstrapToken: "bootstrap-once",
		ClientCertificate: certificate.Certificate, CertificateChain: certificate.Chain,
		CertificateExpiresAt: certificate.ExpiresAt.Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatalf("encode activation package: %v", err)
	}
	path := filepath.Join(t.TempDir(), "activation.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write activation package: %v", err)
	}
	if _, err := prepareWorkerActivationPackage(path, state, "worker-b"); err == nil {
		t.Fatal("activation package for another worker must fail closed")
	}
	if goruntime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("open activation package permissions: %v", err)
		}
		if _, err := prepareWorkerActivationPackage(path, state, "worker-a"); err == nil {
			t.Fatal("group-readable activation package must fail closed")
		}
	}
}

func TestResolveWorkerActivationStateRecoversEveryStartupWindow(t *testing.T) {
	tests := []struct {
		name         string
		activation   bool
		refresh      string
		corrupt      bool
		wantPackage  bool
		wantRetrieve bool
		wantError    bool
	}{
		{name: "new enrollment retrieves encrypted package", wantRetrieve: true},
		{name: "downloaded package is resumed", activation: true, wantPackage: true},
		{name: "persisted refresh starts normally", refresh: "refresh-active"},
		{name: "package awaiting acknowledgement wins over refresh", activation: true, refresh: "refresh-active", wantPackage: true},
		{name: "corrupt refresh fails closed", corrupt: true, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stateDir := t.TempDir()
			if test.activation {
				if err := os.WriteFile(filepath.Join(stateDir, "activation.json"), []byte(`{"pending":true}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.refresh != "" {
				if err := saveWorkerRefresh(workerRefreshFile(stateDir), "worker-a", test.refresh); err != nil {
					t.Fatal(err)
				}
			}
			if test.corrupt {
				if err := os.WriteFile(workerRefreshFile(stateDir), []byte("not-json"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			packagePath, retrieve, err := resolveWorkerActivationState(stateDir, "worker-a")
			if (err != nil) != test.wantError {
				t.Fatalf("resolve activation state error=%v want error=%v", err, test.wantError)
			}
			if err != nil {
				if packagePath != "" || retrieve {
					t.Fatalf("failed activation state fell back to package=%q retrieve=%v", packagePath, retrieve)
				}
				return
			}
			if (packagePath != "") != test.wantPackage || retrieve != test.wantRetrieve {
				t.Fatalf("package=%q retrieve=%v", packagePath, retrieve)
			}
		})
	}
}
