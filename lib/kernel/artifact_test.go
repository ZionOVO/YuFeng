package kernel

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"google.golang.org/protobuf/types/known/durationpb"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

func TestSignAndVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &artifactv1.Artifact{
		Kind:    artifactv1.Kind_KIND_RULE,
		Payload: []byte(`[{"id":"sql","pattern":"union"}]`),
		Ttl:     durationpb.New(3600e9),
	}
	if err := SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	// 签发自动补 id = 全信封哈希，且写入签名时间
	wantID, err := ArtifactID(a)
	if err != nil {
		t.Fatal(err)
	}
	if a.Id != wantID {
		t.Fatalf("id = %s, want %s", a.Id, wantID)
	}
	if a.Signature.SignedAt == nil {
		t.Fatal("签名缺少 SignedAt")
	}
	if err := VerifyArtifact(a, pub); err != nil {
		t.Fatalf("验签失败: %v", err)
	}
	// 验签不得修改原制品（签名字段原样保留）
	if a.Signature == nil || len(a.Signature.Sig) == 0 {
		t.Fatal("验签后签名字段丢失（VerifyArtifact 必须在副本上计算）")
	}
}

func TestSignArtifactRejectsMismatchedID(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &artifactv1.Artifact{
		Id:      "sha256:deadbeef", // 与信封不符的非空 id
		Kind:    artifactv1.Kind_KIND_RULE,
		Payload: []byte(`[]`),
		Ttl:     durationpb.New(3600e9),
	}
	if err := SignArtifact(a, priv); err == nil {
		t.Fatal("签发器不应签出 id 与信封哈希不符的制品（会生产验不过自己的废品）")
	}
}

func TestSignArtifactRejectsResigning(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_RULE, Payload: []byte(`[]`), Ttl: durationpb.New(3600e9)}
	if err := SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	if err := SignArtifact(a, priv); err == nil {
		t.Fatal("已签名制品不应允许重签")
	}
}

func TestVerifyRejectsBadInput(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signed := func() *artifactv1.Artifact {
		a := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_RULE, Payload: []byte(`[]`), Ttl: durationpb.New(3600e9)}
		if err := SignArtifact(a, priv); err != nil {
			t.Fatal(err)
		}
		return a
	}

	t.Run("id 与信封不符", func(t *testing.T) {
		a := signed()
		a.Payload = []byte(`[{"id":"evil"}]`)
		if err := VerifyArtifact(a, pub); err == nil {
			t.Fatal("载荷被篡改后验签竟然通过")
		}
	})
	t.Run("换 scope 后 id 改变", func(t *testing.T) {
		a := signed()
		before := a.Id
		a.Scope = &artifactv1.Scope{AssetIds: []string{"asset-b"}}
		id, err := ArtifactID(a)
		if err != nil {
			t.Fatal(err)
		}
		if id == before {
			t.Fatal("同 payload 不同 scope 的制品 id 不应相同")
		}
	})
	t.Run("伪造签名", func(t *testing.T) {
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		a := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_RULE, Payload: []byte(`[]`), Ttl: durationpb.New(3600e9)}
		if err := SignArtifact(a, other); err != nil {
			t.Fatal(err)
		}
		if err := VerifyArtifact(a, pub); err == nil {
			t.Fatal("未授权私钥签的制品竟然通过")
		}
	})
	t.Run("非法公钥长度不 panic 而是报错", func(t *testing.T) {
		a := signed()
		if err := VerifyArtifact(a, ed25519.PublicKey(make([]byte, 2))); err == nil {
			t.Fatal("坏公钥长度应报错")
		}
	})
	t.Run("缺少签名", func(t *testing.T) {
		a := signed()
		a.Signature = nil
		if err := VerifyArtifact(a, pub); err == nil {
			t.Fatal("无签名制品应报错")
		}
	})
}
