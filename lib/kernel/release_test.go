package kernel

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

func newDraft(t *testing.T) (*Draft, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &artifactv1.Artifact{
		Kind:          artifactv1.Kind_KIND_RULE,
		Payload:       []byte(`[{"id":"sql","pattern":"union"}]`),
		PayloadSchema: "rules/v1",
		Ttl:           durationpb.New(time.Hour),
		CreatedAt:     timestamppb.Now(),
		Scope:         &artifactv1.Scope{AssetIds: []string{"asset-1"}},
	}
	d, err := NewDraft("rel-1", a, "test")
	if err != nil {
		t.Fatal(err)
	}
	return d, priv
}

func passReport() *artifactv1.ReplayReport {
	return &artifactv1.ReplayReport{
		MaliciousTotal:    5,
		MaliciousBlocked:  5,
		BenignTotal:       100,
		BenignBlocked:     0,
		ManagementTotal:   10,
		ManagementBlocked: 0,
		Passed:            true,
		CorpusRef:         "builtin:l1-rules-v1",
	}
}

func TestReleaseLifecycle(t *testing.T) {
	d, priv := newDraft(t)
	res, err := d.Gate(passReport(), priv)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || res.Signed == nil || res.Signed.State() != commonv1.ReleaseState_RELEASE_STATE_SIGNED {
		t.Fatalf("门禁应通过: passed=%v signed=%v", res.Passed, res.Signed)
	}
	if err := VerifyArtifact(res.Signed.Envelope, priv.Public().(ed25519.PublicKey)); err != nil {
		t.Fatalf("门禁签名制品验签失败: %v", err)
	}
	shadow := res.Signed.StartShadow()
	canary, err := shadow.PromoteCanary(5)
	if err != nil {
		t.Fatal(err)
	}
	if canary.CanaryPercent != 5 {
		t.Fatalf("canary percent = %d", canary.CanaryPercent)
	}
	enforce := canary.PromoteEnforce()
	active, err := ActiveOf(enforce)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := RetireActive(active, commonv1.RetireReason_RETIRE_REASON_MANUAL)
	if err != nil {
		t.Fatal(err)
	}
	if retired.State() != commonv1.ReleaseState_RELEASE_STATE_RETIRED || retired.Reason != commonv1.RetireReason_RETIRE_REASON_MANUAL {
		t.Fatalf("退休状态错误: %+v", retired)
	}
}

func TestGateRejectsBadReplay(t *testing.T) {
	d, priv := newDraft(t)
	report := passReport()
	report.BenignBlocked = 1
	res, err := d.Gate(report, priv)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.Draft == nil || res.Signed != nil {
		t.Fatalf("良性误伤不应通过: %+v", res)
	}
	if res.Draft.Envelope.ReplayReport == nil {
		t.Fatal("失败报告应保留在 draft 信封上")
	}
	if _, err := ActiveOf(res.Draft); err == nil {
		t.Fatal("draft 不应该是 active 状态")
	}
}

func TestCanaryPercentBounds(t *testing.T) {
	d, priv := newDraft(t)
	res, err := d.Gate(passReport(), priv)
	if err != nil {
		t.Fatal(err)
	}
	shadow := res.Signed.StartShadow()
	if _, err := shadow.PromoteCanary(0); err == nil {
		t.Fatal("0% 不应放行")
	}
	if _, err := shadow.PromoteCanary(26); err == nil {
		t.Fatal("26% 不应放行")
	}
}

func TestShadowPromoteEnforce(t *testing.T) {
	d, priv := newDraft(t)
	res, err := d.Gate(passReport(), priv)
	if err != nil {
		t.Fatal(err)
	}
	shadow := res.Signed.StartShadow()
	enforce := shadow.PromoteEnforce()
	if enforce == nil || enforce.State() != commonv1.ReleaseState_RELEASE_STATE_ENFORCE {
		t.Fatalf("shadow 直达 enforce 失败: %+v", enforce)
	}
	if enforce.ReleaseID() != shadow.ReleaseID() {
		t.Fatalf("release id changed: %s → %s", shadow.ReleaseID(), enforce.ReleaseID())
	}
}

func TestCanaryMinUnitsCeil(t *testing.T) {
	if got := CanaryMinUnits(5); got != 20 {
		t.Fatalf("5%% want 20 units, got %d", got)
	}
	if got := CanaryMinUnits(25); got != 4 {
		t.Fatalf("25%% want 4 units, got %d", got)
	}
	if got := CanaryMinUnits(0); got != CanaryMinUnits(CanaryPercentDefault) {
		t.Fatalf("zero percent must use default: %d", got)
	}
}

func TestNewDraftRejectsSignedInput(t *testing.T) {
	_, priv := newDraft(t)
	a := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_RULE, Payload: []byte(`[]`), Ttl: durationpb.New(time.Hour)}
	if err := SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDraft("rel-2", a, "test"); err == nil {
		t.Fatal("已签名制品不能作为 draft")
	}
}
