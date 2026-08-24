package brain

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"slices"
	"testing"
)

func TestWorkerActivationEncryptionBindsEnrollmentAndRecipient(t *testing.T) {
	recipient, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := base64.RawURLEncoding.EncodeToString(recipient.PublicKey().Bytes())
	wantFingerprint := sha256.Sum256(recipient.PublicKey().Bytes())
	fingerprint, err := validateActivationPublicKey(publicKey)
	if err != nil {
		t.Fatalf("validate activation public key: %v", err)
	}
	if want := base64.RawURLEncoding.EncodeToString(wantFingerprint[:]); fingerprint != want {
		t.Fatalf("activation fingerprint = %q, want %q", fingerprint, want)
	}

	const enrollmentID = "enroll-security-test"
	plaintext := []byte(`{"bootstrapToken":"secret","workerId":"worker-test"}`)
	sealed, err := encryptWorkerActivation(publicKey, enrollmentID, plaintext)
	if err != nil {
		t.Fatalf("encrypt activation: %v", err)
	}
	if bytes.Contains(sealed, []byte("secret")) {
		t.Fatal("encrypted activation contains plaintext credential")
	}
	if got := decryptWorkerActivationForTest(t, recipient, enrollmentID, sealed); !slices.Equal(got, plaintext) {
		t.Fatalf("decrypted activation = %q, want %q", got, plaintext)
	}

	var envelope encryptedWorkerActivation
	if err := json.Unmarshal(sealed, &envelope); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[len(ciphertext)-1] ^= 1
	envelope.Ciphertext = base64.RawURLEncoding.EncodeToString(ciphertext)
	tampered, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkerActivationDecryptionFails(t, recipient, enrollmentID, tampered)
	assertWorkerActivationDecryptionFails(t, recipient, "enroll-other", sealed)

	otherRecipient, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkerActivationDecryptionFails(t, otherRecipient, enrollmentID, sealed)
}

func TestValidateActivationPublicKeyRejectsUnusableKeys(t *testing.T) {
	tests := map[string]string{
		"empty":        "",
		"not_base64":   "%%%",
		"wrong_length": base64.RawURLEncoding.EncodeToString(make([]byte, 31)),
		"low_order":    base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
	}
	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := validateActivationPublicKey(encoded); err == nil {
				t.Fatal("unusable X25519 public key was accepted")
			}
		})
	}
}

func TestApprovedWorkerManifestDigestIsCanonicalAndSensitiveToLimits(t *testing.T) {
	first, err := approvedWorkerManifestDigest(
		"worker-1", "RUN_SUPERVISOR", "host-a", "linux", "amd64", "v1",
		[]byte(`["seccomp","landlock","seccomp","resource_limits"]`), 2, 1024, 4,
		[]byte(`[{"kind":"asset","id":"asset-b"},{"kind":"asset","id":"asset-a"}]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := approvedWorkerManifestDigest(
		"worker-1", "RUN_SUPERVISOR", "host-a", "linux", "amd64", "v1",
		[]byte(`["resource_limits","landlock","seccomp"]`), 2, 1024, 4,
		[]byte(`[{"id":"asset-a","kind":"asset"},{"id":"asset-b","kind":"asset"}]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equivalent manifests produced different digests: %q != %q", first, second)
	}

	changed, err := approvedWorkerManifestDigest(
		"worker-1", "RUN_SUPERVISOR", "host-a", "linux", "amd64", "v1",
		[]byte(`["resource_limits","landlock","seccomp"]`), 3, 1024, 4,
		[]byte(`[{"kind":"asset","id":"asset-a"},{"kind":"asset","id":"asset-b"}]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("approved concurrency change did not alter manifest digest")
	}
}

func TestWorkerSandboxEligibilityRequiresPlatformSpecificCompleteSet(t *testing.T) {
	tests := []struct {
		name         string
		operatingSys string
		architecture string
		capabilities []string
		wantPlatform bool
		wantMissing  []string
	}{
		{name: "linux_amd64_complete", operatingSys: "LINUX", architecture: "AMD64", capabilities: []string{"SECCOMP", "landlock", "resource_limits"}, wantPlatform: true},
		{name: "linux_missing_seccomp", operatingSys: "linux", architecture: "arm64", capabilities: []string{"landlock", "resource_limits"}, wantPlatform: true, wantMissing: []string{"seccomp"}},
		{name: "windows_complete", operatingSys: "windows", architecture: "amd64", capabilities: []string{"restricted_token", "appcontainer", "job_object"}, wantPlatform: true},
		{name: "darwin_complete", operatingSys: "darwin", architecture: "arm64", capabilities: []string{"sandbox_profile", "resource_limits"}, wantPlatform: true},
		{name: "unsupported_architecture", operatingSys: "linux", architecture: "386", capabilities: []string{"landlock", "seccomp", "resource_limits"}},
		{name: "unsupported_system", operatingSys: "freebsd", architecture: "amd64", wantMissing: []string{"supported_platform_sandbox"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := supportedWorkerPlatform(test.operatingSys, test.architecture); got != test.wantPlatform {
				t.Fatalf("supportedWorkerPlatform() = %v, want %v", got, test.wantPlatform)
			}
			missing := missingSandboxCapabilities(test.operatingSys, test.capabilities)
			if !slices.Equal(missing, test.wantMissing) {
				t.Fatalf("missingSandboxCapabilities() = %v, want %v", missing, test.wantMissing)
			}
			if got := hasVerifiedSandbox(test.operatingSys, test.capabilities); got != (len(test.wantMissing) == 0) {
				t.Fatalf("hasVerifiedSandbox() = %v, missing = %v", got, test.wantMissing)
			}
		})
	}
}

func decryptWorkerActivationForTest(t *testing.T, recipient *ecdh.PrivateKey, enrollmentID string, raw []byte) []byte {
	t.Helper()
	var envelope encryptedWorkerActivation
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	ephemeralRaw, err := base64.RawURLEncoding.DecodeString(envelope.EphemeralPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	ephemeral, err := ecdh.X25519().NewPublicKey(ephemeralRaw)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := recipient.ECDH(ephemeral)
	if err != nil {
		t.Fatal(err)
	}
	material := append([]byte("yufeng-worker-activation-v1\x00"+enrollmentID+"\x00"), secret...)
	key := sha256.Sum256(material)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(enrollmentID))
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func assertWorkerActivationDecryptionFails(t *testing.T, recipient *ecdh.PrivateKey, enrollmentID string, raw []byte) {
	t.Helper()
	defer func() {
		if recover() != nil {
			t.Fatal("activation decryption panicked")
		}
	}()
	var envelope encryptedWorkerActivation
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	ephemeralRaw, err := base64.RawURLEncoding.DecodeString(envelope.EphemeralPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	ephemeral, err := ecdh.X25519().NewPublicKey(ephemeralRaw)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := recipient.ECDH(ephemeral)
	if err != nil {
		return
	}
	material := append([]byte("yufeng-worker-activation-v1\x00"+enrollmentID+"\x00"), secret...)
	key := sha256.Sum256(material)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gcm.Open(nil, nonce, ciphertext, []byte(enrollmentID)); err == nil {
		t.Fatal("activation decrypted outside its recipient or enrollment binding")
	}
}
