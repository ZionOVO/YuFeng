//go:build yufeng_dev

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

func TestParseMode(t *testing.T) {
	tests := []struct {
		in      string
		want    commonv1.ReleaseMode
		wantErr bool
	}{
		{"shadow", commonv1.ReleaseMode_RELEASE_MODE_SHADOW, false},
		{"enforce", commonv1.ReleaseMode_RELEASE_MODE_ENFORCE, false},
		{"canary", 0, true}, // 纵切片未开放 canary，必须显式报错而不是默默放行
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := parseMode(tt.in)
		if (err != nil) != tt.wantErr {
			t.Fatalf("parseMode(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Fatalf("parseMode(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestValidateLaunchModeRequiresBrainUnlessLocalDemoIsExplicit(t *testing.T) {
	if err := validateLaunchMode("", false, false); err == nil {
		t.Fatal("production edge must require a brain URL")
	}
	if err := validateLaunchMode("", true, false); err != nil {
		t.Fatalf("explicit local demo must remain available for development: %v", err)
	}
	if err := validateLaunchMode("https://brain:9050", false, false); err != nil {
		t.Fatalf("brain mode must be accepted: %v", err)
	}
	if err := validateLaunchMode("https://brain:9050", true, false); err == nil {
		t.Fatal("local demo and brain mode must be mutually exclusive")
	}
	if err := validateLaunchMode("http://brain:9050", false, false); err == nil {
		t.Fatal("production edge must reject a plaintext brain")
	}
	if err := validateLaunchMode("http://127.0.0.1:9050", false, true); err != nil {
		t.Fatalf("explicit development mode may use a plaintext brain: %v", err)
	}
	if err := validateLaunchMode("brain:9050", false, true); err == nil {
		t.Fatal("brain URL without an HTTP scheme must fail at startup")
	}
}

func TestLoadPubKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "pubkey.hex")
	if err := os.WriteFile(path, []byte(hex.EncodeToString(priv.Public().(ed25519.PublicKey))+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPubKey(path); err != nil {
		t.Fatalf("loadPubKey: %v", err)
	}
	if _, err := loadPubKey(filepath.Join(dir, "不存在")); err == nil {
		t.Fatal("缺文件应报错")
	}
	// 坏公钥必须在装载期报错，而不是让 ed25519.Verify 在请求路径上 panic
	if err := os.WriteFile(filepath.Join(dir, "bad.hex"), []byte("abcd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPubKey(filepath.Join(dir, "bad.hex")); err == nil {
		t.Fatal("非法长度的公钥应报错")
	}
}

func TestLoadSourcePseudonymizerRequiresExactKeyFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "source.key")
	if err := os.WriteFile(good, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSourcePseudonymizer(good); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, make([]byte, 31), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSourcePseudonymizer(bad); err == nil {
		t.Fatal("non-256-bit source pseudonym key must fail closed")
	}
	if _, err := loadSourcePseudonymizer(""); err == nil {
		t.Fatal("production edge must require a source pseudonym key file")
	}
}

func TestResolveUpstream(t *testing.T) {
	if _, cleanup, err := resolveUpstream("builtin"); err != nil {
		t.Fatalf("builtin 上游: %v", err)
	} else {
		cleanup()
	}
	if _, _, err := resolveUpstream("localhost:9999"); err == nil {
		t.Fatal("无协议的上游地址应在启动期报错")
	}
	if _, _, err := resolveUpstream("ftp://x"); err == nil {
		t.Fatal("非 http/https 协议应报错")
	}
}

// loadDetectors 的装载期校验矩阵。
func TestLoadDetectors(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("正常制品装载", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, "good.json", priv, artifactSpec{payload: `[{"id":"sql","pattern":"(?i)union\\s+select"}]`})
		ds, err := loadDetectors(dir, pub, "asset-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(ds) != 1 || ds[0].ID() == "" {
			t.Fatalf("检测器数 = %d, want 1", len(ds))
		}
	})
	t.Run("范围不含本资产的制品跳过", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, "scoped.json", priv, artifactSpec{
			payload: `[{"id":"sql","pattern":"x"}]`,
			scope:   &artifactv1.Scope{AssetIds: []string{"other-asset"}},
		})
		ds, err := loadDetectors(dir, pub, "asset-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(ds) != 0 {
			t.Fatalf("范围不含本资产的制品应被跳过，检测器数 = %d", len(ds))
		}
	})
	t.Run("范围包含本资产的制品装载", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, "scoped.json", priv, artifactSpec{
			payload: `[{"id":"sql","pattern":"x"}]`,
			scope:   &artifactv1.Scope{AssetIds: []string{"asset-1", "asset-2"}},
		})
		ds, err := loadDetectors(dir, pub, "asset-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(ds) != 1 {
			t.Fatalf("范围含本资产应装载，检测器数 = %d", len(ds))
		}
	})
	t.Run("缺创建时间的制品拒绝（无法判定过期）", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, "nocreated.json", priv, artifactSpec{payload: `[{"id":"sql","pattern":"x"}]`, omitCreatedAt: true})
		if _, err := loadDetectors(dir, pub, "asset-1"); err == nil {
			t.Fatal("缺 created_at 的制品应拒绝装载")
		}
	})
	t.Run("过期制品跳过", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, "expired.json", priv, artifactSpec{
			payload:   `[{"id":"sql","pattern":"x"}]`,
			createdAt: time.Now().Add(-2 * time.Hour),
			ttl:       time.Hour,
		})
		ds, err := loadDetectors(dir, pub, "asset-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(ds) != 0 {
			t.Fatalf("过期制品应被跳过，检测器数 = %d", len(ds))
		}
	})
	t.Run("非规则制品拒绝按规则解析", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, "proc.json", priv, artifactSpec{payload: `{}`, kind: artifactv1.Kind_KIND_PROCEDURE})
		if _, err := loadDetectors(dir, pub, "asset-1"); err == nil {
			t.Fatal("程序制品被当作规则制品装载")
		}
	})
	t.Run("载荷结构标识不符拒绝", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, "schema.json", priv, artifactSpec{payload: `[{"id":"x","pattern":"y"}]`, schema: "rules/v999"})
		if _, err := loadDetectors(dir, pub, "asset-1"); err == nil {
			t.Fatal("未知载荷结构标识竟然装载")
		}
	})
	t.Run("伪造签名拒绝", func(t *testing.T) {
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		dir := t.TempDir()
		writeArtifact(t, dir, "forged.json", other, artifactSpec{payload: `[{"id":"evil","pattern":"x"}]`})
		if _, err := loadDetectors(dir, pub, "asset-1"); err == nil {
			t.Fatal("未授权签名的制品竟然装载成功")
		}
	})
}

type artifactSpec struct {
	payload       string
	kind          artifactv1.Kind
	schema        string
	createdAt     time.Time
	ttl           time.Duration
	scope         *artifactv1.Scope
	omitCreatedAt bool
}

func writeArtifact(t *testing.T, dir, name string, priv ed25519.PrivateKey, spec artifactSpec) {
	t.Helper()
	// 与正常规则制品一致的默认值
	if spec.kind == 0 {
		spec.kind = artifactv1.Kind_KIND_RULE
	}
	if spec.schema == "" {
		spec.schema = edgecore.RulePayloadSchema
	}
	if spec.ttl == 0 {
		spec.ttl = time.Hour
	}
	if spec.createdAt.IsZero() && !spec.omitCreatedAt {
		spec.createdAt = time.Now()
	}
	a := &artifactv1.Artifact{
		Kind:          spec.kind,
		Payload:       []byte(spec.payload),
		Ttl:           durationpb.New(spec.ttl),
		PayloadSchema: spec.schema,
		Scope:         spec.scope,
	}
	if !spec.omitCreatedAt {
		a.CreatedAt = timestamppb.New(spec.createdAt)
	}
	if err := kernel.SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	raw, err := protojson.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
