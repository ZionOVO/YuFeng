package edgecore

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"

	"yufeng/lib/kernel"
)

func TestShapeViolatesAllowlist(t *testing.T) {
	src := &artifactv1.ShapeSource{
		Methods:       []string{"GET"},
		RouteTemplate: "/api/items/{id}",
		Constraints: []*artifactv1.ShapeConstraint{{
			Selector: "query.id", MinLen: 1, MaxLen: 8, Charset: "digit",
		}},
	}
	if err := ValidateShapeSource(src); err != nil {
		t.Fatal(err)
	}
	ok := Canonicalize("GET", "/api/items/12", "id=12", nil, nil, DefaultInspectionProfile())
	if ShapeViolates(src, Request{Method: "GET", Path: "/api/items/12", Query: "id=12"}, ok) {
		t.Fatal("digit id must pass")
	}
	bad := Canonicalize("GET", "/api/items/12", "id=1+UNION", nil, nil, DefaultInspectionProfile())
	if !ShapeViolates(src, Request{Method: "GET", Path: "/api/items/12", Query: "id=1+UNION"}, bad) {
		t.Fatal("non-digit id must violate")
	}
	if ShapeViolates(src, Request{Method: "GET", Path: "/other", Query: "id=x"}, Canonicalize("GET", "/other", "id=x", nil, nil, DefaultInspectionProfile())) {
		t.Fatal("out of scope must not apply")
	}
}

func TestEvaluateShapeViolationUsesProductionCanonicalView(t *testing.T) {
	source := &artifactv1.ShapeSource{
		Methods:       []string{"POST"},
		RouteTemplate: "/api/items/{id}",
		Constraints: []*artifactv1.ShapeConstraint{{
			Selector: "json.quantity", MinLen: 1, MaxLen: 3, Charset: "digit",
		}},
	}
	violates, err := EvaluateShapeViolation(source, Request{
		Method: "post", Path: "/api/./items/42", Headers: map[string]string{"Content-Type": "application/json"}, Body: []byte(`{"quantity":12}`),
	})
	if err != nil || violates {
		t.Fatalf("canonical request violates=%v err=%v", violates, err)
	}
	violates, err = EvaluateShapeViolation(source, Request{
		Method: "POST", Path: "/api/items/42", Headers: map[string]string{"Content-Type": "application/json"}, Body: []byte(`{"quantity":"many"}`),
	})
	if err != nil || !violates {
		t.Fatalf("invalid structured value violates=%v err=%v", violates, err)
	}
	if _, err := EvaluateShapeViolation(&artifactv1.ShapeSource{Methods: []string{"GET"}, PathPrefix: "/"}, Request{}); err == nil {
		t.Fatal("overbroad shape must fail before evaluation")
	}
}

func TestShapeReleaseBlocksOnEnforce(t *testing.T) {
	pub, priv, _ := newKeyPair()
	src := &artifactv1.ShapeSource{
		Methods:    []string{"GET"},
		PathPrefix: "/api/items",
		Constraints: []*artifactv1.ShapeConstraint{{
			Selector: "query.id", MinLen: 1, MaxLen: 4, Charset: "digit",
		}},
	}
	payload, err := protojson.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	a := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_SHAPE, Payload: payload, PayloadSchema: "shape/v1",
		Ttl: durationpb.New(time.Hour), CreatedAt: timestamppb.Now(),
	}
	if err := kernel.SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	set := NewReleaseSet()
	if err := set.Apply(&artifactv1.ReleaseItem{ReleaseId: "rel-shape", Artifact: a, Mode: commonv1.ReleaseMode_RELEASE_MODE_ENFORCE}, pub); err != nil {
		t.Fatal(err)
	}
	dec := set.Check(context.Background(), Request{Method: "GET", Path: "/api/items", Query: "id=abc"}, "req")
	if dec.Action != ActionBlock {
		t.Fatalf("shape violate want block: %+v", dec)
	}
	ok := set.Check(context.Background(), Request{Method: "GET", Path: "/api/items", Query: "id=12"}, "req2")
	if ok.Action != ActionAllow {
		t.Fatalf("shape ok want allow: %+v", ok)
	}
}

func TestApplySnapshotRejectsBadMemberKeepsPrevious(t *testing.T) {
	pub, priv, _ := newKeyPair()
	payload, _ := MarshalRules([]Rule{{ID: "sql", Pattern: `(?i)union`}})
	good := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_RULE, Payload: payload, PayloadSchema: RulePayloadSchema,
		Ttl: durationpb.New(time.Hour), CreatedAt: timestamppb.Now(),
	}
	if err := kernel.SignArtifact(good, priv); err != nil {
		t.Fatal(err)
	}
	set := NewReleaseSet()
	if err := set.Apply(&artifactv1.ReleaseItem{ReleaseId: "rel-good", Artifact: good, Mode: commonv1.ReleaseMode_RELEASE_MODE_ENFORCE}, pub); err != nil {
		t.Fatal(err)
	}
	bad := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_RULE, Payload: []byte("not-json"), PayloadSchema: RulePayloadSchema,
		Ttl: durationpb.New(time.Hour), CreatedAt: timestamppb.Now(),
	}
	if err := kernel.SignArtifact(bad, priv); err != nil {
		t.Fatal(err)
	}
	err := set.ApplySnapshot([]*artifactv1.ReleaseItem{
		{ReleaseId: "rel-good", Artifact: good, Mode: commonv1.ReleaseMode_RELEASE_MODE_ENFORCE},
		{ReleaseId: "rel-bad", Artifact: bad, Mode: commonv1.ReleaseMode_RELEASE_MODE_ENFORCE},
	}, pub)
	if err == nil {
		t.Fatal("bad snapshot must fail")
	}
	dec := set.Check(context.Background(), Request{Path: "/x", Query: "id=1 union select"}, "r")
	if dec.Action != ActionBlock {
		t.Fatalf("previous good release must remain: %+v", dec)
	}
}
