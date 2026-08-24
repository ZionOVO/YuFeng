package brain

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	"yufeng/lib/replay"
)

func writePropose(ctx context.Context, pool *pgxpool.Pool, createdBy string, msg *governv1.ProposeArtifactRequest, complete func(pgx.Tx, *governv1.ProposeArtifactResponse) error) (*governv1.ProposeArtifactResponse, error) {
	if msg.GetIntent() != nil {
		return writeProposeIntent(ctx, pool, createdBy, msg, complete)
	}
	if msg.Kind != artifactv1.Kind_KIND_RULE {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("only KIND_RULE is supported in L1"))
	}
	if msg.PayloadSchema != edgecore.RulePayloadSchema {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("payload_schema must be %s", edgecore.RulePayloadSchema))
	}
	rules, err := edgecore.ParseRules(msg.Payload)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if _, err := edgecore.NewRuleDetector("proposal", rules); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if msg.Scope == nil || len(msg.Scope.AssetIds) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scope.asset_ids is required"))
	}
	ttl := msg.Ttl.AsDuration()
	if ttl < 300*time.Second || ttl > 7*24*time.Hour {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("ttl must be between 300s and 604800s"))
	}
	artifact := &artifactv1.Artifact{
		Kind:          msg.Kind,
		Payload:       append([]byte(nil), msg.Payload...),
		PayloadSchema: msg.PayloadSchema,
		Scope:         proto.Clone(msg.Scope).(*artifactv1.Scope),
		Ttl:           durationpb.New(ttl),
		Supersedes:    msg.Supersedes,
		EvidenceRefs:  append([]string(nil), msg.EvidenceRefs...),
		CreatedBy:     createdBy,
	}
	releaseID, err := newID("rel")
	if err != nil {
		return nil, err
	}
	if _, err := kernel.NewDraft(releaseID, artifact, createdBy); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	artifactJSON, err := protojson.Marshal(artifact)
	if err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, supersedes, created_by)
	VALUES($1,'draft',$2::jsonb,$3,$4,$5)`, releaseID, string(artifactJSON), int64(ttl.Seconds()), msg.Supersedes, createdBy); err != nil {
		return nil, err
	}
	for _, assetID := range msg.Scope.AssetIds {
		if _, err := tx.Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, releaseID, assetID); err != nil {
			return nil, err
		}
	}
	if err := appendAuditTx(ctx, tx, "user", createdBy, "release.propose", "release", releaseID, map[string]any{"created_by": createdBy}); err != nil {
		return nil, auditFailedError(err)
	}
	resp := &governv1.ProposeArtifactResponse{
		ReleaseId: releaseID, State: commonv1.ReleaseState_RELEASE_STATE_DRAFT, Artifact: artifact,
	}
	if complete != nil {
		if err := complete(tx, resp); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return resp, nil
}

func writeProposeIntent(ctx context.Context, pool *pgxpool.Pool, createdBy string, msg *governv1.ProposeArtifactRequest, complete func(pgx.Tx, *governv1.ProposeArtifactResponse) error) (*governv1.ProposeArtifactResponse, error) {
	trusted, err := deriveTrustedProposal(ctx, pool, msg)
	if err != nil {
		return nil, err
	}
	return writeTrustedProposal(ctx, pool, createdBy, trusted, msg.Ttl, complete)
}

func writeTrustedProposal(ctx context.Context, pool *pgxpool.Pool, createdBy string, trusted trustedProposal, requestedTTL *durationpb.Duration, complete func(pgx.Tx, *governv1.ProposeArtifactResponse) error) (*governv1.ProposeArtifactResponse, error) {
	intent := trusted.intent
	kind := artifactv1.Kind_KIND_POLICY
	schema := edgecore.PolicyPayloadSchema
	if intent.GetKind() == commonv1.ProposalKind_PROPOSAL_KIND_SHAPE {
		kind = artifactv1.Kind_KIND_SHAPE
		schema = "shape/v1"
	}
	var mapper *artifactv1.TaxonomyMapper
	if len(trusted.scope.AssetIds) > 0 {
		mapper = loadAssetTaxonomyMapper(ctx, pool, trusted.scope.AssetIds[0])
	}
	payload, err := compileProposalPayload(intent, trusted.scope, mapper)
	if trusted.evidence == commonv1.EvidenceClass_EVIDENCE_CLASS_CRS_UNMAPPED && intent.GetKind() == commonv1.ProposalKind_PROPOSAL_KIND_POLICY {
		payload, err = compilePolicyPayload(intent, trusted.scope, intent.GetDetectionKeys())
	}
	if err != nil {
		return nil, err
	}
	ttl := kernel.TTLDefault
	if requestedTTL != nil && requestedTTL.AsDuration() > 0 {
		ttl = requestedTTL.AsDuration()
	}
	scope := trusted.scope
	scopeRisk, evidence := trusted.scopeRisk, trusted.evidence
	artifact := &artifactv1.Artifact{
		Kind: kind, Payload: payload, PayloadSchema: schema, Scope: scope,
		Ttl: durationpb.New(ttl), CreatedBy: createdBy,
		ScopeRisk: scopeRisk, EvidenceClass: evidence, EvidenceRefs: trusted.evidenceRefs,
	}
	releaseID, err := newID("rel")
	if err != nil {
		return nil, err
	}
	artifactJSON, err := protojson.Marshal(artifact)
	if err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, created_by, scope_risk, evidence_class, review_at, hard_expires_at, expiry_behavior)
		VALUES($1,'draft',$2::jsonb,$3,$4,$5,$6,now()+make_interval(secs=>$7),now()+make_interval(secs=>$8),'retire')`,
		releaseID, string(artifactJSON), int64(ttl.Seconds()), createdBy, scopeRiskDB(scopeRisk), evidenceClassDB(evidence),
		int64(kernel.ReviewDefault.Seconds()), int64(ttl.Seconds())); err != nil {
		return nil, err
	}
	if artifact.Scope != nil {
		for _, assetID := range artifact.Scope.AssetIds {
			if _, err := tx.Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, releaseID, assetID); err != nil {
				return nil, err
			}
		}
	}
	if err := appendAuditTx(ctx, tx, "user", createdBy, "release.propose", "release", releaseID, map[string]any{"created_by": createdBy}); err != nil {
		return nil, auditFailedError(err)
	}
	resp := &governv1.ProposeArtifactResponse{ReleaseId: releaseID, State: commonv1.ReleaseState_RELEASE_STATE_DRAFT, Artifact: artifact}
	if complete != nil {
		if err := complete(tx, resp); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return resp, nil
}

// compileProposalPayload 把提案意图编译成可回放、可装载的制品载荷。
// 检测键策略写成 PolicyCandidate；形状仍以意图为载荷，回放另走形状算子。
func compileProposalPayload(intent *governv1.ProposalIntent, scope *artifactv1.Scope, mapper *artifactv1.TaxonomyMapper) ([]byte, error) {
	if intent.GetKind() == commonv1.ProposalKind_PROPOSAL_KIND_SHAPE {
		src := intent.GetShapeSource()
		if err := edgecore.ValidateShapeSource(src); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return protojson.Marshal(src)
	}
	keys := filterAutoGovernKeys(intent.GetDetectionKeys(), mapper)
	if len(keys) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("policy intent requires auto-governable detection_keys"))
	}
	return compilePolicyPayload(intent, scope, keys)
}

func compilePolicyPayload(intent *governv1.ProposalIntent, scope *artifactv1.Scope, keys []*commonv1.DetectionKey) ([]byte, error) {
	deps := policyDependencies(keys)
	if deps.GetDetectorManifestDigest() == "" || deps.GetNormalizerProfileDigest() == "" || deps.GetMinEdgeVersion() == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("policy detection_keys require one complete dependency set"))
	}
	cand := &artifactv1.PolicyCandidate{
		Action: "block",
		Predicate: &artifactv1.PolicyPredicate{
			DetectionKeys:       keys,
			RequireMatchPresent: true,
		},
		Scope: &artifactv1.PolicyScope{
			RouteTemplate: intent.GetRouteTemplate(),
			Methods:       append([]string(nil), intent.GetMethods()...),
		},
		Dependencies: deps,
	}
	if scope != nil && len(scope.AssetIds) > 0 {
		cand.Scope.AssetId = scope.AssetIds[0]
	}
	return protojson.Marshal(cand)
}

func policyDependencies(keys []*commonv1.DetectionKey) *artifactv1.PolicyDependencies {
	deps := &artifactv1.PolicyDependencies{MinEdgeVersion: kernel.MinimumEdgeVersion}
	manifestSet := false
	profileSet := false
	for _, key := range keys {
		if key == nil {
			continue
		}
		if !manifestSet {
			deps.DetectorManifestDigest = key.GetDetectorManifestDigest()
			manifestSet = true
		} else if deps.DetectorManifestDigest != key.GetDetectorManifestDigest() {
			deps.DetectorManifestDigest = ""
		}
		if !profileSet {
			deps.NormalizerProfileDigest = key.GetNormalizationProfileDigest()
			profileSet = true
		} else if deps.NormalizerProfileDigest != key.GetNormalizationProfileDigest() {
			deps.NormalizerProfileDigest = ""
		}
	}
	return deps
}

func filterAutoGovernKeys(in []*commonv1.DetectionKey, mapper *artifactv1.TaxonomyMapper) []*commonv1.DetectionKey {
	var keys []*commonv1.DetectionKey
	for _, k := range in {
		if !edgecore.AutoGovernable(k, mapper) {
			continue
		}
		keys = append(keys, k)
	}
	return keys
}

func loadAssetTaxonomyMapper(ctx context.Context, pool *pgxpool.Pool, assetID string) *artifactv1.TaxonomyMapper {
	if assetID == "" {
		return nil
	}
	var env []byte
	if err := pool.QueryRow(ctx, `SELECT envelope FROM asset_generations WHERE asset_id=$1 AND signed ORDER BY generation_seq DESC LIMIT 1`, assetID).Scan(&env); err != nil {
		return nil
	}
	var gen artifactv1.AssetGeneration
	if err := protojson.Unmarshal(env, &gen); err != nil {
		return nil
	}
	for _, m := range gen.Members {
		if m == nil || m.Artifact == nil || m.Artifact.Kind != artifactv1.Kind_KIND_TAXONOMY_MAPPER {
			continue
		}
		var mapper artifactv1.TaxonomyMapper
		if err := protojson.Unmarshal(m.Artifact.Payload, &mapper); err != nil {
			continue
		}
		return &mapper
	}
	return nil
}

func intentRiskClass(intent *governv1.ProposalIntent) (commonv1.ScopeRisk, commonv1.EvidenceClass) {
	if intent.GetKind() == commonv1.ProposalKind_PROPOSAL_KIND_SHAPE {
		return commonv1.ScopeRisk_SCOPE_RISK_EXACT, commonv1.EvidenceClass_EVIDENCE_CLASS_INTEL
	}
	return commonv1.ScopeRisk_SCOPE_RISK_EXACT, commonv1.EvidenceClass_EVIDENCE_CLASS_CRS_MAPPED
}

func scopeRiskDB(v commonv1.ScopeRisk) string {
	switch v {
	case commonv1.ScopeRisk_SCOPE_RISK_ROUTE:
		return "route"
	case commonv1.ScopeRisk_SCOPE_RISK_PREFIX:
		return "prefix"
	case commonv1.ScopeRisk_SCOPE_RISK_ASSET_WIDE:
		return "asset_wide"
	case commonv1.ScopeRisk_SCOPE_RISK_CLASS_ONLY:
		return "class_only"
	default:
		return "exact"
	}
}

func evidenceClassDB(v commonv1.EvidenceClass) string {
	switch v {
	case commonv1.EvidenceClass_EVIDENCE_CLASS_CRS_UNMAPPED:
		return "crs_unmapped"
	case commonv1.EvidenceClass_EVIDENCE_CLASS_HUMAN:
		return "human"
	case commonv1.EvidenceClass_EVIDENCE_CLASS_REPLAY:
		return "replay"
	case commonv1.EvidenceClass_EVIDENCE_CLASS_INTEL:
		return "intel"
	case commonv1.EvidenceClass_EVIDENCE_CLASS_MODEL:
		return "model"
	default:
		return "crs_mapped"
	}
}

func writeGate(ctx context.Context, pool *pgxpool.Pool, key ed25519.PrivateKey, actorType, actorID, releaseID string, artifactSigner kernel.Signer, complete func(pgx.Tx, *governv1.GateArtifactResponse) error) (*governv1.GateArtifactResponse, error) {
	draft, err := loadDraft(ctx, pool, releaseID)
	if err != nil {
		return nil, err
	}
	assetID := "builtin-corpus"
	if draft.Envelope.Scope != nil && len(draft.Envelope.Scope.AssetIds) > 0 {
		assetID = draft.Envelope.Scope.AssetIds[0]
	}
	report, err := replay.Run(ctx, draft.Envelope, replay.BuiltinCorpus(assetID))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	var result kernel.GateResult
	if artifactSigner != nil {
		result, err = draft.GateWithSigner(report, artifactSigner)
	} else {
		result, err = draft.Gate(report, key)
	}
	if err != nil {
		return nil, err
	}
	resp := &governv1.GateArtifactResponse{
		ReleaseId: releaseID, State: commonv1.ReleaseState_RELEASE_STATE_DRAFT,
		ReplayReport: result.Report, Artifact: draft.Envelope,
	}
	if !result.Passed {
		if complete != nil {
			if err := withTx(ctx, pool, func(tx pgx.Tx) error { return complete(tx, resp) }); err != nil {
				return nil, err
			}
		}
		return resp, nil
	}
	resp.State = commonv1.ReleaseState_RELEASE_STATE_SIGNED
	resp.ReplayReport = report
	resp.Artifact = result.Signed.Envelope
	if err := commitReleaseChange(ctx, pool, releaseWrite{
		rel: result.Signed, actorType: actorType, actorID: actorID, action: "release.gate",
		details: map[string]any{"corpus_ref": report.CorpusRef, "passed": true},
		key:     key, signer: artifactSigner, complete: func(tx pgx.Tx) error {
			if complete == nil {
				return nil
			}
			return complete(tx, resp)
		},
	}); err != nil {
		return nil, auditFailedError(err)
	}
	return resp, nil
}

func writeStartShadow(ctx context.Context, pool *pgxpool.Pool, actorType, actorID, releaseID string, key ed25519.PrivateKey, signer kernel.Signer, complete func(pgx.Tx, *kernel.Shadow) error) (*kernel.Shadow, error) {
	rel, err := loadRelease(ctx, pool, releaseID)
	if err != nil {
		return nil, err
	}
	signed, ok := rel.(*kernel.Signed)
	if !ok {
		return nil, gateConflict(rel.State())
	}
	shadow := signed.StartShadow()
	if err := commitReleaseChange(ctx, pool, releaseWrite{
		rel: shadow, feed: true, actorType: actorType, actorID: actorID, action: "release.start_shadow",
		key: key, signer: signer, complete: func(tx pgx.Tx) error {
			if complete == nil {
				return nil
			}
			return complete(tx, shadow)
		},
	}); err != nil {
		return nil, auditFailedError(err)
	}
	return shadow, nil
}

func releaseAssetIDs(ctx context.Context, pool *pgxpool.Pool, releaseID string) ([]string, error) {
	seen := map[string]bool{}
	rel, err := loadRelease(ctx, pool, releaseID)
	if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
		return nil, err
	}
	if rel != nil {
		if a := rel.Artifact(); a != nil && a.Scope != nil {
			for _, id := range a.Scope.AssetIds {
				if id != "" {
					seen[id] = true
				}
			}
		}
	}
	rows, err := pool.Query(ctx, `SELECT asset_id FROM release_assets WHERE release_id=$1`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			seen[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out, nil
}
