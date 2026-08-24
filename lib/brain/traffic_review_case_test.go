package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	eventv1 "yufeng/proto/gen/eventv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

func TestReviewCandidateRejectsRawIdentifiersAndUnexpectedProjectionFields(t *testing.T) {
	now := time.Now()
	base := func() *telemetryv1.ReviewCandidate {
		return &telemetryv1.ReviewCandidate{
			CandidateId: "candidate", WindowId: "window", AssetId: "asset", OccurredAt: timestamppb.New(now),
			Method: "GET", RouteTemplate: "/users/:number", Baseline: true,
			EvidenceExpiresAt: timestamppb.New(now.Add(time.Hour)),
			Evidence:          &eventv1.EvidenceProjection{Fields: map[string]string{"method": "GET", "route_template": "/users/:number"}},
			ReviewMode:        artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_REDACTED_CASES,
		}
	}
	for name, mutate := range map[string]func(*telemetryv1.ReviewCandidate){
		"raw identifier route": func(candidate *telemetryv1.ReviewCandidate) {
			candidate.RouteTemplate = "/users/12345"
			candidate.Evidence.Fields["route_template"] = candidate.RouteTemplate
		},
		"unexpected projection field": func(candidate *telemetryv1.ReviewCandidate) { candidate.Evidence.Fields["authorization"] = "secret" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base()
			mutate(candidate)
			_, code, _, err := (&TelemetryServer{}).ingestReviewCandidate(context.Background(), "unit", candidate)
			if err != nil || code != "invalid_candidate" {
				t.Fatalf("code=%s err=%v", code, err)
			}
		})
	}
}

func TestReviewCandidatesUseDurableOutboxAndBoundedRepresentatives(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	suffix := newTestSuffix()
	assetID, unitID := "traffic-asset-"+suffix, "traffic-unit-"+suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO units(unit_id, kind, token_hash) VALUES($1,'edge',$2)`, unitID, "hash-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_assets(unit_id, asset_id) VALUES($1,$2)`, unitID, assetID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM traffic.review_case_outbox WHERE candidate_json->>'asset_id'=$1`, assetID)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM traffic.review_candidates WHERE asset_id=$1`, assetID)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM traffic.traffic_windows WHERE asset_id=$1`, assetID)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM traffic.traffic_window_receipts WHERE window_id IN ($1,$2,$3)`,
			"window-"+suffix+"-0", "window-"+suffix+"-1", "window-stable-"+suffix)
	})

	server := NewTelemetryServer(st.Pool(), nil, nil, "jarvis-1", st.TrafficPool())
	now := time.Now().UTC()
	windowStart := now.Truncate(5 * time.Minute)
	for index := range 2 {
		window := &telemetryv1.TrafficWindow{
			WindowId: fmt.Sprintf("window-%s-%d", suffix, index), UnitId: unitID, AssetId: assetID,
			WindowStart:  timestamppb.New(windowStart.Add(time.Duration(index) * 5 * time.Minute)),
			WindowEnd:    timestamppb.New(windowStart.Add(time.Duration(index+1) * 5 * time.Minute)),
			PolicyDigest: "sha256:policy", Other: &telemetryv1.TrafficRouteCell{},
			ReviewMode: artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL,
		}
		if outcome, _, _, err := server.ingestTrafficWindow(ctx, unitID, window); err != nil || outcome != "accepted" {
			t.Fatalf("candidate window %d outcome=%s err=%v", index, outcome, err)
		}
	}
	makeCandidate := func(sequence int, baseline bool) *telemetryv1.ReviewCandidate {
		risk := float64(90 - sequence)
		reasons := []string{"unmitigated"}
		if baseline {
			risk, reasons = 0, nil
		}
		windowIndex := sequence / 4
		occurredAt := windowStart.Add(time.Duration(windowIndex)*5*time.Minute + time.Duration(sequence%4)*time.Second)
		return &telemetryv1.ReviewCandidate{
			CandidateId: fmt.Sprintf("candidate-%s-%d", suffix, sequence), WindowId: fmt.Sprintf("window-%s-%d", suffix, windowIndex),
			UnitId: unitID, AssetId: assetID, OccurredAt: timestamppb.New(occurredAt),
			Method: "POST", RouteTemplate: "/login", RiskScore: risk, RiskReasons: reasons,
			Evidence:       &eventv1.EvidenceProjection{Fields: map[string]string{"method": "POST", "route_template": "/login"}},
			EvidenceHandle: fmt.Sprintf("handle-%s-%d", suffix, sequence), EvidenceDigest: fmt.Sprintf("sha256:%064d", sequence),
			EvidenceExpiresAt: timestamppb.New(now.Add(12 * time.Hour)), Baseline: baseline,
			ReviewMode: artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL,
		}
	}

	for sequence, baseline := range []bool{true, false, true, false, false, false} {
		outcome, code, message, err := server.ingestReviewCandidate(ctx, unitID, makeCandidate(sequence, baseline))
		if err != nil || outcome != "accepted" {
			t.Fatalf("candidate %d outcome=%s code=%s message=%s err=%v", sequence, outcome, code, message, err)
		}
	}
	duplicate := makeCandidate(1, false)
	if outcome, _, _, err := server.ingestReviewCandidate(ctx, unitID, duplicate); err != nil || outcome != "deduped" {
		t.Fatalf("stable candidate duplicate outcome=%s err=%v", outcome, err)
	}
	duplicate.RouteTemplate = "/changed"
	duplicate.Evidence.Fields["route_template"] = duplicate.RouteTemplate
	if outcome, code, _, err := server.ingestReviewCandidate(ctx, unitID, duplicate); err != nil || outcome != "" || code != "stable_id_changed" {
		t.Fatalf("changed stable candidate outcome=%s code=%s err=%v", outcome, code, err)
	}
	for sequence := 6; sequence < 8; sequence++ {
		if outcome, _, _, err := server.ingestReviewCandidate(ctx, unitID, makeCandidate(sequence, false)); err != nil || outcome != "accepted" {
			t.Fatalf("candidate %d outcome=%s err=%v", sequence, outcome, err)
		}
	}
	overflow := makeCandidate(7, false)
	overflow.CandidateId = "candidate-overflow-" + suffix
	if outcome, code, _, err := server.ingestReviewCandidate(ctx, unitID, overflow); err != nil || outcome != "" || code != "candidate_limit_exceeded" {
		t.Fatalf("candidate overflow outcome=%s code=%s err=%v", outcome, code, err)
	}
	window := &telemetryv1.TrafficWindow{
		WindowId: "window-stable-" + suffix, UnitId: unitID, AssetId: assetID,
		WindowStart: timestamppb.New(now.Truncate(5 * time.Minute)), WindowEnd: timestamppb.New(now.Truncate(5 * time.Minute).Add(5 * time.Minute)),
		PolicyDigest: "sha256:policy", RequestCount: 1, Other: &telemetryv1.TrafficRouteCell{RequestCount: 1},
		ReviewMode: artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_STATISTICS_ONLY,
	}
	if outcome, _, _, err := server.ingestTrafficWindow(ctx, unitID, window); err != nil || outcome != "accepted" {
		t.Fatalf("stable window first outcome=%s err=%v", outcome, err)
	}
	if outcome, _, _, err := server.ingestTrafficWindow(ctx, unitID, window); err != nil || outcome != "deduped" {
		t.Fatalf("stable window duplicate outcome=%s err=%v", outcome, err)
	}
	window.RequestCount = 2
	window.Other.RequestCount = 2
	if outcome, code, _, err := server.ingestTrafficWindow(ctx, unitID, window); err != nil || outcome != "" || code != "stable_id_changed" {
		t.Fatalf("changed stable window outcome=%s code=%s err=%v", outcome, code, err)
	}
	var diagnostic string
	if err := st.Pool().QueryRow(ctx, `SELECT COALESCE(max(last_error),'') FROM traffic.review_case_outbox
		WHERE candidate_json->>'asset_id'=$1`, assetID).Scan(&diagnostic); err != nil {
		t.Fatal(err)
	}
	if diagnostic != "" {
		t.Fatalf("case outbox processing failed: %s", diagnostic)
	}
	var cases int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM investigation_cases WHERE asset_id=$1`, assetID).Scan(&cases); err != nil {
		t.Fatal(err)
	}
	if cases != 1 {
		t.Fatalf("baseline must not open a case and high-risk candidates must cluster: cases=%d", cases)
	}
	var representativesRaw []byte
	if err := st.Pool().QueryRow(ctx, `SELECT representatives FROM investigation_cases WHERE asset_id=$1`, assetID).Scan(&representativesRaw); err != nil {
		t.Fatal(err)
	}
	var representatives []map[string]any
	if err := json.Unmarshal(representativesRaw, &representatives); err != nil {
		t.Fatal(err)
	}
	var high, baseline int
	minHighRisk := 101.0
	for _, representative := range representatives {
		if value, _ := representative["baseline"].(bool); value {
			baseline++
		} else {
			high++
			if risk, _ := representative["risk_score"].(float64); risk < minHighRisk {
				minHighRisk = risk
			}
		}
	}
	if high != 3 || baseline != 2 || len(representatives) != 5 || minHighRisk < 86 {
		t.Fatalf("representatives high=%d baseline=%d total=%d min_high=%.0f want 3/2/5 and top risks", high, baseline, len(representatives), minHighRisk)
	}
	var pending int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM traffic.review_case_outbox
		WHERE candidate_json->>'asset_id'=$1 AND state<>'processed'`, assetID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		var lastError string
		_ = st.Pool().QueryRow(ctx, `SELECT last_error FROM traffic.review_case_outbox
			WHERE candidate_json->>'asset_id'=$1 AND state='pending' ORDER BY created_at LIMIT 1`, assetID).Scan(&lastError)
		t.Fatalf("durable case outbox pending=%d last_error=%s", pending, lastError)
	}
}

func TestTrafficCasePriorityCombinesRiskNoveltyCriticalityAndFeedback(t *testing.T) {
	high := priorityFromTrafficSignals(60, "p0", []string{"suspected_miss", "unmapped_detection", "inspection_incomplete"}, true, "")
	benign := priorityFromTrafficSignals(60, "p2", nil, false, "benign")
	if high != 96 {
		t.Fatalf("high priority=%d want 96", high)
	}
	if benign != 52 {
		t.Fatalf("benign priority=%d want 52", benign)
	}
}

func TestManagedAgentProfileDrivesDurableCaseDelegation(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	suffix := newTestSuffix()
	assetID := "delegation-asset-" + suffix
	caseID := "delegation-case-" + suffix
	profileID := "delegation-profile-" + suffix
	jarvisID := "delegation-jarvis-" + suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name) VALUES($1,$1)`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(case_id, module_id, asset_id, state, priority, title)
		VALUES($1,'traffic-interception',$2,'open',90,'test case')`, caseID, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO managed_agent_profiles(agent_id, display_name, tools, bindings, created_by)
		VALUES($1,'真实审查 Agent','["case.get","case.request_evidence","run.create"]',$2::jsonb,'operator')`,
		profileID, fmt.Sprintf(`[{"kind":"asset","id":%q}]`, assetID)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agents(agent_id, refresh_token_hash, role, public_key)
		VALUES($1,'x','orchestrator','registered-public-key')`, jarvisID); err != nil {
		t.Fatal(err)
	}
	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcilePendingCaseDelegations(ctx, st.Pool()); err != nil {
		t.Fatal(err)
	}
	agents := NewAgentServer(st.Pool(), "unused", signingKey)
	if err := processPendingCaseDelegations(ctx, st.Pool(), agents, jarvisID); err != nil {
		t.Fatal(err)
	}

	var assignedID, assignedName string
	var snapshot []byte
	if err := st.Pool().QueryRow(ctx, `SELECT assigned_agent_id, assigned_agent_display_name, agent_profile_snapshot
		FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&assignedID, &assignedName, &snapshot); err != nil {
		t.Fatal(err)
	}
	if assignedID != profileID || assignedName != "真实审查 Agent" || len(snapshot) == 0 {
		t.Fatalf("assigned id=%s name=%s snapshot=%s", assignedID, assignedName, snapshot)
	}
	var outboxState, token string
	if err := st.Pool().QueryRow(ctx, `SELECT o.state, i.capability_token
		FROM case_delegation_outbox o JOIN agent_instructions i ON i.payload_ref=o.case_id
		WHERE o.case_id=$1 AND i.kind='CASE_REVIEW'`, caseID).Scan(&outboxState, &token); err != nil {
		t.Fatal(err)
	}
	claims, err := kernel.VerifyCapabilityToken(token, signingKey.Public().(ed25519.PublicKey), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if outboxState != "dispatched" || !slices.Equal(claims.Tools, []string{"case.get", "case.request_evidence", "run.create"}) {
		t.Fatalf("outbox=%s tools=%v", outboxState, claims.Tools)
	}
	if !slices.Equal(claims.Bindings, []string{"asset:" + assetID, "case:" + caseID}) {
		t.Fatalf("bindings=%v", claims.Bindings)
	}
	if _, err := st.Pool().Exec(ctx, `DELETE FROM managed_agent_profiles WHERE agent_id=$1`, profileID); err != nil {
		t.Fatal(err)
	}
	var preserved string
	if err := st.Pool().QueryRow(ctx, `SELECT agent_profile_snapshot->>'agent_id' FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if preserved != profileID {
		t.Fatalf("deleted profile rewrote case snapshot: %s", preserved)
	}
	leaseToken, err := agents.signInstructionCapability(ctx, st.Pool(), jarvisID, instructionCaseReview, caseID)
	if err != nil {
		t.Fatal(err)
	}
	leaseClaims, err := kernel.VerifyCapabilityToken(leaseToken, signingKey.Public().(ed25519.PublicKey), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(leaseClaims.Tools, []string{"case.get", "case.request_evidence", "run.create"}) ||
		!slices.Equal(leaseClaims.Bindings, []string{"asset:" + assetID, "case:" + caseID}) {
		t.Fatalf("lease scope fell back after profile deletion: tools=%v bindings=%v", leaseClaims.Tools, leaseClaims.Bindings)
	}
}

func TestUnmatchedCaseWaitsForEnabledAssetBoundAgent(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	suffix := newTestSuffix()
	assetID := "unassigned-asset-" + suffix
	caseID := "unassigned-case-" + suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name) VALUES($1,$1)`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(case_id, module_id, asset_id, state, priority, title)
		VALUES($1,'traffic-interception',$2,'open',60,'waiting case')`, caseID, assetID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := reconcilePendingCaseDelegations(ctx, st.Pool()); err != nil {
			t.Fatal(err)
		}
	}
	var assigned string
	var recommendations, outboxRows int
	if err := st.Pool().QueryRow(ctx, `SELECT assigned_agent_id FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM case_activities WHERE case_id=$1 AND ref_id=$2`,
		caseID, caseAgentRequiredActivity).Scan(&recommendations); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM case_delegation_outbox WHERE case_id=$1`, caseID).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if assigned != "" || recommendations != 1 || outboxRows != 0 {
		t.Fatalf("assigned=%q recommendations=%d outbox=%d", assigned, recommendations, outboxRows)
	}
	profileID := "enabled-profile-" + suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO managed_agent_profiles(agent_id, display_name, tools, bindings, created_by)
		VALUES($1,'后加入审查 Agent','["case.get","case.request_evidence","run.create"]',$2::jsonb,'operator')`,
		profileID, fmt.Sprintf(`[{"kind":"asset","id":%q}]`, assetID)); err != nil {
		t.Fatal(err)
	}
	if err := reconcilePendingCaseDelegations(ctx, st.Pool()); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT assigned_agent_id FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if assigned != profileID {
		t.Fatalf("assigned=%q want %q", assigned, profileID)
	}
}
