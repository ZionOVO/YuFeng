package brain

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgecore"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
)

func seedProposalCluster(t *testing.T, ctx context.Context, pool *pgxpool.Pool, assetID, route, method string, reason commonv1.TriageReason, keys []*commonv1.DetectionKey) string {
	t.Helper()
	clusterID := "clu-" + newTestSuffix()
	eventID := "evt-" + newTestSuffix()
	detections := make([]*eventv1.Detection, 0, len(keys))
	for _, key := range keys {
		detections = append(detections, &eventv1.Detection{Key: key})
	}
	event := &eventv1.Event{
		Id: eventID, OccurredAt: timestamppb.Now(), AssetId: assetID, Source: "test",
		Kind: eventv1.Kind_KIND_TRAFFIC, Verdict: eventv1.Verdict_VERDICT_OBSERVE,
		Traffic:    &eventv1.Event_Http{Http: &eventv1.Http{Method: method, Path: route}},
		Detections: detections, ClusterId: clusterID,
	}
	raw, err := protojson.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO events(event_id, asset_id, occurred_at, source, kind, verdict, payload)
		VALUES($1,$2,now(),'test','traffic','observe',$3::jsonb)`, eventID, assetID, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO triage_clusters(cluster_id, asset_id, route_template, method, identity_key, reason, event_ids, representative)
		VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8)`, clusterID, assetID, route, method, clusterID, reason.String(), `["`+eventID+`"]`, eventID); err != nil {
		t.Fatal(err)
	}
	seedProposalTaxonomy(t, ctx, pool, assetID)
	return clusterID
}

func seedProposalTaxonomy(t *testing.T, ctx context.Context, pool *pgxpool.Pool, assetID string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM asset_generations WHERE asset_id=$1 AND signed)`, assetID).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		return
	}
	payload, err := protojson.Marshal(edgecore.DefaultTaxonomyMapper())
	if err != nil {
		t.Fatal(err)
	}
	gen := &artifactv1.AssetGeneration{
		GenerationId: "gen-tax-" + newTestSuffix(), AssetId: assetID, GenerationSeq: 1,
		Members: []*artifactv1.ReleaseItem{{ReleaseId: "rel-tax-" + newTestSuffix(), Artifact: &artifactv1.Artifact{
			Kind: artifactv1.Kind_KIND_TAXONOMY_MAPPER, Payload: payload, PayloadSchema: edgecore.TaxonomyMapperSchema,
		}}},
	}
	raw, err := protojson.Marshal(gen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO asset_generations(generation_id, asset_id, generation_seq, envelope, signed)
		VALUES($1,$2,1,$3::jsonb,true)`, gen.GenerationId, assetID, string(raw)); err != nil {
		t.Fatal(err)
	}
}

func proposalDetectionKeys(t *testing.T, ctx context.Context, method, path, query string) []*commonv1.DetectionKey {
	t.Helper()
	crs, err := edgecore.NewCorazaDetector()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := crs.Close(); err != nil {
			t.Errorf("close Coraza detector: %v", err)
		}
	})
	view := edgecore.Canonicalize(method, path, query, nil, nil, edgecore.DefaultInspectionProfile())
	inspection, err := crs.Inspect(ctx, edgecore.InspectionInput{View: view})
	if err != nil {
		t.Fatal(err)
	}
	var keys []*commonv1.DetectionKey
	for _, detection := range inspection.Detections {
		if !edgecore.CRSAutoGovernRule(detection.RuleID) {
			continue
		}
		keys = append(keys, &commonv1.DetectionKey{
			DetectorId: detection.InspectorID, DetectorVersion: detection.Version,
			DetectorManifestDigest: detection.ManifestDigest, RuleId: detection.RuleID,
			Phase: detection.Phase, TargetLocation: detection.Location, TargetSelector: detection.Selector,
			NormalizationProfileDigest: detection.ProfileDigest,
		})
	}
	if len(keys) == 0 {
		t.Fatalf("request %s %s has no auto-governable detection key", method, path)
	}
	return keys
}
