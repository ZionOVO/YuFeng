package edgecore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"

	commonv1 "yufeng/proto/gen/commonv1"
)

// RulePayloadSchema 是规则类制品载荷的结构标识，装载时与制品的
// payload_schema 字段核对，防止其他种类制品被当作规则解析。
const RulePayloadSchema = "rules/v1"

// PolicyPayloadSchema 是检测键策略制品载荷的结构标识。
const PolicyPayloadSchema = "policy/v1"

// Rule 是规则类制品载荷中的一条检测规则。
type Rule struct {
	// ID 规则标识，进遥测的 rule_id 字段。
	ID string `json:"id"`
	// Pattern 正则表达式，对目标内容做子串匹配。
	Pattern string `json:"pattern"`
	// Target 匹配目标：path / query / body；空 = 三者都查。
	Target string `json:"target,omitempty"`
}

// ParseRules 从 JavaScript 对象表示法数组解析规则类制品载荷。
func ParseRules(payload []byte) ([]Rule, error) {
	var rs []Rule
	if err := json.Unmarshal(payload, &rs); err != nil {
		return nil, fmt.Errorf("parse rules: %w", err)
	}
	return rs, nil
}

// MarshalRules 序列化规则载荷（签发侧与测试使用）。
func MarshalRules(rules []Rule) ([]byte, error) {
	return json.Marshal(rules)
}

// RuleDetector 是规则制品驱动的同步检测器（第一个第一方检测器）。
type RuleDetector struct {
	id    string
	rules []compiledRule
}

type compiledRule struct {
	rule Rule
	re   *regexp.Regexp
}

// NewRuleDetector 编译规则并构造检测器。装载期拒绝带病规则：
// 空 ID、重复 ID、空模式（空正则匹配一切，等于全量拒绝）、未知目标
// （拼错目标不该静默扩大匹配面）——任何一条命中即整体报错。
func NewRuleDetector(id string, rules []Rule) (*RuleDetector, error) {
	if len(rules) == 0 {
		return nil, fmt.Errorf("detector %s: empty rule set", id)
	}
	d := &RuleDetector{id: id}
	seen := make(map[string]bool, len(rules))
	for _, r := range rules {
		switch {
		case r.ID == "":
			return nil, fmt.Errorf("detector %s: rule with empty id", id)
		case seen[r.ID]:
			return nil, fmt.Errorf("detector %s: duplicate rule id %q", id, r.ID)
		case r.Pattern == "":
			return nil, fmt.Errorf("detector %s: rule %s: empty pattern matches everything", id, r.ID)
		case r.Target != "" && r.Target != "path" && r.Target != "query" && r.Target != "body":
			return nil, fmt.Errorf("detector %s: rule %s: unknown target %q (want path/query/body)", id, r.ID, r.Target)
		}
		seen[r.ID] = true
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("detector %s: rule %s: %w", id, r.ID, err)
		}
		d.rules = append(d.rules, compiledRule{rule: r, re: re})
	}
	return d, nil
}

// ID 返回演示规则检查器的稳定标识。
func (d *RuleDetector) ID() string { return d.id }

// Tier 返回演示规则检查器的同步微秒级成本层级。
func (d *RuleDetector) Tier() CostTier { return CostSyncMicros }

// Match 报告规则是否命中。闸用本方法，不把 Action 当拦截权。
func (d *RuleDetector) Match(req Request) (string, bool) {
	if d == nil {
		return "", false
	}
	for _, c := range d.rules {
		if c.re.MatchString(targetString(c.rule.Target, req)) {
			return c.rule.ID, true
		}
	}
	return "", false
}

// Inspect 只出发现。KIND_RULE 不进同步眼睛注册表，本方法供回放对照。
func (d *RuleDetector) Inspect(_ context.Context, input InspectionInput) (Inspection, error) {
	view := input.View
	id, ok := d.Match(RequestFromView(view))
	out := Inspection{Coverage: view.Coverage, Rejected: view.Rejected}
	if ok {
		out.Detections = []Detection{ruleDetection(d.id, id, RequestFromView(view))}
	}
	return out, nil
}

func ruleDetection(inspectorID, ruleID string, req Request) Detection {
	loc := commonv1.InspectionSurface_INSPECTION_SURFACE_PATH
	if req.Query != "" {
		loc = commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY
	}
	if len(req.Body) > 0 && req.Query == "" {
		loc = commonv1.InspectionSurface_INSPECTION_SURFACE_BODY
	}
	return Detection{InspectorID: inspectorID, RuleID: ruleID, Location: loc, Score: 1}
}

// Evaluate 无副作用：不读写网络与磁盘，不取时钟。
func (d *RuleDetector) Evaluate(_ context.Context, req Request) (Verdict, error) {
	if id, ok := d.Match(req); ok {
		return Verdict{
			Action:     ActionBlock,
			RuleID:     id,
			Confidence: 1,
			Message:    "matched rule " + id,
		}, nil
	}
	return Verdict{Action: ActionAllow}, nil
}

func targetString(target string, req Request) string {
	switch target {
	case "path":
		return req.Path
	case "query":
		return decodedQuery(req.Query)
	case "body":
		return string(req.Body)
	default:
		return req.Path + " " + decodedQuery(req.Query) + " " + string(req.Body)
	}
}

// decodedQuery 还原统一资源定位符中的加号与百分号编码；检测必须看解码后的形态，
// 否则 `UNION+SELECT` 这类编码载荷直接绕过。
func decodedQuery(q string) string {
	if dec, err := url.QueryUnescape(q); err == nil {
		return dec
	}
	return q
}
