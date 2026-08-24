package replay

import (
	"context"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

func TestShapeReplayUsesOnlyDeclaredRoute(t *testing.T) {
	payload, err := protojson.Marshal(&artifactv1.ShapeSource{
		Methods: []string{"GET"}, RouteTemplate: "/api/items",
		Constraints: []*artifactv1.ShapeConstraint{{Selector: "query.page", MinLen: 1, MaxLen: 8, Charset: "digit"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := RunShape(context.Background(), &artifactv1.Artifact{
		PayloadSchema: shapeSchema, Payload: payload, Scope: &artifactv1.Scope{AssetIds: []string{"asset-1"}},
	}, BuiltinCorpus("asset-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !report.GetPassed() || report.GetMaliciousTotal() != 2 || report.GetMaliciousBlocked() != 2 || report.GetBenignBlocked() != 0 {
		t.Fatalf("shape replay report=%+v", report)
	}
}
