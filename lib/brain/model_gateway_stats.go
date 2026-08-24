package brain

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	modelv1 "yufeng/proto/gen/modelv1"

	"yufeng/lib/kernel"
)

const (
	gatewayCallComplete = "complete"
	gatewayCallGenerate = "generate"
	gatewayCallProbe    = "probe"
)

// gatewayCall 是一次出网补全的记账输入（无密钥）。
type gatewayCall struct {
	Kind     string
	OK       bool
	Host     string
	Model    string
	Latency  time.Duration
	Err      string
	Occurred time.Time
}

func modelHostOf(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Host)
}

// deriveModelGatewayStatus 由槽是否配齐与窗内成败合成服务状态。
func deriveModelGatewayStatus(configured bool, total, ok int64) modelv1.ModelGatewayStatus {
	if !configured {
		return modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_UNCONFIGURED
	}
	if total <= 0 {
		return modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_READY
	}
	if ok == total {
		return modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_LIVE
	}
	if ok == 0 {
		return modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_DOWN
	}
	return modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_DEGRADED
}

func ensureModelGatewayCallsTable(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS model_gateway_calls (
		id          bigserial PRIMARY KEY,
		occurred_at timestamptz NOT NULL DEFAULT now(),
		kind        text NOT NULL,
		ok          boolean NOT NULL,
		host        text NOT NULL DEFAULT '',
		model       text NOT NULL DEFAULT '',
		latency_ms  integer NOT NULL DEFAULT 0,
		error       text NOT NULL DEFAULT ''
	)`); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS model_gateway_calls_at_idx ON model_gateway_calls (occurred_at DESC)`)
	return err
}

func recordGatewayCall(ctx context.Context, pool *pgxpool.Pool, rec gatewayCall) error {
	if pool == nil {
		return nil
	}
	if err := ensureModelGatewayCallsTable(ctx, pool); err != nil {
		return err
	}
	when := rec.Occurred
	if when.IsZero() {
		when = time.Now()
	}
	kind := rec.Kind
	if kind == "" {
		kind = gatewayCallComplete
	}
	ms := int(rec.Latency / time.Millisecond)
	if ms < 0 {
		ms = 0
	}
	if _, err := pool.Exec(ctx, `INSERT INTO model_gateway_calls(occurred_at, kind, ok, host, model, latency_ms, error)
		VALUES($1,$2,$3,$4,$5,$6,$7)`,
		when, kind, rec.OK, rec.Host, rec.Model, ms, rec.Err); err != nil {
		return err
	}
	cutoff := time.Now().Add(-kernel.ModelGatewayCallRetain)
	_, err := pool.Exec(ctx, `DELETE FROM model_gateway_calls WHERE occurred_at < $1`, cutoff)
	return err
}

type gatewayProviderRow struct {
	Host  string
	Total int64
	OK    int64
	Last  time.Time
}

type gatewayWindow struct {
	Providers []gatewayProviderRow
	Total     int64
	OK        int64
	LastAt    time.Time
	LastErr   string
}

func loadGatewayWindow(ctx context.Context, pool *pgxpool.Pool, currentBase string) (gatewayWindow, error) {
	var out gatewayWindow
	if pool == nil {
		return out, nil
	}
	if err := ensureModelGatewayCallsTable(ctx, pool); err != nil {
		return out, err
	}
	since := time.Now().Add(-kernel.ModelGatewayStatsWindow)
	rows, err := pool.Query(ctx, `SELECT host, count(*)::bigint, count(*) FILTER (WHERE ok)::bigint, max(occurred_at)
		FROM model_gateway_calls
		WHERE occurred_at >= $1
		GROUP BY host
		ORDER BY count(*) DESC, host`, since)
	if err != nil {
		if isUndefinedTable(err) {
			return mergeCurrentHost(out, currentBase), nil
		}
		return out, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var row gatewayProviderRow
		if err := rows.Scan(&row.Host, &row.Total, &row.OK, &row.Last); err != nil {
			return out, err
		}
		out.Providers = append(out.Providers, row)
		out.Total += row.Total
		out.OK += row.OK
		if row.Last.After(out.LastAt) {
			out.LastAt = row.Last
		}
		seen[row.Host] = true
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	var lastOK bool
	var lastErr string
	var lastAt time.Time
	err = pool.QueryRow(ctx, `SELECT occurred_at, ok, error FROM model_gateway_calls
		WHERE occurred_at >= $1 ORDER BY occurred_at DESC LIMIT 1`, since).
		Scan(&lastAt, &lastOK, &lastErr)
	if err == nil {
		out.LastAt = lastAt
		if !lastOK {
			out.LastErr = lastErr
		}
	}
	current := modelHostOf(currentBase)
	if current != "" && !seen[current] {
		out.Providers = append(out.Providers, gatewayProviderRow{Host: current})
	}
	return out, nil
}

func mergeCurrentHost(win gatewayWindow, currentBase string) gatewayWindow {
	current := modelHostOf(currentBase)
	if current == "" {
		return win
	}
	for _, p := range win.Providers {
		if p.Host == current {
			return win
		}
	}
	win.Providers = append(win.Providers, gatewayProviderRow{Host: current})
	return win
}

func providerCountOf(win gatewayWindow, currentBase string) int32 {
	seen := map[string]struct{}{}
	for _, p := range win.Providers {
		if p.Host != "" {
			seen[p.Host] = struct{}{}
		}
	}
	if host := modelHostOf(currentBase); host != "" {
		seen[host] = struct{}{}
	}
	return int32(len(seen))
}

func projectModelGateway(view onboardingView, win gatewayWindow) *modelv1.GetModelGatewayResponse {
	configured := view.HasSecret && strings.TrimSpace(view.BaseURL) != ""
	resp := &modelv1.GetModelGatewayResponse{
		BaseUrl:       view.BaseURL,
		Model:         view.Model,
		HasSecret:     view.HasSecret,
		SecretHint:    view.SecretHint,
		Status:        deriveModelGatewayStatus(configured, win.Total, win.OK),
		ProviderCount: providerCountOf(win, view.BaseURL),
		WindowSeconds: int64(kernel.ModelGatewayStatsWindow / time.Second),
		CallsTotal:    win.Total,
		CallsOk:       win.OK,
		LastError:     win.LastErr,
		Dialect:       protoModelDialect(view.Dialect),
		Providers:     make([]*modelv1.ModelProviderStat, 0, len(win.Providers)),
	}
	if !win.LastAt.IsZero() {
		resp.LastCallAt = timestamppb.New(win.LastAt)
	}
	for _, p := range win.Providers {
		stat := &modelv1.ModelProviderStat{
			Host:       p.Host,
			CallsTotal: p.Total,
			CallsOk:    p.OK,
		}
		if !p.Last.IsZero() {
			stat.LastAt = timestamppb.New(p.Last)
		}
		resp.Providers = append(resp.Providers, stat)
	}
	return resp
}

func copyModelGateway(dst *modelv1.UpdateModelGatewayResponse, src *modelv1.GetModelGatewayResponse) {
	if dst == nil || src == nil {
		return
	}
	dst.BaseUrl = src.GetBaseUrl()
	dst.Model = src.GetModel()
	dst.HasSecret = src.GetHasSecret()
	dst.SecretHint = src.GetSecretHint()
	dst.Status = src.GetStatus()
	dst.ProviderCount = src.GetProviderCount()
	dst.WindowSeconds = src.GetWindowSeconds()
	dst.CallsTotal = src.GetCallsTotal()
	dst.CallsOk = src.GetCallsOk()
	dst.LastCallAt = src.GetLastCallAt()
	dst.LastError = src.GetLastError()
	dst.Providers = src.GetProviders()
	dst.Dialect = src.GetDialect()
}
