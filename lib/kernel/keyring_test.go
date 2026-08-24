package kernel

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

func TestKeyRotationOldArtifactStillVerifies(t *testing.T) {
	pubOld, privOld, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubNew, privNew, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldArt := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_POLICY, Payload: []byte(`{"k":"old"}`), CreatedBy: "t", CreatedAt: timestamppb.Now()}
	if err := SignArtifact(oldArt, privOld); err != nil {
		t.Fatal(err)
	}
	newArt := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_POLICY, Payload: []byte(`{"k":"new"}`), CreatedBy: "t", CreatedAt: timestamppb.Now()}
	if err := SignArtifact(newArt, privNew); err != nil {
		t.Fatal(err)
	}
	ring, err := NewVerifyRing(pubOld, pubNew)
	if err != nil {
		t.Fatal(err)
	}
	if err := ring.Verify(oldArt); err != nil {
		t.Fatalf("old artifact must still verify after rotation: %v", err)
	}
	if err := ring.Verify(newArt); err != nil {
		t.Fatalf("new artifact must verify with new key: %v", err)
	}
	if err := VerifyArtifact(newArt, pubOld); err == nil {
		t.Fatal("new artifact must not verify with retired signing key alone")
	}
}

func TestVerifyRingRejectsUnknownKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_POLICY, Payload: []byte(`x`), CreatedBy: "t", CreatedAt: timestamppb.Now()}
	if err := SignArtifact(a, other); err != nil {
		t.Fatal(err)
	}
	ring, err := NewVerifyRing(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := ring.Verify(a); err == nil {
		t.Fatal("unknown key must fail")
	}
	_ = priv
}
