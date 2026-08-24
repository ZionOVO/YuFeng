package kernel

// Claims 是能力令牌的声明结构（令牌的载荷部分）。
//
// 能力令牌约束四件事：
//   - 是谁：Subject 与 Role 声明智能代理身份与角色；角色只决定默认权限，实际授权以令牌为准；
//   - 能用哪些工具：Tools 白名单之外的调用一律拒绝；
//   - 能用多少次：MaxCalls 预算，每次调用记账，超限即拒；
//   - 只能碰哪些对象：Bindings 圈定数据范围，工具的返回与引用不得越界。
//
// 普通智能代理与贾维斯使用同一种令牌结构与认证协议，差异只在中台签发的声明内容。
//
// [能力令牌]: ../../docs/glossary.md#capability-token
// [角色分层]: ../../docs/glossary.md#agent-role-layers
type Claims struct {
	// Issuer 签发方（治理内核）的密钥标识。
	Issuer string `json:"iss"`
	// Subject 持有方：智能代理标识（agent_id）或执行实例标识（run_id）。
	Subject string `json:"sub"`
	// AuthorizedParty 是实际领取租约并持访问令牌的 agent_id 或 worker_id。
	AuthorizedParty string `json:"azp,omitempty"`
	// Role 是中台签发的智能代理授予模板；当前使用编排者或工作进程模板，实际授权以工具白名单、权限域、对象绑定与调用上限为准，且不得与平台账户角色或执行实例岗位混用。
	Role string `json:"role,omitempty"`
	// Audience 用途域（如 "tools"）。
	Audience string `json:"aud"`
	// ExpiresAt / NotBefore 生效窗口（Unix 秒）。
	ExpiresAt int64 `json:"exp"`
	NotBefore int64 `json:"nbf"`
	// IssuedAt 签发时间（Unix 秒）。
	IssuedAt int64 `json:"iat"`
	// TokenID 只标识本次签发的令牌实例，用于短期吊销。
	TokenID string `json:"jti"`
	// BudgetID 是跨续租与重新领取保持不变的权威预算账户。
	BudgetID string `json:"budget_id,omitempty"`
	// LeaseEpoch 是所有权隔离栅栏；正常续租不变，重新领取递增。
	LeaseEpoch int64 `json:"lease_epoch,omitempty"`
	// Scopes 是可直连的超文本传输协议接口权限域；工具调用只认工具白名单，不认本字段。
	Scopes []string `json:"scopes,omitempty"`
	// Tools 工具白名单：只允许调用列出的工具（工具描述制品中的 name）。
	Tools []string `json:"tools,omitempty"`
	// MaxCalls 调用预算上限；0 表示不限制（仅只读工具允许）。
	MaxCalls int64 `json:"max_calls,omitempty"`
	// Bindings 绑定的业务对象（事件 / 漏洞发现 / 修复计划标识）。
	// 工具返回的数据不得越出此范围——证据引用防伪造校验的依据。
	Bindings []string `json:"bindings,omitempty"`
}
