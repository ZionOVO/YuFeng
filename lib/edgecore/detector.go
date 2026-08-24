package edgecore

import (
	"context"
	"net/netip"
)

// CostTier 标注检测器的执行成本档位，决定它可以挂在请求路径的哪个位置。
type CostTier int

const (
	// CostSyncMicros 同步档：进程内微秒级，允许在请求路径上直接调用。
	CostSyncMicros CostTier = iota + 1
	// CostAsyncMillis 异步档：毫秒级小模型，只允许订阅事件流离线调用。
	CostAsyncMillis
)

// Request 是交给检测器的请求数据（已按证据预算截断）。
type Request struct {
	AssetID string
	UnitID  string
	Method  string
	Path    string
	Query   string
	Headers map[string]string
	Body    []byte
	// ClientAddress 只作检测元数据与边缘假名输入，不进规范视图或检测键。
	ClientAddress netip.Addr
}

// Action 只属于闸。同步眼睛不得返回本类型。
type Action int

const (
	ActionAllow Action = iota
	ActionObserve
	ActionBlock
)

// Verdict 是一次检测的结论。
type Verdict struct {
	// DetectorID 检测器标识（由引擎回填，检测器实现无须填写）。
	DetectorID string
	Action     Action
	RuleID     string
	Confidence float64
	Message    string
}

// Detector 是演示 KIND_RULE 的匹配器接口（闸的旁路表），不是同步眼睛。
// 活路径同步口是 Inspector；本接口的 Action 不得被新眼睛用来 403。
//
// [检测器]: ../../docs/glossary.md#inspector
// [闸]: ../../docs/glossary.md#gate
type Detector interface {
	// ID 返回规则制品标识。
	ID() string
	// Tier 返回成本档位。
	Tier() CostTier
	// Evaluate 仅供规则编译单测。闸必须走 Match，不得把 Action 当拦截权。
	Evaluate(ctx context.Context, req Request) (Verdict, error)
}
