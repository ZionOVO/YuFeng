package kernel

import (
	"errors"
	"time"
)

// 下列常量是生产非功能预算，不是压测记录。
// 权威表见 docs/architecture.md §13；实现与测试必须引用这些标识符，不得另写一份数字。
//
// [复核时间与硬过期]: ../../docs/glossary.md#review-hard-expiry

const (
	// MinimumEdgeVersion 是当前世代合同要求的最低边缘版本标识。
	MinimumEdgeVersion = "dev"
	// ShadowMinDuration 是自动 canary 前 shadow 最短时长。
	ShadowMinDuration = 300 * time.Second
	// ShadowMinRequests 是自动 canary 前 shadow 最少请求数。
	ShadowMinRequests = 100
	// CanaryMinDuration 是自动 enforce 前 canary 最短时长。
	CanaryMinDuration = 300 * time.Second
	// CanaryMinRequests 是自动 enforce 前 canary 最少请求数。
	CanaryMinRequests = 100
	// CanaryPercentDefault 是未指定时的 canary 百分比。
	CanaryPercentDefault int32 = 5
	// CanaryPercentMin / CanaryPercentMax 是可配置区间。
	CanaryPercentMin int32 = 1
	CanaryPercentMax int32 = 25

	// GuardWindow 是守护窗口聚合时长。
	GuardWindow = 5 * time.Minute
	// GuardBadWindows 是触发自动回滚的连续坏窗口数。
	GuardBadWindows = 2
	// Guard5xxRateMultiple 是相对 shadow 基线的 5xx 倍率阈值。
	Guard5xxRateMultiple = 2.0
	// Guard5xxAbsDelta 是 5xx 率绝对差阈值。
	Guard5xxAbsDelta = 0.005
	// GuardP99RelGrowth 是 p99 相对基线增长率阈值。
	GuardP99RelGrowth = 0.10
	// GuardP99AbsMicros 是 p99 绝对增量阈值（微秒）。
	GuardP99AbsMicros = 5000
	// DenyFeedbackBlockThreshold 是触发自动回滚的连续误报数。
	DenyFeedbackBlockThreshold = 3

	// ExtAuthzTimeout 是外部授权通道超时（失败即开）。
	ExtAuthzTimeout = 50 * time.Millisecond
	// ExtAuthzTimeoutRateWindow 是超时率熔断滑动窗。
	ExtAuthzTimeoutRateWindow = 10 * time.Second
	// ExtAuthzTimeoutRateTrip 是改为 503 的超时率。
	ExtAuthzTimeoutRateTrip = 0.05
	// ExtAuthzTimeoutRateRecover 是恢复失败即开的超时率。
	ExtAuthzTimeoutRateRecover = 0.01
	// ExtAuthzTimeoutRateRecoverHold 是恢复判定的持续时长。
	ExtAuthzTimeoutRateRecoverHold = 30 * time.Second

	// EngineBodyLimitBytes 是进入同步引擎的请求体上限。
	EngineBodyLimitBytes = 64 * 1024
	// ClockSkew 是世代 not_before 与能力令牌共用的允许时钟偏差。
	ClockSkew = 60 * time.Second
	// NoDetectionSampleRate 是普通无发现入账采样率。
	NoDetectionSampleRate = 0.01
	// EvidenceRingTTL 是边缘本地加密证据环缓冲存活时间。
	EvidenceRingTTL = 15 * time.Minute
	// TrafficReviewWindow 是流量审查统计窗时长。
	TrafficReviewWindow = 5 * time.Minute
	// TrafficReviewTopRoutes 是统计窗保留的方法与路由组合数。
	TrafficReviewTopRoutes = 32
	// TrafficReviewCandidatesPerWindow 是每单元每窗候选上限。
	TrafficReviewCandidatesPerWindow = 4
	// TrafficReviewEvidenceBytes 是单候选证据字节上限。
	TrafficReviewEvidenceBytes = 8 << 10
	// TrafficReviewCaseEvidenceBytes 是单案件批准的证据字节上限。
	TrafficReviewCaseEvidenceBytes = 40 << 10
	// TrafficReviewModelInputTokens 是流量调查单次模型输入令牌上限。
	TrafficReviewModelInputTokens = 8192
	// TrafficReviewModelInputBytes 是按每令牌四字节估算的敏感中继输入硬上限。
	TrafficReviewModelInputBytes = TrafficReviewModelInputTokens * 4
	// TrafficReviewModelEvidenceBytes 为系统提示与类型化封装预留两千字节后的证据正文上限。
	TrafficReviewModelEvidenceBytes = TrafficReviewModelInputBytes - 2<<10
	// TrafficReviewVaultBytes 是单边缘加密证据库硬上限。
	TrafficReviewVaultBytes = 256 << 20
	// TrafficReviewEvidenceTTL 是候选证据有效期。
	TrafficReviewEvidenceTTL = 24 * time.Hour
	// TrafficReviewDailyCases 是单部署每日模型调查上限。
	TrafficReviewDailyCases = 200
	// TrafficReviewDailyCasesPerAsset 是单资产每日模型调查上限。
	TrafficReviewDailyCasesPerAsset = 24
	// TrafficReviewConcurrentCases 是中央执行池批准后的并发硬上限。
	TrafficReviewConcurrentCases = 4

	// ClusterWindow 是研判聚类翻页窗。
	ClusterWindow = 15 * time.Minute
	// ClusterIdle 是聚类空闲关闭时间。
	ClusterIdle = 2 * time.Hour
	// ClusterRepresentatives 是一条聚类最多保留的代表事件数。
	ClusterRepresentatives = 5

	// P99ExtraLatency 是数据面第 99 百分位额外延迟预算。
	P99ExtraLatency = 5 * time.Millisecond
	// ModelBypassP99Budget 是模型旁路相对关闭状态允许增加的第 99 百分位延迟。
	ModelBypassP99Budget = time.Millisecond
	// EdgeThroughputRPS 是单 edge 进程吞吐目标。
	EdgeThroughputRPS = 2000
	// EdgeMemoryBytes 是单 edge 进程内存上限。
	EdgeMemoryBytes = 512 * 1024 * 1024
	// EdgeCacheDiskBytes 是世代缓存磁盘上限（当前 + 上一份）。
	EdgeCacheDiskBytes = 512 * 1024 * 1024
	// EdgeTelemetrySpoolBytes 是断网遥测落盘缓冲上限。
	EdgeTelemetrySpoolBytes = 64 * 1024 * 1024
	// EdgeInFlight 是单 edge 在途请求上限。
	EdgeInFlight = 4096
	// EdgeBypassQueueMax 是本地异步旁路队列条数上限。
	EdgeBypassQueueMax = 256
	// EdgeBypassQueueBytes 是本地异步旁路队列字节上限。
	EdgeBypassQueueBytes = 8 << 20
	// EdgeBypassWorkers 是本地异步旁路后台协程数。
	EdgeBypassWorkers = 2
	// EvidenceRingMaxEntries 是证据环条数上限。
	EvidenceRingMaxEntries = 1024
	// EvidenceRingMaxBytes 是证据环字节上限。
	EvidenceRingMaxBytes = 32 << 20
	// ExtAuthzHalfOpenPerSec 是外部授权熔断半开时每秒放行的真请求数。
	ExtAuthzHalfOpenPerSec = 1

	// HTTPReadHeaderTimeout 是控制面与数据面读头超时。
	HTTPReadHeaderTimeout = 5 * time.Second
	// HTTPReadTimeout 是读超时。
	HTTPReadTimeout = 30 * time.Second
	// HTTPWriteTimeout 是写超时。
	HTTPWriteTimeout = 30 * time.Second
	// HTTPIdleTimeout 是空闲连接超时。
	HTTPIdleTimeout = 60 * time.Second
	// HTTPMaxHeaderBytes 是请求头上限。
	HTTPMaxHeaderBytes = 1 << 20
	// ControlPlaneBodyLimit 是控制面请求体上限。
	ControlPlaneBodyLimit = 1 << 20

	// TTLDefault 是生产策略默认硬过期间隔（无 hard_expires_at 时填入）。
	TTLDefault = 24 * time.Hour
	// TTLMin 是硬过期间隔下限。
	TTLMin = 300 * time.Second
	// TTLMax 是硬过期间隔上限。
	TTLMax = 7 * 24 * time.Hour
	// ReviewDefault 是未指定 review_at 时相对签发的复核间隔。
	ReviewDefault = 24 * time.Hour

	// AuditCheckpointPeriod 是审计哈希链对外检查点周期。
	AuditCheckpointPeriod = time.Hour
	// BackupRestoreDeadline 是全新库恢复时间目标（与检查点同量级）。
	BackupRestoreDeadline = time.Hour
	// BackupCommittedRPO 是允许丢失的已提交行数：必须为 0。
	BackupCommittedRPO = 0

	// UnitRPCQPS 是单元域合计每秒请求上限（心跳除外）。
	UnitRPCQPS = 10
	// UploadBatchMax 是单次上传事件上限。
	UploadBatchMax = 100
	// PageSizeDefault 是列表默认页大小。
	PageSizeDefault = 50
	// PageSizeMax 是列表页大小上限。
	PageSizeMax = 200
	// ArtifactPageMaxBytes 是 ListReleases / ListGenerations 默认字节预算。
	ArtifactPageMaxBytes = 4 << 20
	// ArtifactPageHardMaxBytes 是 ListReleases / ListGenerations 字节预算上限。
	ArtifactPageHardMaxBytes = 16 << 20
	// IdempotencyPendingTTL 是写远程过程调用幂等键处于待处理状态的占用上限。
	// 超过后同键同摘要允许接管；必须大于 ChatCompleteTimeout。
	IdempotencyPendingTTL = 120 * time.Second

	// AccessTokenTTL 是单元与智能代理短期访问令牌存活时间。
	AccessTokenTTL = 30 * time.Minute
	// RefreshTokenTTL 是可轮换刷新令牌存活时间。
	RefreshTokenTTL = 30 * 24 * time.Hour
	// CapabilityTokenMaxTTL 是能力令牌上限（不得超过租约）。
	CapabilityTokenMaxTTL = 15 * time.Minute
	// LongPollMax 是历史长轮询上限，不再作为会话或智能代理门禁。
	// 会话使用 SessionLongPollMax，智能代理使用 AgentLongPollMax。
	LongPollMax = 30 * time.Second
	// LongPollConcurrencyPerAgent 是同一智能代理长轮询并发上限。
	LongPollConcurrencyPerAgent = 4
	// LoginRatePerMinute 是同一用户名+来源地址的登录尝试上限。
	LoginRatePerMinute = 10
	// PublicAuthRatePerMinute 是公开注册与刷新接口的来源地址上限。
	PublicAuthRatePerMinute = 30
	// ToolInvokeQPS 是同一访问令牌的工具调用每秒上限。
	ToolInvokeQPS = 20
	// RunModelInputTokensPerCall 是执行实例每次模型调用的保守输入令牌预留。
	RunModelInputTokensPerCall = (1 << 20) / 4
	// RunToolResultBytesPerCall 是执行实例每次工具调用的结果字节预留。
	RunToolResultBytesPerCall = ControlPlaneBodyLimit
	// RunModelCostMicrounitsPerCall 是供应商未返回成本时的保守微单位预留。
	RunModelCostMicrounitsPerCall = 1_000_000
	// AgentPollQPS 是同一访问令牌的长轮询发起每秒上限（不含挂起时长）。
	AgentPollQPS = 5

	// JarvisOnlineWindow 是 GetOnboarding.jarvis_online 判定心跳/领指令仍有效的窗口。
	JarvisOnlineWindow = 60 * time.Second
	// EdgeOnlineWindow 是人工部署 Edge 的最近心跳就绪窗口。
	EdgeOnlineWindow = 90 * time.Second
	// ModelSideIngressQueueMax 是 Edge 到 ModelSide 的最大排队条目数。
	ModelSideIngressQueueMax = 256
	// ModelSideIngressQueueBytes 是 Edge 到 ModelSide 的最大排队正文总字节。
	ModelSideIngressQueueBytes = 8 << 20
	// ModelSideIngressWorkers 是 Edge 后台模型输入发送协程数。
	ModelSideIngressWorkers = 2
	// EdgeObservationQueueMax 是请求路径到本地遥测落盘后台的最大条目数。
	EdgeObservationQueueMax = 256
	// ModelSideIngressTimeout 是单次 Edge 到 ModelSide 后台提交上限。
	ModelSideIngressTimeout = 2 * time.Second
	// ModelSideResultQueueMax 是 ModelSide 独立结果队列上限。
	ModelSideResultQueueMax = 1024
	// ModelSideUploadBatchMax 是 ModelSide 到 Brain 的单批结果上限。
	ModelSideUploadBatchMax = 100
	// ModelReviewWindow 是基线签名模型档案的复核窗口。
	ModelReviewWindow = 5 * time.Minute
	// ModelReviewPerUnit 是基线每单元每窗代表上限。
	ModelReviewPerUnit = 4
	// ModelReviewPerRoute 是基线同方法路由每窗代表上限。
	ModelReviewPerRoute = 1
	// SessionLongPollDefault 是 PollMessages 省略 long_poll_seconds 时的等待。
	SessionLongPollDefault = 30 * time.Second
	// SessionLongPollMax 是 PollMessages.long_poll_seconds 上限。
	SessionLongPollMax = 60 * time.Second
	// AgentLongPollDefault 是 PollInstructions 省略 long_poll_seconds 时的等待。
	AgentLongPollDefault = 30 * time.Second
	// AgentLongPollMax 是 PollInstructions.long_poll_seconds 上限。
	// LongPollMax 不再作为本上限。
	AgentLongPollMax = 60 * time.Second
	// ControlPlaneHTTPTimeout 覆盖最长控制面长轮询并保留响应传输余量。
	ControlPlaneHTTPTimeout = SessionLongPollMax + 15*time.Second
)

// DefaultModelSideSocket 是 Edge 与同机 ModelSide 的优先 Unix 域套接字。
const DefaultModelSideSocket = "/run/yufeng/modelside.sock"

// DataplaneControlPort 是技术人员手动启动本机监督器时使用的管理端口。
const DataplaneControlPort = 19091

// ModelAlertThresholdDefault 是 Brain 写入基线签名模型档案的告警阈值。
const ModelAlertThresholdDefault = 0.9

// ModelReviewFloorDefault 是 Brain 写入基线签名模型档案的复核下限。
const ModelReviewFloorDefault = 0.5

// ResolveLongPoll 把客户端秒数收成等待时长。
// 省略或 0 用 def；超过 max 返回错误，调用方映射为 invalid_argument。
func ResolveLongPoll(seconds int32, def, max time.Duration) (time.Duration, error) {
	if seconds < 0 {
		return 0, errors.New("long_poll_seconds is invalid")
	}
	if seconds == 0 {
		return def, nil
	}
	wait := time.Duration(seconds) * time.Second
	if wait > max {
		return 0, errors.New("long_poll_seconds exceeds max")
	}
	return wait, nil
}

// CanaryMinUnits 返回进入 canary 所需的最少绑定单元数：ceil(100 / percent)。
// percent<=0 时按 CanaryPercentDefault 计算。
func CanaryMinUnits(percent int32) int {
	if percent <= 0 {
		percent = CanaryPercentDefault
	}
	return int((100 + percent - 1) / percent)
}

// DefaultChatModel 是引导未指定模型名时的缺省聊天模型（OpenAI 兼容名）。
//
// [初次配置引导]: ../../docs/glossary.md#onboarding
const DefaultChatModel = "grok-4-1-fast-non-reasoning"

// ChatProbeMaxTokens 是连通性探测的补全上限；只要非空文本。
const ChatProbeMaxTokens = 32

// ChatCompleteMaxTokens 是 CompleteChat 与工具结构化对象的补全上限。
// 32 个令牌会截在思考标签或半段结构化对象中，贾维斯无法解析。
const ChatCompleteMaxTokens = 1024

// ChatCompleteTimeout 是模型出网与 CompleteChat 写回的等待上限。
// 必须大于 HTTPWriteTimeout：补全比普通控制面写更慢。
const ChatCompleteTimeout = 60 * time.Second

// ModelGatewayStatsWindow 是 GetModelGateway 成功率与主机数的统计窗。
const ModelGatewayStatsWindow = 24 * time.Hour

// ModelGatewayCallRetain 是调用记录保留上限，超时删除。
const ModelGatewayCallRetain = 7 * 24 * time.Hour

// CRSVersion 是冻结的开放全球应用安全项目核心规则集版本。
const CRSVersion = "4.25.0"

// CRSTarballSHA256 是官方核心规则集 v4.25.0 源码包的安全哈希算法 256 位摘要。
const CRSTarballSHA256 = "10370cad9f461e64abfa2fbf371a48e65715078b8acc81c3f5140c09618aee6a"

// CRSGoModule 是边缘实际装载的 Go 嵌入模块。
const CRSGoModule = "github.com/corazawaf/coraza-coreruleset/v4@v4.25.0"

// CRSParanoia 是冻结的 paranoia 档（crs-setup.conf.example 默认值）。
const CRSParanoia = 1
