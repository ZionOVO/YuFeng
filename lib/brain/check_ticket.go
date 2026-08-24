package brain

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
)

const (
	checkTicketReady       = "ready"
	checkTicketQuarantined = "quarantined"

	quarantineGenerationMissing     = "generation_missing"
	quarantineGenerationMismatch    = "generation_mismatch"
	quarantineEvidenceDigestMissing = "evidence_digest_missing"
	quarantineForwardPolicyMissing  = "forward_policy_missing"
	quarantineProjectionInvalid     = "projection_invalid"
)

type frozenCheckTicket struct {
	Ticket           *eventv1.CheckTicket
	Digest           string
	Forward          commonv1.ForwardPolicyKind
	Status           string
	QuarantineReason string
}

func freezeCheckTicket(ctx context.Context, db dbTX, unitID string, event *eventv1.Event) (frozenCheckTicket, error) {
	frozen, err := buildCheckTicket(ctx, db, unitID, event)
	if err != nil {
		return frozenCheckTicket{}, err
	}
	ticketJSON := []byte(`{}`)
	if frozen.Ticket != nil {
		ticketJSON, err = protojson.Marshal(frozen.Ticket)
		if err != nil {
			return frozenCheckTicket{}, err
		}
	}
	_, err = db.Exec(ctx, `INSERT INTO check_tickets(
		event_id, generation_id, generation_seq, status, ticket, ticket_digest, forward_policy, quarantine_reason)
		VALUES($1,$2,$3,$4,$5::jsonb,$6,$7,$8)`,
		event.GetId(), event.GetGenerationId(), event.GetGenerationSeq(), frozen.Status, ticketJSON,
		frozen.Digest, frozen.Forward.String(), frozen.QuarantineReason)
	if err != nil {
		return frozenCheckTicket{}, err
	}
	return frozen, nil
}

func buildCheckTicket(ctx context.Context, db dbTX, unitID string, event *eventv1.Event) (frozenCheckTicket, error) {
	if event == nil || strings.TrimSpace(event.GetGenerationId()) == "" || event.GetGenerationSeq() < 1 {
		return quarantinedTicket(quarantineGenerationMissing), nil
	}
	var assetID string
	var generationSeq int64
	var raw []byte
	var signed bool
	err := db.QueryRow(ctx, `SELECT asset_id, generation_seq, envelope, signed
		FROM asset_generations WHERE generation_id=$1`, event.GetGenerationId()).Scan(&assetID, &generationSeq, &raw, &signed)
	if errors.Is(err, pgx.ErrNoRows) {
		return quarantinedTicket(quarantineGenerationMissing), nil
	}
	if err != nil {
		return frozenCheckTicket{}, err
	}
	if !signed || assetID != event.GetAssetId() || generationSeq != event.GetGenerationSeq() {
		return quarantinedTicket(quarantineGenerationMismatch), nil
	}
	var generation artifactv1.AssetGeneration
	if err := protojson.Unmarshal(raw, &generation); err != nil || generation.GetGenerationId() != event.GetGenerationId() || generation.GetAssetId() != event.GetAssetId() || generation.GetGenerationSeq() != event.GetGenerationSeq() {
		return quarantinedTicket(quarantineGenerationMismatch), nil
	}
	digest, forward, reason := ticketGenerationMaterial(&generation)
	if reason != "" {
		return quarantinedTicket(reason), nil
	}
	evidence, err := edgecore.ProjectEventEvidence(event, digest)
	if err != nil {
		return quarantinedTicket(quarantineProjectionInvalid), nil
	}
	ticket := &eventv1.CheckTicket{
		EventId: event.GetId(), AssetId: event.GetAssetId(), UnitId: unitID,
		GenerationId: event.GetGenerationId(), GenerationSeq: event.GetGenerationSeq(),
		Posture: event.GetIngressPosture(), Coverage: cloneCoverage(event.GetCoverage()),
		Detections: cloneDetections(event.GetDetections()), Evidence: evidence,
		RouteTemplate: event.GetHttp().GetPath(), Method: event.GetHttp().GetMethod(), Forward: forward,
	}
	ticketDigest, err := digestCheckTicket(ticket)
	if err != nil {
		return frozenCheckTicket{}, err
	}
	return frozenCheckTicket{Ticket: ticket, Digest: ticketDigest, Forward: forward, Status: checkTicketReady}, nil
}

func quarantinedTicket(reason string) frozenCheckTicket {
	return frozenCheckTicket{Status: checkTicketQuarantined, QuarantineReason: reason}
}

func ticketGenerationMaterial(generation *artifactv1.AssetGeneration) (*artifactv1.EvidenceDigest, commonv1.ForwardPolicyKind, string) {
	var digest *artifactv1.EvidenceDigest
	var forward commonv1.ForwardPolicyKind
	for _, member := range generation.GetMembers() {
		artifact := member.GetArtifact()
		if artifact == nil {
			continue
		}
		switch artifact.GetKind() {
		case artifactv1.Kind_KIND_EVIDENCE_DIGEST:
			if digest != nil || artifact.GetPayloadSchema() != edgecore.EvidenceDigestSchema {
				return nil, 0, quarantineProjectionInvalid
			}
			var value artifactv1.EvidenceDigest
			if err := protojson.Unmarshal(artifact.GetPayload(), &value); err != nil || !validEvidenceDigest(&value) {
				return nil, 0, quarantineProjectionInvalid
			}
			digest = &value
		case artifactv1.Kind_KIND_FORWARD_POLICY:
			if forward != 0 || artifact.GetPayloadSchema() != edgecore.ForwardPolicySchema {
				return nil, 0, quarantineProjectionInvalid
			}
			var value artifactv1.ForwardPolicy
			if err := protojson.Unmarshal(artifact.GetPayload(), &value); err != nil || !validForwardPolicy(value.GetKind()) {
				return nil, 0, quarantineProjectionInvalid
			}
			forward = value.GetKind()
		}
	}
	if digest == nil {
		return nil, 0, quarantineEvidenceDigestMissing
	}
	if forward == commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_UNSPECIFIED {
		return nil, 0, quarantineForwardPolicyMissing
	}
	return digest, forward, ""
}

func validEvidenceDigest(digest *artifactv1.EvidenceDigest) bool {
	if digest == nil || digest.GetMaxSpanBytes() < 1 || len(digest.GetFields()) == 0 {
		return false
	}
	switch digest.GetAlgorithm() {
	case commonv1.EvidenceDigestAlgorithm_EVIDENCE_DIGEST_ALGORITHM_SPAN_SHA256,
		commonv1.EvidenceDigestAlgorithm_EVIDENCE_DIGEST_ALGORITHM_NGRAM3_HASH,
		commonv1.EvidenceDigestAlgorithm_EVIDENCE_DIGEST_ALGORITHM_CHARSET_HIST:
	default:
		return false
	}
	allowed := map[string]bool{"method": true, "route_template": true, "selector": true, "span_hash": true, "charset_class": true}
	seen := map[string]bool{}
	for _, field := range digest.GetFields() {
		field = strings.ToLower(strings.TrimSpace(field))
		if !allowed[field] || seen[field] {
			return false
		}
		seen[field] = true
	}
	return true
}

func validForwardPolicy(forward commonv1.ForwardPolicyKind) bool {
	switch forward {
	case commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_NONE,
		commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_WORKER_SCORE,
		commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_MODELGW_SCORE,
		commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_AGENT_INVESTIGATE:
		return true
	default:
		return false
	}
}

func cloneCoverage(items []*commonv1.InspectionCoverage) []*commonv1.InspectionCoverage {
	out := make([]*commonv1.InspectionCoverage, 0, len(items))
	for _, item := range items {
		if item != nil {
			out = append(out, proto.Clone(item).(*commonv1.InspectionCoverage))
		}
	}
	return out
}

func cloneDetections(items []*eventv1.Detection) []*eventv1.Detection {
	out := make([]*eventv1.Detection, 0, len(items))
	for _, item := range items {
		if item != nil {
			out = append(out, proto.Clone(item).(*eventv1.Detection))
		}
	}
	return out
}

func digestCheckTicket(ticket *eventv1.CheckTicket) (string, error) {
	return kernel.CheckTicketDigest(ticket)
}
