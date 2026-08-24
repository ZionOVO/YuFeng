package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const (
	// DataplanePostureReverseProxy 是监督器控制契约中的反向代理姿态全名。
	DataplanePostureReverseProxy = "INGRESS_POSTURE_REVERSE_PROXY"
	// DataplanePostureExtAuthz 是监督器控制契约中的外部授权姿态全名。
	DataplanePostureExtAuthz = "INGRESS_POSTURE_EXT_AUTHZ"
	// TrustedProxyCIDRLimit 限制一份监听计划可信代理网段数量。
	TrustedProxyCIDRLimit = 64
)

// DataplaneDesiredSpec 是中台交给本机监督器的最小期望规格。
// Digest 覆盖 unit_id 与其余规范字段，不作为摘要输入。
type DataplaneDesiredSpec struct {
	Posture           string   `json:"posture"`
	TrafficKey        string   `json:"traffic_key"`
	ListenAddress     string   `json:"listen_address"`
	UpstreamURL       string   `json:"upstream_url,omitempty"`
	TrustedProxyCIDRs []string `json:"trusted_proxy_cidrs,omitempty"`
	Digest            string   `json:"digest"`
}

// Normalize 去掉规格字段首尾空白，返回副本。
func (s DataplaneDesiredSpec) Normalize() DataplaneDesiredSpec {
	s.Posture = strings.TrimSpace(s.Posture)
	s.TrafficKey = strings.TrimSpace(s.TrafficKey)
	s.ListenAddress = strings.TrimSpace(s.ListenAddress)
	s.UpstreamURL = strings.TrimSpace(s.UpstreamURL)
	s.TrustedProxyCIDRs = cleanTrustedProxyCIDRs(s.TrustedProxyCIDRs)
	s.Digest = strings.TrimSpace(s.Digest)
	return s
}

// CalculateDigest 计算本机单元期望规格的稳定摘要。
func (s DataplaneDesiredSpec) CalculateDigest(unitID string) (string, error) {
	n := s.Normalize()
	normalizedCIDRs, err := NormalizeTrustedProxyCIDRs(n.TrustedProxyCIDRs)
	if err != nil {
		return "", err
	}
	canonical := struct {
		UnitID            string   `json:"unit_id"`
		Posture           string   `json:"posture"`
		TrafficKey        string   `json:"traffic_key"`
		ListenAddress     string   `json:"listen_address"`
		UpstreamURL       string   `json:"upstream_url,omitempty"`
		TrustedProxyCIDRs []string `json:"trusted_proxy_cidrs,omitempty"`
	}{
		UnitID: strings.TrimSpace(unitID), Posture: n.Posture, TrafficKey: n.TrafficKey,
		ListenAddress: n.ListenAddress, UpstreamURL: n.UpstreamURL, TrustedProxyCIDRs: normalizedCIDRs,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Validate 检查监督器可执行的闭集姿态、地址、回源与摘要。
func (s DataplaneDesiredSpec) Validate(unitID string) error {
	n := s.Normalize()
	if strings.TrimSpace(unitID) == "" || len(strings.TrimSpace(unitID)) > 64 {
		return errors.New("unit_id must be 1-64 characters")
	}
	if n.TrafficKey == "" || len(n.TrafficKey) > 256 {
		return errors.New("traffic_key must be 1-256 characters")
	}
	if err := validateDataplaneListenAddress(n.ListenAddress); err != nil {
		return err
	}
	normalizedCIDRs, err := NormalizeTrustedProxyCIDRs(n.TrustedProxyCIDRs)
	if err != nil {
		return err
	}
	if !slices.Equal(normalizedCIDRs, n.TrustedProxyCIDRs) {
		return errors.New("trusted_proxy_cidrs must be normalized")
	}
	switch n.Posture {
	case DataplanePostureReverseProxy:
		if err := validateDataplaneUpstream(n.UpstreamURL); err != nil {
			return err
		}
	case DataplanePostureExtAuthz:
		if n.UpstreamURL != "" {
			return errors.New("ext_authz spec must not contain upstream_url")
		}
	default:
		return errors.New("posture is unsupported")
	}
	want, err := n.CalculateDigest(unitID)
	if err != nil {
		return err
	}
	if n.Digest == "" || n.Digest != want {
		return errors.New("spec digest does not match desired spec")
	}
	return nil
}

// NormalizeTrustedProxyCIDRs 把互联网协议第四版或第六版网段整理成排序去重的无类别域间路由前缀。
//
// [无类别域间路由]: ../../docs/glossary.md#protocol-and-implementation-terms
func NormalizeTrustedProxyCIDRs(values []string) ([]string, error) {
	cleaned := cleanTrustedProxyCIDRs(values)
	if len(cleaned) > TrustedProxyCIDRLimit {
		return nil, fmt.Errorf("trusted_proxy_cidrs exceed %d entries", TrustedProxyCIDRLimit)
	}
	set := make(map[string]struct{}, len(cleaned))
	for _, raw := range cleaned {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix.Addr().Zone() != "" {
			return nil, errors.New("trusted_proxy_cidrs contains invalid cidr")
		}
		set[prefix.Masked().String()] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	slices.Sort(out)
	return out, nil
}

// cleanTrustedProxyCIDRs 去除空白网段，并返回排序去重的原始字符串。
func cleanTrustedProxyCIDRs(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

// DataplaneProbeRequest 是监督器校验边缘管理口时的期望状态。
type DataplaneProbeRequest struct {
	UnitID        string `json:"unit_id"`
	Posture       string `json:"posture"`
	TrafficKey    string `json:"traffic_key"`
	GenerationID  string `json:"generation_id"`
	GenerationSeq int64  `json:"generation_seq"`
	ListenVersion uint64 `json:"listen_plan_version"`
}

// EdgeReadyState 是边缘独立管理口返回的活动状态。
type EdgeReadyState struct {
	Ready         bool   `json:"ready"`
	UnitID        string `json:"unit_id"`
	Posture       string `json:"posture"`
	TrafficKey    string `json:"traffic_key"`
	GenerationID  string `json:"generation_id"`
	GenerationSeq int64  `json:"generation_seq"`
	ListenVersion uint64 `json:"listen_plan_version"`
}

// validateDataplaneListenAddress 校验监听地址只使用空主机或互联网协议地址以及有效端口。
func validateDataplaneListenAddress(raw string) error {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return fmt.Errorf("listen_address is invalid: %w", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("listen_address port is invalid")
	}
	if host != "" && net.ParseIP(host) == nil {
		return errors.New("listen_address host must be an ip address")
	}
	return nil
}

// validateDataplaneUpstream 校验上游为无凭据、无片段的绝对超文本传输协议地址。
func validateDataplaneUpstream(raw string) error {
	if raw == "" || raw == "builtin" {
		return errors.New("upstream_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Hostname() == "" {
		return errors.New("upstream_url must be absolute")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("upstream_url scheme must be http or https")
	}
	if u.User != nil || u.Fragment != "" {
		return errors.New("upstream_url must not contain user info or fragment")
	}
	return nil
}
