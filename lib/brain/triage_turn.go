package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"

	eventv1 "yufeng/proto/gen/eventv1"
)

const (
	threadSourceTriage = "triage_cluster"
	bindingTurnPrefix  = "turn:"
)

// triageTurnSnapshot 是研判回合的不可变输入；只含字段级投影和可信键。
type triageTurnSnapshot struct {
	ClusterID      string            `json:"clusterId"`
	ClusterVersion int64             `json:"clusterVersion"`
	AssetID        string            `json:"assetId"`
	RouteTemplate  string            `json:"routeTemplate"`
	Method         string            `json:"method"`
	Reason         string            `json:"reason"`
	EventIDs       []string          `json:"eventIds"`
	Representative string            `json:"representative"`
	DetectionKeys  []json.RawMessage `json:"detectionKeys"`
	Selectors      []string          `json:"selectors"`
	Inferences     []map[string]any  `json:"inferences"`
}

func turnBinding(turnID string) string {
	return bindingTurnPrefix + turnID
}

func bindingAllowsTurn(bindings []string, turnID string) bool {
	want := turnBinding(turnID)
	for _, binding := range bindings {
		if binding == want {
			return true
		}
	}
	return false
}

func ensureTriageTurn(ctx context.Context, db dbTX, agentID, clusterID string) (string, error) {
	snapshot, err := buildTriageSnapshot(ctx, db, clusterID)
	if err != nil {
		return "", err
	}
	_, turnID, err := ensureAgentTurn(ctx, db, turnSeed{
		SourceKind: threadSourceTriage, SourceRef: clusterID, SubjectID: agentID,
		SourceVersion: snapshot.ClusterVersion,
		SourceCursor:  map[string]any{"clusterId": clusterID, "clusterVersion": snapshot.ClusterVersion},
		InputSnapshot: snapshot, BudgetID: "triage:" + clusterID,
		ContentRef: "triage-cluster:" + clusterID,
	})
	if err != nil {
		return "", err
	}
	return turnID, nil
}

func buildTriageSnapshot(ctx context.Context, db dbTX, clusterID string) (triageTurnSnapshot, error) {
	var snapshot triageTurnSnapshot
	var rawIDs []byte
	err := db.QueryRow(ctx, `SELECT cluster_id, version, asset_id, route_template, method, reason, event_ids, representative
		FROM triage_clusters WHERE cluster_id=$1`, clusterID).Scan(
		&snapshot.ClusterID, &snapshot.ClusterVersion, &snapshot.AssetID, &snapshot.RouteTemplate,
		&snapshot.Method, &snapshot.Reason, &rawIDs, &snapshot.Representative)
	if errors.Is(err, pgx.ErrNoRows) {
		return triageTurnSnapshot{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("triage cluster not found"))
	}
	if err != nil {
		return triageTurnSnapshot{}, err
	}
	if err := json.Unmarshal(rawIDs, &snapshot.EventIDs); err != nil || len(snapshot.EventIDs) == 0 {
		return triageTurnSnapshot{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("triage cluster has no pinned events"))
	}
	events, err := loadPinnedTriageEvents(ctx, db, snapshot.EventIDs, snapshot.AssetID)
	if err != nil {
		return triageTurnSnapshot{}, err
	}
	seenKeys := map[string]bool{}
	selectors := map[string]bool{}
	for _, event := range events {
		if h := event.GetHttp(); h != nil {
			query, _ := url.ParseQuery(h.GetQueryRedacted())
			for name := range query {
				selectors["query."+name] = true
			}
		}
		for _, detection := range event.GetDetections() {
			key := detection.GetKey()
			if key == nil || strings.TrimSpace(key.GetRuleId()) == "" {
				continue
			}
			raw, err := protojson.Marshal(key)
			if err != nil {
				return triageTurnSnapshot{}, err
			}
			if !seenKeys[string(raw)] {
				seenKeys[string(raw)] = true
				snapshot.DetectionKeys = append(snapshot.DetectionKeys, json.RawMessage(raw))
			}
			if selector := strings.TrimSpace(key.GetTargetSelector()); selector != "" {
				selectors[selector] = true
			}
		}
	}
	for selector := range selectors {
		snapshot.Selectors = append(snapshot.Selectors, selector)
	}
	sort.Strings(snapshot.Selectors)
	if snapshot.DetectionKeys == nil {
		snapshot.DetectionKeys = []json.RawMessage{}
	}
	if snapshot.Selectors == nil {
		snapshot.Selectors = []string{}
	}
	inferences, err := loadTriageInferenceProjection(ctx, db, snapshot.EventIDs)
	if err != nil {
		return triageTurnSnapshot{}, err
	}
	snapshot.Inferences = inferences
	return snapshot, nil
}

func loadPinnedTriageEvents(ctx context.Context, db dbTX, eventIDs []string, assetID string) ([]*eventv1.Event, error) {
	rows, err := db.Query(ctx, `SELECT event_id, payload FROM events WHERE event_id=ANY($1) AND asset_id=$2 ORDER BY event_id`, eventIDs, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]bool{}
	var events []*eventv1.Event
	for rows.Next() {
		var eventID string
		var raw []byte
		if err := rows.Scan(&eventID, &raw); err != nil {
			return nil, err
		}
		var event eventv1.Event
		if err := protojson.Unmarshal(raw, &event); err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("pinned triage event is invalid"))
		}
		found[eventID] = true
		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, eventID := range eventIDs {
		if !found[eventID] {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("pinned triage event is missing"))
		}
	}
	return events, nil
}

func loadTriageInferenceProjection(ctx context.Context, db dbTX, eventIDs []string) ([]map[string]any, error) {
	rows, err := db.Query(ctx, `SELECT event_id, model_group, model_type, model_version, threshold, score, attack_class, taxonomy_version
		FROM model_inferences WHERE event_id=ANY($1) ORDER BY recorded_at, inference_id`, eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var eventID, group, modelType, version, attackClass, taxonomy string
		var threshold, score float64
		if err := rows.Scan(&eventID, &group, &modelType, &version, &threshold, &score, &attackClass, &taxonomy); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"eventId": eventID, "modelGroup": group, "modelType": modelType, "modelVersion": version,
			"threshold": threshold, "score": score, "attackClass": attackClass, "taxonomyVersion": taxonomy,
		})
	}
	return out, rows.Err()
}

func loadTriageTurnSnapshot(ctx context.Context, db dbTX, turnID string) (triageTurnSnapshot, string, string, error) {
	var raw []byte
	var agentID, state string
	err := db.QueryRow(ctx, `SELECT t.input_snapshot, th.agent_id, t.state
		FROM agent_turns t JOIN agent_threads th ON th.thread_id=t.thread_id
		WHERE t.turn_id=$1 AND th.source_kind=$2`, turnID, threadSourceTriage).Scan(&raw, &agentID, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return triageTurnSnapshot{}, "", "", deniedObject()
	}
	if err != nil {
		return triageTurnSnapshot{}, "", "", err
	}
	var snapshot triageTurnSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return triageTurnSnapshot{}, "", "", err
	}
	return snapshot, agentID, state, nil
}

func triageTurnQueued(ctx context.Context, db dbTX, turnID string) (bool, error) {
	var count int
	err := db.QueryRow(ctx, `SELECT count(*) FROM agent_instructions WHERE kind=$1 AND payload_ref=$2`, instructionTriage, turnID).Scan(&count)
	return count > 0, err
}

func pendingTriageSource(ctx context.Context, db dbTX, clusterID string) (bool, error) {
	var count int
	err := db.QueryRow(ctx, `SELECT count(*) FROM agent_instructions i
		JOIN agent_turns t ON t.turn_id=i.turn_id
		JOIN agent_threads th ON th.thread_id=t.thread_id
		WHERE i.kind=$1 AND i.status IN ('pending','leased') AND th.source_kind=$2 AND th.source_ref=$3`,
		instructionTriage, threadSourceTriage, clusterID).Scan(&count)
	return count > 0, err
}
