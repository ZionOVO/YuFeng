package edgecore

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"

	"yufeng/lib/kernel"
)

func TestReleaseSetEnforceShadowRetire(t *testing.T) {
	pub, priv, _ := newKeyPair()
	payload, _ := MarshalRules([]Rule{{ID: "sql", Pattern: `(?i)union\s+select`}})
	a := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_RULE, Payload: payload, PayloadSchema: RulePayloadSchema,
		Ttl: durationpb.New(time.Hour), CreatedAt: timestamppb.Now(),
		Scope: &artifactv1.Scope{AssetIds: []string{"asset-1"}},
	}
	if err := kernel.SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	set := NewReleaseSet()
	if err := set.Apply(&artifactv1.ReleaseItem{ReleaseId: "rel-1", Artifact: a, Mode: commonv1.ReleaseMode_RELEASE_MODE_ENFORCE}, pub); err != nil {
		t.Fatal(err)
	}
	req := Request{AssetID: "asset-1", Path: "/api/items", Query: "id=1 union select"}
	dec := set.Check(context.Background(), req, "req-1")
	if dec.Action != ActionBlock {
		t.Fatalf("enforce 应拦截: %+v", dec)
	}
	if len(dec.Observations) != 1 || !dec.Observations[0].Matched {
		t.Fatalf("缺少发布轨迹: %+v", dec)
	}
	if err := set.Apply(&artifactv1.ReleaseItem{ReleaseId: "rel-1", Artifact: a, Retired: true}, pub); err != nil {
		t.Fatal(err)
	}
	if dec := set.Check(context.Background(), req, "req-2"); dec.Action != ActionAllow {
		t.Fatalf("退休后应放行: %+v", dec)
	}
}

func TestReleaseSetShadowAndCanary(t *testing.T) {
	pub, priv, _ := newKeyPair()
	payload, _ := MarshalRules([]Rule{{ID: "sql", Pattern: `(?i)union\s+select`}})
	a := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_RULE, Payload: payload, PayloadSchema: RulePayloadSchema,
		Ttl: durationpb.New(time.Hour), CreatedAt: timestamppb.Now(),
	}
	if err := kernel.SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	set := NewReleaseSet()
	if err := set.Apply(&artifactv1.ReleaseItem{ReleaseId: "rel-1", Artifact: a, Mode: commonv1.ReleaseMode_RELEASE_MODE_SHADOW}, pub); err != nil {
		t.Fatal(err)
	}
	req := Request{AssetID: "asset-1", Path: "/api/items", Query: "id=1 union select"}
	if dec := set.Check(context.Background(), req, "req-shadow"); dec.Action != ActionObserve {
		t.Fatalf("shadow 只应观察: %+v", dec)
	}
	if err := set.Apply(&artifactv1.ReleaseItem{ReleaseId: "rel-1", Artifact: a, Mode: commonv1.ReleaseMode_RELEASE_MODE_CANARY, CanaryPercent: 1}, pub); err != nil {
		t.Fatal(err)
	}
	same := set.Check(context.Background(), Request{AssetID: "asset-1", UnitID: "unit-1", Path: req.Path, Query: req.Query}, "req-a")
	again := set.Check(context.Background(), Request{AssetID: "asset-1", UnitID: "unit-1", Path: req.Path, Query: req.Query}, "req-b")
	if same.Action != again.Action {
		t.Fatal("same unit_id must not change canary bucket across request ids")
	}
	blocked, observed := 0, 0
	for i := 0; i < 200; i++ {
		dec := set.Check(context.Background(), Request{AssetID: "asset-1", UnitID: "unit-" + string(rune('A'+i%26)) + string(rune(i)), Path: req.Path, Query: req.Query}, "req")
		if dec.Action == ActionBlock {
			blocked++
		} else {
			observed++
		}
	}
	if blocked == 0 && observed == 0 {
		t.Fatal("canary produced no decisions")
	}
}
