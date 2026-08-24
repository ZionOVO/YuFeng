package edgecore

import (
	"context"
	"fmt"

	commonv1 "yufeng/proto/gen/commonv1"
)

// Engine 持有一组同步检测器，是数据面最小的裁决内核。
type Engine struct {
	detectors []Detector
}

// NewEngine 装配引擎。第二个检测器接入时引入注册表（三次原则，现在只有规则检测器）。
func NewEngine(ds ...Detector) *Engine {
	return &Engine{detectors: ds}
}

// Result 是一次请求的全部检测结论。
type Result struct {
	Verdicts []Verdict
}

// MatchRules 问闸侧 KIND_RULE 匹配器，不把 Inspector 返回值当拦截权。
func (e *Engine) MatchRules(req Request) (Verdict, bool) {
	if e == nil {
		return Verdict{}, false
	}
	for _, d := range e.detectors {
		if m, ok := d.(interface{ Match(Request) (string, bool) }); ok {
			if id, hit := m.Match(req); hit {
				return Verdict{DetectorID: d.ID(), Action: ActionBlock, RuleID: id, Confidence: 1, Message: "matched rule " + id}, true
			}
			continue
		}
		v, err := d.Evaluate(context.Background(), req)
		if err != nil {
			continue
		}
		v.DetectorID = d.ID()
		if v.Action == ActionBlock {
			return v, true
		}
	}
	return Verdict{}, false
}

// Check 跑全部检测器并收集结论；检测器标识由本函数无条件回填
// （检测器自行填写的值会被覆盖——标识的唯一权威是注册处）。
// 检测器自身出错按"观察"记录，不中断请求处理。
func (e *Engine) Check(ctx context.Context, req Request) Result {
	var res Result
	for _, d := range e.detectors {
		v, err := d.Evaluate(ctx, req)
		if err != nil {
			v = Verdict{Action: ActionObserve, Message: fmt.Sprintf("detector error: %v", err)}
		}
		v.DetectorID = d.ID()
		res.Verdicts = append(res.Verdicts, v)
	}
	return res
}

// Decide 按发布状态决定最终动作（纯函数，表驱动测试覆盖）。
//
// 本函数服务于单模式代理形态（Proxy）：整机一个模式、无发布集。canary 在
// 此形态缺少分桶依据（请求标识），按"宁可过拦"视同 enforce 全量拦截；
// 分桶放量只在发布集形态（ReleaseSet.Check）实现。shadow 与一切未知状态
// （含未指定）只记录不生效——配置错误绝不静默升级为拦截。
//
// [生效状态]: ../../docs/glossary.md#release-modes
func Decide(mode commonv1.ReleaseMode, res Result) Action {
	hasBlock := false
	for _, v := range res.Verdicts {
		if v.Action == ActionBlock {
			hasBlock = true
			break
		}
	}
	if !hasBlock {
		return ActionAllow
	}
	switch mode {
	case commonv1.ReleaseMode_RELEASE_MODE_ENFORCE, commonv1.ReleaseMode_RELEASE_MODE_CANARY:
		return ActionBlock
	default:
		return ActionObserve
	}
}
