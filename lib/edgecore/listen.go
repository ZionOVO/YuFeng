package edgecore

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"

	"yufeng/lib/kernel"
)

// ListenPlanSchema 是单元监听计划载荷标识。
const ListenPlanSchema = "listen-plan/v1"

// ValidateListenPlans 检查：一个单元恰好一种壳；同一流量键至多一个拦截单元。
// 形态不进资产世代。
func ValidateListenPlans(plans []*artifactv1.UnitListenPlan) error {
	units := map[string]commonv1.IngressPosture{}
	interceptKey := map[string]string{}
	for _, p := range plans {
		if p == nil || strings.TrimSpace(p.UnitId) == "" {
			return fmt.Errorf("listen plan is incomplete")
		}
		posture := ResolvePosture(p.Posture)
		if prev, ok := units[p.UnitId]; ok && prev != posture {
			return fmt.Errorf("unit %s must have exactly one posture", p.UnitId)
		}
		units[p.UnitId] = posture
		if !Intercepts(posture) {
			continue
		}
		key := p.TrafficKey
		if other, ok := interceptKey[key]; ok && other != p.UnitId {
			return fmt.Errorf("traffic key already has intercept unit %s", other)
		}
		interceptKey[key] = p.UnitId
	}
	return nil
}

// ValidateUnitListenPlan 检查一份单元监听计划的结构与姿态约束。
// 验签与单调版本由装载器单独处理。
func ValidateUnitListenPlan(plan *artifactv1.UnitListenPlan) error {
	if plan == nil || strings.TrimSpace(plan.UnitId) == "" {
		return fmt.Errorf("listen plan is incomplete")
	}
	if plan.Version == 0 {
		return fmt.Errorf("listen plan version is required")
	}
	if strings.TrimSpace(plan.TrafficKey) == "" {
		return fmt.Errorf("listen plan traffic_key is required")
	}
	posture := plan.Posture
	if posture < commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY || posture > commonv1.IngressPosture_INGRESS_POSTURE_MIRROR_OBSERVE {
		return fmt.Errorf("listen plan posture is invalid")
	}
	if err := validateListenAddress(plan.ListenAddress); err != nil {
		return err
	}
	upstream := strings.TrimSpace(plan.UpstreamUrl)
	if posture == commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY {
		if err := validateUpstreamURL(upstream); err != nil {
			return err
		}
	} else if upstream != "" {
		return fmt.Errorf("listen plan upstream_url is only valid for reverse proxy")
	}
	if Intercepts(posture) && strings.TrimSpace(plan.FollowUnitId) != "" {
		return fmt.Errorf("intercept listen plan must not follow another unit")
	}
	cidrs := plan.GetClientSource().GetTrustedProxyCidrs()
	normalized, err := kernel.NormalizeTrustedProxyCIDRs(cidrs)
	if err != nil {
		return fmt.Errorf("listen plan client_source is invalid: %w", err)
	}
	if !slices.Equal(cidrs, normalized) {
		return fmt.Errorf("listen plan client_source must be normalized")
	}
	if _, err := kernel.NormalizeModelIngressWindow(plan.GetModelIngressWindow()); err != nil {
		return fmt.Errorf("listen plan model_ingress_window is invalid: %w", err)
	}
	return nil
}

func validateListenAddress(raw string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("listen plan listen_address is invalid: %w", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("listen plan port is invalid")
	}
	if host != "" && net.ParseIP(host) == nil {
		return fmt.Errorf("listen plan host must be an ip address")
	}
	return nil
}

func validateUpstreamURL(raw string) error {
	if raw == "" || raw == "builtin" {
		return fmt.Errorf("listen plan upstream_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Hostname() == "" {
		return fmt.Errorf("listen plan upstream_url must be absolute")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("listen plan upstream_url scheme must be http or https")
	}
	if u.User != nil || u.Fragment != "" {
		return fmt.Errorf("listen plan upstream_url must not contain user info or fragment")
	}
	return nil
}

// TapWindow 是一个心跳窗的请求形状，供 tap_silent / tap_skew。
type TapWindow struct {
	UnitID         string
	Posture        commonv1.IngressPosture
	TrafficKey     string
	FollowUnitID   string
	WindowReqs     uint64
	PrevWindowReqs uint64
	Routes         []string
	TotalRequests  uint64
}

// EvaluateTapHealth 按 api 第 21.1 节给出单元健康。
func EvaluateTapHealth(windows []TapWindow) map[string]commonv1.UnitHealth {
	out := make(map[string]commonv1.UnitHealth, len(windows))
	interceptAlive := map[string]bool{}
	byUnit := map[string]TapWindow{}
	for _, w := range windows {
		byUnit[w.UnitID] = w
		if Intercepts(ResolvePosture(w.Posture)) && w.WindowReqs > 0 {
			interceptAlive[w.TrafficKey] = true
		}
	}
	for _, w := range windows {
		out[w.UnitID] = commonv1.UnitHealth_UNIT_HEALTH_HEALTHY
		posture := ResolvePosture(w.Posture)
		if Observes(posture) && interceptAlive[w.TrafficKey] && w.WindowReqs == 0 && w.PrevWindowReqs == 0 {
			out[w.UnitID] = commonv1.UnitHealth_UNIT_HEALTH_TAP_SILENT
			continue
		}
		if w.FollowUnitID == "" {
			continue
		}
		peer, ok := byUnit[w.FollowUnitID]
		if !ok {
			continue
		}
		if w.TotalRequests < 100 || peer.TotalRequests < 100 {
			continue
		}
		if jaccard(w.Routes, peer.Routes) < 0.5 {
			out[w.UnitID] = commonv1.UnitHealth_UNIT_HEALTH_TAP_SKEW
		}
	}
	return out
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	set := map[string]struct{}{}
	for _, x := range a {
		set[x] = struct{}{}
	}
	inter := 0
	union := map[string]struct{}{}
	for _, x := range a {
		union[x] = struct{}{}
	}
	for _, x := range b {
		union[x] = struct{}{}
		if _, ok := set[x]; ok {
			inter++
		}
	}
	if len(union) == 0 {
		return 1
	}
	return float64(inter) / float64(len(union))
}
