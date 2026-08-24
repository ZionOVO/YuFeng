package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"

	"yufeng/lib/kernel"
)

// ClusterIdentity 是研判聚类身份。覆盖度与时间窗不得进入本函数。
func ClusterIdentity(assetID, method, routeTemplate, keyOrMiss string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(assetID))
	b.WriteByte('\n')
	b.WriteString(strings.ToUpper(strings.TrimSpace(method)))
	b.WriteByte('\n')
	b.WriteString(strings.TrimSpace(routeTemplate))
	b.WriteByte('\n')
	b.WriteString(strings.TrimSpace(keyOrMiss))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// RouteTemplate 把路径收成路由模板：数字段与 UUID 换成 {id}。
func RouteTemplate(path string) string {
	if path == "" {
		return "/"
	}
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == "" {
			continue
		}
		if isTemplateID(p) {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

func isTemplateID(s string) bool {
	if s == "" {
		return false
	}
	digit := true
	for _, r := range s {
		if !unicode.IsDigit(r) {
			digit = false
			break
		}
	}
	if digit {
		return true
	}
	if len(s) == 36 {
		for i, r := range s {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				if r != '-' {
					return false
				}
				continue
			}
			if !unicode.Is(unicode.ASCII_Hex_Digit, r) {
				return false
			}
		}
		return true
	}
	return false
}

// EventClusterKey 从事件抽出聚类键：检测键或漏检证据类型，不含覆盖度。
func EventClusterKey(ev *eventv1.Event) string {
	if ev == nil {
		return ""
	}
	if ev.TriageReason == commonv1.TriageReason_TRIAGE_REASON_SUSPECTED_MISS {
		return "miss"
	}
	for _, d := range ev.GetDetections() {
		if d == nil {
			continue
		}
		if k := d.GetKey(); k != nil {
			sel := strings.TrimSpace(k.TargetSelector)
			return "key:" + strings.TrimSpace(k.RuleId) + ":" + sel
		}
		if id := strings.TrimSpace(d.GetRuleId()); id != "" {
			return "key:" + id
		}
	}
	return ""
}

// ClusterOpen 判定是否应新开聚类：不存在，或空闲超过 ClusterIdle。
func ClusterOpen(exists bool, lastSeen, now time.Time) bool {
	if !exists {
		return true
	}
	return !lastSeen.IsZero() && now.Sub(lastSeen) >= kernel.ClusterIdle
}

// AppendRepresentatives 把 eventID 并入代表列表，最多 ClusterRepresentatives 条。
func AppendRepresentatives(ids []string, eventID string) []string {
	if eventID == "" {
		return ids
	}
	for _, id := range ids {
		if id == eventID {
			return ids
		}
	}
	if len(ids) >= kernel.ClusterRepresentatives {
		return ids
	}
	return append(ids, eventID)
}

func upsertTriageCluster(ctx context.Context, db dbTX, ev *eventv1.Event, now time.Time) (clusterID string, err error) {
	method, path := "", ""
	if h := ev.GetHttp(); h != nil {
		method = h.Method
		path = h.Path
	}
	route := RouteTemplate(path)
	identity := ClusterIdentity(ev.AssetId, method, route, EventClusterKey(ev))
	var existingID string
	var lastSeen time.Time
	var rawIDs []byte
	err = db.QueryRow(ctx, `SELECT cluster_id, last_seen_at, event_ids FROM triage_clusters
		WHERE asset_id=$1 AND identity_key=$2 AND closed_at IS NULL`, ev.AssetId, identity).
		Scan(&existingID, &lastSeen, &rawIDs)
	if err == nil && !ClusterOpen(true, lastSeen, now) {
		var ids []string
		_ = json.Unmarshal(rawIDs, &ids)
		ids = AppendRepresentatives(ids, ev.Id)
		blob, _ := json.Marshal(ids)
		if _, err := db.Exec(ctx, `UPDATE triage_clusters SET last_seen_at=$1, event_ids=$2::jsonb,
			version=version+CASE WHEN event_ids <> $2::jsonb THEN 1 ELSE 0 END WHERE cluster_id=$3`,
			now, blob, existingID); err != nil {
			return "", err
		}
		if _, err := db.Exec(ctx, `UPDATE events SET cluster_id=$1 WHERE event_id=$2`, existingID, ev.Id); err != nil {
			return "", err
		}
		return existingID, nil
	}
	if err == nil && ClusterOpen(true, lastSeen, now) {
		if _, err := db.Exec(ctx, `UPDATE triage_clusters SET closed_at=$1 WHERE cluster_id=$2`, now, existingID); err != nil {
			return "", err
		}
	}
	id, err := newID("clu")
	if err != nil {
		return "", err
	}
	ids, _ := json.Marshal([]string{ev.Id})
	if _, err := db.Exec(ctx, `INSERT INTO triage_clusters(cluster_id, asset_id, route_template, method, identity_key, reason, event_ids, representative, opened_at, last_seen_at)
		VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$9)`,
		id, ev.AssetId, route, method, identity, ev.TriageReason.String(), ids, ev.Id, now); err != nil {
		return "", err
	}
	if _, err := db.Exec(ctx, `UPDATE events SET cluster_id=$1 WHERE event_id=$2`, id, ev.Id); err != nil {
		return "", err
	}
	return id, nil
}

func pendingTriageCluster(ctx context.Context, db dbTX, clusterID string) (bool, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM agent_instructions
		WHERE kind=$1 AND payload_ref=$2 AND status IN ('pending','leased')`, instructionTriage, clusterID).Scan(&n)
	return n > 0, err
}
