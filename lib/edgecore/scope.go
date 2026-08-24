package edgecore

import (
	"context"
	"strings"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

// 制品作用范围的执行约定（纯函数，装载期与请求期各用一半）：
//   - 空 / nil 范围 = 全局生效（全站规则与演示用；正式签发管道会强制
//     显式范围，治理内核阶段实现）；
//   - asset_ids 非空 = 只对列表内资产生效（装载期过滤）；
//   - route_selector 非空 = 路径前缀（如 "/api/"），请求期收窄。
//
// [制品]: ../../docs/glossary.md#artifact

// ScopeCoversAsset 报告制品范围是否覆盖给定资产（装载期过滤用）。
func ScopeCoversAsset(s *artifactv1.Scope, assetID string) bool {
	if s == nil || len(s.AssetIds) == 0 {
		return true
	}
	for _, id := range s.AssetIds {
		if id == assetID {
			return true
		}
	}
	return false
}

// ScopePrefix 返回路由选择器表达的路径前缀；空表示不限路由。
func ScopePrefix(s *artifactv1.Scope) string {
	if s == nil {
		return ""
	}
	return s.RouteSelector
}

// ScopedDetector 按路径前缀收窄内部检测器的适用范围：
// 前缀不匹配的请求直接放行，不进内部检测。
type ScopedDetector struct {
	inner  Detector
	prefix string
}

// NewScoped 包装检测器，使其只对匹配路径前缀的请求生效。
func NewScoped(inner Detector, prefix string) Detector {
	if prefix == "" {
		return inner
	}
	return &ScopedDetector{inner: inner, prefix: prefix}
}

// ID 透传内部检查器的稳定标识。
func (s *ScopedDetector) ID() string { return s.inner.ID() }

// Tier 透传内部检查器的成本层级。
func (s *ScopedDetector) Tier() CostTier { return s.inner.Tier() }

// Evaluate 仅对路径前缀匹配的请求调用内部演示检查器。
func (s *ScopedDetector) Evaluate(ctx context.Context, req Request) (Verdict, error) {
	if !strings.HasPrefix(req.Path, s.prefix) {
		return Verdict{Action: ActionAllow}, nil
	}
	return s.inner.Evaluate(ctx, req)
}

// Match 按前缀收窄后问内部规则匹配器。
func (s *ScopedDetector) Match(req Request) (string, bool) {
	if !strings.HasPrefix(req.Path, s.prefix) {
		return "", false
	}
	if m, ok := s.inner.(interface{ Match(Request) (string, bool) }); ok {
		return m.Match(req)
	}
	v, _ := s.inner.Evaluate(context.Background(), req)
	return v.RuleID, v.Action == ActionBlock
}
