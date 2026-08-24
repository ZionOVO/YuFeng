package kernel

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

func TestMemoryRollbackKeyCannotSignPolicy(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewMemorySigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	s.RollbackOnly = true
	if _, err := s.Sign([]byte("digest-digest-digest-digest-diges")); err == nil {
		t.Fatal("rollback key must not sign new policy")
	}
}

func TestSocketSignerRoundTrip(t *testing.T) {
	sock, inner, _ := newTestSocketSigner(t)
	artifact := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_RULE, Payload: []byte(`{"pattern":"safe"}`), PayloadSchema: "rule/v1"}
	if err := SignArtifactWithSigner(artifact, sock); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifact(artifact, inner.Public()); err != nil {
		t.Fatalf("typed socket signature must verify: %v", err)
	}
	if _, err := sock.Sign([]byte("arbitrary bytes")); err == nil {
		t.Fatal("socket signer must reject untyped arbitrary bytes")
	}
}

func TestSocketSignerTypedOperationsRoundTrip(t *testing.T) {
	sock, inner, _ := newTestSocketSigner(t)
	artifact := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_RULE, Payload: []byte(`{"pattern":"safe"}`), PayloadSchema: "rule/v1"}
	if err := SignArtifactWithSigner(artifact, sock); err != nil {
		t.Fatal(err)
	}
	generation := &artifactv1.AssetGeneration{
		GenerationId: "generation-1", AssetId: "asset-1", GenerationSeq: 1,
		Members: []*artifactv1.ReleaseItem{{ReleaseId: "release-1", Artifact: artifact}},
	}
	if err := SignGenerationWithSigner(generation, sock); err != nil {
		t.Fatal(err)
	}
	if err := VerifyGeneration(generation, inner.Public()); err != nil {
		t.Fatalf("typed generation signature must verify: %v", err)
	}
	plan := &artifactv1.UnitListenPlan{UnitId: "unit-1", Version: 1}
	if err := SignUnitListenPlanWithSigner(plan, sock); err != nil {
		t.Fatal(err)
	}
	if err := VerifyUnitListenPlan(plan, inner.Public()); err != nil {
		t.Fatalf("typed listen plan signature must verify: %v", err)
	}
	checkpoint := &AuditCheckpoint{Sequence: 7, Head: "sha256:ledger-head", CreatedAt: time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)}
	if err := SignAuditCheckpointWithSigner(checkpoint, sock); err != nil {
		t.Fatal(err)
	}
	if err := VerifySignedAuditCheckpoint(checkpoint, inner.Public(), checkpoint.Sequence, checkpoint.Head); err != nil {
		t.Fatalf("typed checkpoint signature must verify: %v", err)
	}
	var output bytes.Buffer
	if err := WriteSignedAuditCheckpoint(&output, checkpoint); err != nil {
		t.Fatal(err)
	}
	var written AuditCheckpoint
	if err := json.Unmarshal(output.Bytes(), &written); err != nil {
		t.Fatal(err)
	}
	if written.Sequence != checkpoint.Sequence || written.Head != checkpoint.Head || !bytes.Equal(written.Signature, checkpoint.Signature) {
		t.Fatalf("written checkpoint=%+v", written)
	}
}

func TestSocketSignerRejectsTrailingRequestData(t *testing.T) {
	_, _, address := newTestSocketSigner(t)
	checkpoint := &AuditCheckpoint{Sequence: 1, Head: "sha256:head", CreatedAt: time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)}
	envelope, err := auditCheckpointEnvelope(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(signingRequest{Version: signingProtocolVersion, Operation: SignOperationAuditCheckpoint, Envelope: envelope})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.Dial("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close() //nolint:errcheck // 测试请求结束后尽力清理。
	if err := writeSigningFrame(connection, append(request, []byte(`{}`)...)); err != nil {
		t.Fatal(err)
	}
	responseRaw, err := readSigningFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	var response signingResponse
	if err := json.Unmarshal(responseRaw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == "" || len(response.Signature) != 0 {
		t.Fatalf("trailing request was signed: %+v", response)
	}
}

func TestSocketSignerRejectsInvalidSignatureResponse(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp("", "yufeng-invalid-signer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	address := filepath.Join(directory, "signer.sock")
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
		defer connection.Close() //nolint:errcheck // 测试签名器写完无效签名后尽力释放连接。
		if _, readErr := readSigningFrame(connection); readErr != nil {
			return
		}
		_ = writeSigningResponse(connection, signingResponse{Signature: make([]byte, ed25519.SignatureSize)})
	}()
	signer, err := NewSocketSigner(address, public)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := &AuditCheckpoint{Sequence: 1, Head: "sha256:head", CreatedAt: time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)}
	envelope, err := auditCheckpointEnvelope(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.SignTyped(SignOperationAuditCheckpoint, envelope); err == nil {
		t.Fatal("socket signer must verify the returned signature before accepting it")
	}
}

func TestAuditCheckpointSigningEnvelopeRejectsTrailingData(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewMemorySigner(private)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := &AuditCheckpoint{Sequence: 1, Head: "sha256:head", CreatedAt: time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)}
	envelope, err := auditCheckpointEnvelope(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalTypedSigningRequest(signingRequest{
		Version: signingProtocolVersion, Operation: SignOperationAuditCheckpoint, Envelope: append(envelope, []byte(`{}`)...),
	}, signer.Public()); err == nil {
		t.Fatal("checkpoint envelope with trailing data must be rejected")
	}
}

func TestSignedAuditCheckpointRejectsTampering(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewMemorySigner(private)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := &AuditCheckpoint{Sequence: 9, Head: "sha256:ledger-head"}
	if err := SignAuditCheckpointWithSigner(checkpoint, signer); err != nil {
		t.Fatal(err)
	}
	if checkpoint.CreatedAt.IsZero() {
		t.Fatal("signing must freeze checkpoint creation time")
	}
	tests := []struct {
		name   string
		mutate func(*AuditCheckpoint)
		seq    int64
		head   string
	}{
		{name: "sequence", mutate: func(value *AuditCheckpoint) { value.Sequence++ }, seq: checkpoint.Sequence, head: checkpoint.Head},
		{name: "head", mutate: func(value *AuditCheckpoint) { value.Head = "sha256:other" }, seq: checkpoint.Sequence, head: checkpoint.Head},
		{name: "created at", mutate: func(value *AuditCheckpoint) { value.CreatedAt = value.CreatedAt.Add(time.Second) }, seq: checkpoint.Sequence, head: checkpoint.Head},
		{name: "key id", mutate: func(value *AuditCheckpoint) { value.KeyID = "other" }, seq: checkpoint.Sequence, head: checkpoint.Head},
		{name: "signature", mutate: func(value *AuditCheckpoint) { value.Signature[0] ^= 0xff }, seq: checkpoint.Sequence, head: checkpoint.Head},
		{name: "expected sequence", mutate: func(*AuditCheckpoint) {}, seq: checkpoint.Sequence + 1, head: checkpoint.Head},
		{name: "expected head", mutate: func(*AuditCheckpoint) {}, seq: checkpoint.Sequence, head: "sha256:other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := *checkpoint
			value.Signature = append([]byte(nil), checkpoint.Signature...)
			test.mutate(&value)
			if err := VerifySignedAuditCheckpoint(&value, public, test.seq, test.head); err == nil {
				t.Fatal("tampered checkpoint must be rejected")
			}
		})
	}
	if err := WriteSignedAuditCheckpoint(&bytes.Buffer{}, &AuditCheckpoint{}); err == nil {
		t.Fatal("unsigned checkpoint must not be written")
	}
	if err := SignAuditCheckpointWithSigner(nil, signer); err == nil {
		t.Fatal("nil checkpoint must not be signed")
	}
	malformedKey := ed25519.PublicKey("short")
	malformedCheckpoint := *checkpoint
	malformedCheckpoint.KeyID = KeyID(malformedKey)
	if err := VerifySignedAuditCheckpoint(&malformedCheckpoint, malformedKey, checkpoint.Sequence, checkpoint.Head); err == nil {
		t.Fatal("invalid checkpoint verification key length must be rejected")
	}
}

func TestAuditCheckpointDetectsBreak(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteAuditCheckpoint(&buf, 3, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAuditCheckpoint(bytes.NewReader(buf.Bytes()), 3, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAuditCheckpoint(bytes.NewReader(buf.Bytes()), 3, "tampered"); err == nil {
		t.Fatal("tampered db head must break")
	}
	if err := VerifyAuditCheckpoint(strings.NewReader("3 abc trailing\n"), 3, "abc"); err == nil {
		t.Fatal("checkpoint record with trailing fields must be rejected")
	}
}

func newTestSocketSigner(t *testing.T) (*SocketSigner, *MemorySigner, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := NewMemorySigner(private)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp("", "yufeng-signer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	address := filepath.Join(directory, "signer.sock")
	listener, err := net.Listen("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() { _ = ServeMemorySigner(listener, inner) }()
	socket, err := NewSocketSigner(address, inner.Public())
	if err != nil {
		t.Fatal(err)
	}
	return socket, inner, address
}
