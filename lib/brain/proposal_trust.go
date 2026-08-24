package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	governv1 "yufeng/proto/gen/governv1"
)

type trustedProposal struct {
	intent       *governv1.ProposalIntent
	scope        *artifactv1.Scope
	evidenceRefs []string
	scopeRisk    commonv1.ScopeRisk
	evidence     commonv1.EvidenceClass
}

type proposalCluster struct {
	assetID  string
	route    string
	method   string
	reason   string
	eventIDs []string
	events   []*eventv1.Event
}

// deriveTrustedProposal 只从聚类钉死的事件投影派生生产提案的可信字段。
func deriveTrustedProposal(ctx context.Context, pool *pgxpool.Pool, msg *governv1.ProposeArtifactRequest) (trustedProposal, error) {
	if msg == nil || msg.GetIntent() == nil {
		return trustedProposal{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("production requires proposal intent"))
	}
	if msg.GetKind() != artifactv1.Kind_KIND_UNSPECIFIED || len(msg.GetPayload()) > 0 || msg.GetPayloadSchema() != "" ||
		msg.GetCreatedBy() != "" || len(msg.GetEvidenceRefs()) > 0 {
		return trustedProposal{}, connect.NewError(connect.CodeInvalidArgument, errors.New("production proposal accepts intent only"))
	}
	intent := msg.GetIntent()
	clusterID := strings.TrimSpace(intent.GetClusterId())
	if clusterID == "" {
		return trustedProposal{}, connect.NewError(connect.CodeInvalidArgument, errors.New("cluster_id is required"))
	}
	cluster, err := loadProposalCluster(ctx, pool, clusterID)
	if err != nil {
		return trustedProposal{}, err
	}
	if msg.GetScope() == nil || len(msg.GetScope().GetAssetIds()) != 1 || msg.GetScope().GetAssetIds()[0] != cluster.assetID {
		return trustedProposal{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("proposal scope must equal cluster asset"))
	}
	scope := proto.Clone(msg.GetScope()).(*artifactv1.Scope)
	if scope.GetRouteSelector() != "" && !strings.HasPrefix(cluster.route, scope.GetRouteSelector()) {
		return trustedProposal{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("proposal scope route is not supported by cluster"))
	}

	trustedIntent := proto.Clone(intent).(*governv1.ProposalIntent)
	switch intent.GetKind() {
	case commonv1.ProposalKind_PROPOSAL_KIND_POLICY:
		if cluster.reason != commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED.String() {
			return trustedProposal{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("policy proposal requires detected unmitigated cluster"))
		}
		keys, err := trustedPolicyKeys(intent.GetDetectionKeys(), cluster.events)
		if err != nil {
			return trustedProposal{}, err
		}
		if err := deriveClusterRoute(trustedIntent, cluster); err != nil {
			return trustedProposal{}, err
		}
		trustedIntent.DetectionKeys = keys
		risk := commonv1.ScopeRisk_SCOPE_RISK_ROUTE
		for _, key := range keys {
			if key.GetTargetSelector() != "" {
				risk = commonv1.ScopeRisk_SCOPE_RISK_EXACT
				break
			}
		}
		return trustedProposal{
			intent: trustedIntent, scope: scope, evidenceRefs: cluster.eventIDs,
			scopeRisk: risk, evidence: commonv1.EvidenceClass_EVIDENCE_CLASS_CRS_MAPPED,
		}, nil
	case commonv1.ProposalKind_PROPOSAL_KIND_SHAPE:
		if cluster.reason != commonv1.TriageReason_TRIAGE_REASON_SUSPECTED_MISS.String() {
			return trustedProposal{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("shape proposal requires suspected miss cluster"))
		}
		if err := validateShapeAgainstCluster(intent.GetShapeSource(), cluster); err != nil {
			return trustedProposal{}, err
		}
		return trustedProposal{
			intent: trustedIntent, scope: scope, evidenceRefs: cluster.eventIDs,
			scopeRisk: commonv1.ScopeRisk_SCOPE_RISK_EXACT,
			evidence:  commonv1.EvidenceClass_EVIDENCE_CLASS_INTEL,
		}, nil
	default:
		return trustedProposal{}, connect.NewError(connect.CodeInvalidArgument, errors.New("proposal intent kind is required"))
	}
}

func loadProposalCluster(ctx context.Context, pool *pgxpool.Pool, clusterID string) (proposalCluster, error) {
	var cluster proposalCluster
	var rawIDs []byte
	err := pool.QueryRow(ctx, `SELECT asset_id, route_template, method, reason, event_ids
		FROM triage_clusters WHERE cluster_id=$1`, clusterID).
		Scan(&cluster.assetID, &cluster.route, &cluster.method, &cluster.reason, &rawIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return proposalCluster{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("proposal cluster not found"))
	}
	if err != nil {
		return proposalCluster{}, err
	}
	if err := json.Unmarshal(rawIDs, &cluster.eventIDs); err != nil || len(cluster.eventIDs) == 0 {
		return proposalCluster{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("proposal cluster has no pinned events"))
	}
	rows, err := pool.Query(ctx, `SELECT event_id, payload FROM events
		WHERE event_id = ANY($1) AND asset_id=$2 ORDER BY event_id`, cluster.eventIDs, cluster.assetID)
	if err != nil {
		return proposalCluster{}, err
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var eventID string
		var raw []byte
		if err := rows.Scan(&eventID, &raw); err != nil {
			return proposalCluster{}, err
		}
		var event eventv1.Event
		if err := protojson.Unmarshal(raw, &event); err != nil {
			return proposalCluster{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cluster event %s is invalid", eventID))
		}
		if event.GetAssetId() != cluster.assetID {
			return proposalCluster{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("cluster event asset mismatch"))
		}
		found[eventID] = true
		cluster.events = append(cluster.events, &event)
	}
	if err := rows.Err(); err != nil {
		return proposalCluster{}, err
	}
	for _, id := range cluster.eventIDs {
		if !found[id] {
			return proposalCluster{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("cluster pinned event is missing"))
		}
	}
	return cluster, nil
}

func deriveClusterRoute(intent *governv1.ProposalIntent, cluster proposalCluster) error {
	if intent.GetRouteTemplate() != "" && intent.GetRouteTemplate() != cluster.route {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("proposal route does not match cluster"))
	}
	intent.RouteTemplate = cluster.route
	if len(intent.GetMethods()) > 0 {
		if len(intent.GetMethods()) != 1 || !strings.EqualFold(intent.GetMethods()[0], cluster.method) {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("proposal methods do not match cluster"))
		}
	}
	intent.Methods = []string{strings.ToUpper(cluster.method)}
	return nil
}

func trustedPolicyKeys(asserted []*commonv1.DetectionKey, events []*eventv1.Event) ([]*commonv1.DetectionKey, error) {
	if len(asserted) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("policy intent requires detection_keys"))
	}
	var available []*commonv1.DetectionKey
	for _, event := range events {
		for _, detection := range event.GetDetections() {
			if detection.GetKey() != nil && detection.GetKey().GetRuleId() != "" {
				available = append(available, detection.GetKey())
			}
		}
	}
	selected := make([]*commonv1.DetectionKey, 0, len(asserted))
	seen := map[string]bool{}
	for _, claim := range asserted {
		if claim == nil || strings.TrimSpace(claim.GetRuleId()) == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("detection key rule_id is required"))
		}
		var matches []*commonv1.DetectionKey
		for _, candidate := range available {
			if detectionKeyAssertionMatches(claim, candidate) {
				matches = append(matches, candidate)
			}
		}
		if len(matches) != 1 {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("detection key is absent or ambiguous in pinned cluster"))
		}
		raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(matches[0])
		if err != nil {
			return nil, err
		}
		key := string(raw)
		if !seen[key] {
			seen[key] = true
			selected = append(selected, proto.Clone(matches[0]).(*commonv1.DetectionKey))
		}
	}
	return selected, nil
}

func detectionKeyAssertionMatches(asserted, trusted *commonv1.DetectionKey) bool {
	if asserted.GetRuleId() != trusted.GetRuleId() {
		return false
	}
	return (asserted.GetDetectorId() == "" || asserted.GetDetectorId() == trusted.GetDetectorId()) &&
		(asserted.GetDetectorVersion() == "" || asserted.GetDetectorVersion() == trusted.GetDetectorVersion()) &&
		(asserted.GetDetectorManifestDigest() == "" || asserted.GetDetectorManifestDigest() == trusted.GetDetectorManifestDigest()) &&
		(asserted.GetPhase() == "" || asserted.GetPhase() == trusted.GetPhase()) &&
		(asserted.GetTargetLocation() == commonv1.InspectionSurface_INSPECTION_SURFACE_UNSPECIFIED || asserted.GetTargetLocation() == trusted.GetTargetLocation()) &&
		(asserted.GetTargetSelector() == "" || asserted.GetTargetSelector() == trusted.GetTargetSelector()) &&
		(asserted.GetNormalizationProfileDigest() == "" || asserted.GetNormalizationProfileDigest() == trusted.GetNormalizationProfileDigest())
}

func validateShapeAgainstCluster(shape *artifactv1.ShapeSource, cluster proposalCluster) error {
	if shape == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("shape_source is required"))
	}
	if len(shape.GetMethods()) != 1 || !strings.EqualFold(shape.GetMethods()[0], cluster.method) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("shape methods do not match cluster"))
	}
	if shape.GetRouteTemplate() != "" && shape.GetRouteTemplate() != cluster.route {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("shape route does not match cluster"))
	}
	if shape.GetPathPrefix() != "" && !strings.HasPrefix(cluster.route, shape.GetPathPrefix()) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("shape prefix does not cover cluster route"))
	}
	selectors := map[string]bool{}
	for _, event := range cluster.events {
		if http := event.GetHttp(); http != nil {
			query, _ := url.ParseQuery(http.GetQueryRedacted())
			for name := range query {
				selectors["query."+name] = true
			}
		}
		for _, detection := range event.GetDetections() {
			if selector := detection.GetKey().GetTargetSelector(); selector != "" {
				selectors[selector] = true
			}
		}
	}
	for _, constraint := range shape.GetConstraints() {
		if constraint == nil || !selectors[constraint.GetSelector()] {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("shape selector is absent from pinned cluster"))
		}
	}
	return nil
}
