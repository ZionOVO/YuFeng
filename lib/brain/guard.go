package brain

import (
	"context"

	"yufeng/lib/kernel"
)

// GuardSnapshot 是一个守护窗口的计数快照。
type GuardSnapshot struct {
	Requests, Blocks, Denies, Upstream5xx, LatencyMicros, LatencySamples, P99Micros int64
}

// GuardWindowBad 判定本窗是否坏窗：误报、拦截率跳变、5xx 倍率、p99 回归。
func GuardWindowBad(prev, cur GuardSnapshot, denyThreshold uint64) (bool, string) {
	var reasons []string
	if uint64(cur.Denies-prev.Denies) >= denyThreshold && denyThreshold > 0 {
		reasons = append(reasons, "unexpected_deny")
	}
	dReq := cur.Requests - prev.Requests
	dBlk := cur.Blocks - prev.Blocks
	if dReq > 0 && dBlk > 0 && float64(dBlk)/float64(dReq) > badWindowBlockRateJump {
		reasons = append(reasons, "block_rate_jump")
	}
	d5 := cur.Upstream5xx - prev.Upstream5xx
	if dReq > 0 && prev.Requests > 0 {
		prevRate := float64(prev.Upstream5xx) / float64(prev.Requests)
		curRate := float64(d5) / float64(dReq)
		if curRate > prevRate*kernel.Guard5xxRateMultiple && curRate-prevRate >= kernel.Guard5xxAbsDelta {
			reasons = append(reasons, "upstream_5xx")
		}
	}
	if cur.P99Micros > 0 && prev.P99Micros > 0 {
		if float64(cur.P99Micros) > float64(prev.P99Micros)*(1+kernel.GuardP99RelGrowth) && cur.P99Micros-prev.P99Micros >= kernel.GuardP99AbsMicros {
			reasons = append(reasons, "p99_regression")
		}
	}
	if len(reasons) == 0 {
		return false, ""
	}
	out := reasons[0]
	for i := 1; i < len(reasons); i++ {
		out += "," + reasons[i]
	}
	return true, out
}

func ensureGuardBaseline(ctx context.Context, db dbTX, releaseID string) error {
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM release_guards WHERE release_id=$1`, releaseID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var reqs, blocks, denies int64
	_ = db.QueryRow(ctx, `SELECT COALESCE(SUM(requests_total),0), COALESCE(SUM(blocks_total),0) FROM release_counters WHERE release_id=$1`, releaseID).Scan(&reqs, &blocks)
	_ = db.QueryRow(ctx, `SELECT count(*) FROM deny_feedback WHERE release_id=$1`, releaseID).Scan(&denies)
	_, err := db.Exec(ctx, `INSERT INTO release_guards(release_id, requests_total, blocks_total, deny_total, consecutive_bad, updated_at)
		VALUES($1,$2,$3,$4,0,now()) ON CONFLICT DO NOTHING`, releaseID, reqs, blocks, denies)
	return err
}
