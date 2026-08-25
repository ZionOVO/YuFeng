// 控制台薄领域类型：与 docs/api.md 的 protojson 表示一一对齐（字段名 lowerCamelCase）。
// 约定：int64/uint64 在 JSON 中是 string；Timestamp 是 RFC3339 字符串；Duration 是 "300s" 形式；
// 枚举一律用 proto 全名（如 'RELEASE_STATE_CANARY'）。本文件只定义页面真正用到的字段，
// 不是 proto 类型的完整复制（见 docs/api.md §17.8）。

/* ---------- 共享枚举（yufeng.common.v1） ---------- */

export type UserRole = 'USER_ROLE_UNSPECIFIED' | 'USER_ROLE_ADMIN' | 'USER_ROLE_OPERATOR' | 'USER_ROLE_VIEWER'
export type UserState = 'USER_STATE_UNSPECIFIED' | 'USER_STATE_ACTIVE' | 'USER_STATE_DISABLED' | 'USER_STATE_DELETED'

export type ReleaseState =
  | 'RELEASE_STATE_UNSPECIFIED'
  | 'RELEASE_STATE_DRAFT'
  | 'RELEASE_STATE_SIGNED'
  | 'RELEASE_STATE_SHADOW'
  | 'RELEASE_STATE_CANARY'
  | 'RELEASE_STATE_ENFORCE'
  | 'RELEASE_STATE_RETIRED'

export type RetireReason =
  | 'RETIRE_REASON_UNSPECIFIED'
  | 'RETIRE_REASON_ROLLBACK'
  | 'RETIRE_REASON_MANUAL'
  | 'RETIRE_REASON_TTL'
  | 'RETIRE_REASON_SUPERSEDED'

export type ReleaseMode =
  | 'RELEASE_MODE_UNSPECIFIED'
  | 'RELEASE_MODE_SHADOW'
  | 'RELEASE_MODE_CANARY'
  | 'RELEASE_MODE_ENFORCE'

export type Tier =
  | 'TIER_UNSPECIFIED'
  | 'TIER_L0_REPORT'
  | 'TIER_L1_TRAFFIC'
  | 'TIER_L2_RUNTIME'
  | 'TIER_L3_COLD_PATCH'

export type AccessMode =
  | 'ACCESS_MODE_UNSPECIFIED'
  | 'ACCESS_MODE_EMBEDDED'
  | 'ACCESS_MODE_REMOTE'
  | 'ACCESS_MODE_NETWORK'

export type Criticality = 'CRITICALITY_UNSPECIFIED' | 'CRITICALITY_P0' | 'CRITICALITY_P1' | 'CRITICALITY_P2'

/* ---------- 认证与用户（yufeng.auth.v1 / yufeng.user.v1） ---------- */

export interface User {
  userId: string
  username: string
  displayName: string
  role: UserRole
  state: UserState
  createdAt?: string
  updatedAt?: string
  lastLoginAt?: string
}

/** 对象范围：kind+id，禁止通配符（docs/api.md §5.3 / §6.1）。 */
export type BindingKind = 'asset' | 'unit' | 'release'

export interface BindingRef {
  kind: BindingKind
  id: string
}

/** 授予表即时展开的生效权限。前端按钮只看这个，不看 user.role。 */
export interface EffectiveAccess {
  tools: string[]
  bindings: BindingRef[]
}

/** 引导状态：线上只许 proto 全名（docs/api.md §19）。 */
export type OnboardingState =
  | 'ONBOARDING_STATE_UNSPECIFIED'
  | 'ONBOARDING_STATE_PENDING'
  | 'ONBOARDING_STATE_MODEL_CONFIGURED'
  | 'ONBOARDING_STATE_MODEL_LIVE'
  | 'ONBOARDING_STATE_EDGE_LIVE'
  | 'ONBOARDING_STATE_COMPLETED'
  | 'ONBOARDING_STATE_FAILED'

export type ModelDialect =
  | 'MODEL_DIALECT_UNSPECIFIED'
  | 'MODEL_DIALECT_OPENAI_CHAT'
  | 'MODEL_DIALECT_OPENAI_RESPONSES'
  | 'MODEL_DIALECT_CLAUDE_MESSAGES'

/** GetOnboarding 投影，无密钥明文。 */
export interface Onboarding {
  state: OnboardingState
  baseUrl: string
  model: string
  dialect?: ModelDialect
  hasSecret: boolean
  secretHint: string
  jarvisOnline: boolean
  lastError: string
  updatedAt?: string
}

/** 模型网关服务状态：线上只许 proto 全名（docs/api.md §19.4）。 */
export type ModelGatewayStatus =
  | 'MODEL_GATEWAY_STATUS_UNSPECIFIED'
  | 'MODEL_GATEWAY_STATUS_UNCONFIGURED'
  | 'MODEL_GATEWAY_STATUS_READY'
  | 'MODEL_GATEWAY_STATUS_LIVE'
  | 'MODEL_GATEWAY_STATUS_DEGRADED'
  | 'MODEL_GATEWAY_STATUS_DOWN'

/** 统计窗内一个主机的调用汇总。计数是 protojson int64 字符串。 */
export interface ModelProviderStat {
  host: string
  callsTotal: string
  callsOk: string
  lastAt?: string
}

/** GetModelGateway 投影：当前槽加近窗统计，无密钥明文。 */
export interface ModelGateway {
  baseUrl: string
  model: string
  dialect?: ModelDialect
  hasSecret: boolean
  secretHint: string
  status: ModelGatewayStatus
  providerCount: number
  windowSeconds: string
  callsTotal: string
  callsOk: string
  lastCallAt?: string
  lastError: string
  providers: ModelProviderStat[]
}

export const TOOL = {
  consoleRead: 'console.read',
  governPropose: 'govern.propose',
  governGate: 'govern.gate',
  governStartShadow: 'govern.start_shadow',
  governPromoteCanary: 'govern.promote_canary',
  governPromoteEnforce: 'govern.promote_enforce',
  governRollback: 'govern.rollback',
  governRetire: 'govern.retire',
  governDenyFeedback: 'govern.deny_feedback',
  assetCreate: 'asset.create',
  assetUpdate: 'asset.update',
  assetDelete: 'asset.delete',
  assetAttach: 'asset.attach',
  assetDetach: 'asset.detach',
  runCreate: 'run.create',
  caseRead: 'case.read',
  caseManage: 'case.manage',
  evidenceApprove: 'evidence.approve',
  workerEnroll: 'worker.enroll',
  workerCapacityApprove: 'worker.capacity.approve',
  agentManage: 'agent.manage',
  catalogManage: 'catalog.manage',
  grantWrite: 'grant.write',
  userAdmin: 'user.admin',
} as const

export type ToolName = (typeof TOOL)[keyof typeof TOOL]

export interface Grant {
  grantId: string
  subjectUserId: string
  tools: string[]
  bindings: BindingRef[]
  createdBy: string
  createdAt: string
  expiresAt?: string
}

/** 会话：Login 响应的持久化形状，写 sessionStorage（键 yufeng.session，见 docs/api.md §17.2）。 */
export interface Session {
  token: string
  expiresAt: string
  user: User
  access: EffectiveAccess
}

export interface LoginConfig {
  allowSelfRegistration: boolean
  passwordMinLength: number
  /** protojson Duration，如 "43200s"。 */
  sessionTtl: string
}

/** UpdateUser 允许修改的字段（见 docs/api.md §6：display_name、role、state）。 */
export interface UserPatch {
  displayName?: string
  role?: UserRole
  state?: UserState
}

/* ---------- 资产（yufeng.asset.v1） ---------- */

export type TransportKind = 'KIND_UNSPECIFIED' | 'KIND_LOCAL' | 'KIND_SSH' | 'KIND_VENDOR_API'

export interface Transport {
  kind: TransportKind
  endpoint: string
}

export interface CapabilityMatrix {
  kernelVersion: string
  bpfLsm: boolean
  seccomp: boolean
  nftables: boolean
  landlock: boolean
  packageManagers: string[]
}

export interface Asset {
  id: string
  displayName: string
  accessMode: AccessMode
  transports: Transport[]
  capabilities?: CapabilityMatrix
  criticality: Criticality
  maxAutoTier: Tier
  labels: Record<string, string>
  lastProbeAt?: string
  updatedAt?: string
}

/**
 * 资产详情。health 在 proto 中是 string（proto/yufeng/asset/v1/service.proto），
 * 期望值是 UnitHealth 枚举名（如 'UNIT_HEALTH_HEALTHY'）；前端按字符串宽松匹配，不做枚举强转。
 */
export interface AssetDetail {
  asset: Asset
  unitIds: string[]
  units: UnitProjection[]
  edgeEnrollments: EdgeEnrollment[]
  health: string
  activeReleaseCount: number
}

export interface ModelProfile {
  profileId: string
  modelGroup: string
  modelType: string
  modelVersion: string
  alertThreshold: number
  reviewFloor: number
  reviewWindowSeconds: number
  maxReviewPerUnit: number
  maxReviewPerRoute: number
  dedupeRule: 'MODEL_DEDUPE_RULE_METHOD_ROUTE_HIGHEST_SCORE'
  allowedHeaders: string[]
  maxBodyBytes: number
  reviewNewRoutes: boolean
  reviewInsufficientCoverage: boolean
}

export type EdgeEnrollmentStatus =
  | 'EDGE_ENROLLMENT_STATUS_UNSPECIFIED'
  | 'EDGE_ENROLLMENT_STATUS_WAITING_FOR_REGISTRATION'
  | 'EDGE_ENROLLMENT_STATUS_ONLINE'
  | 'EDGE_ENROLLMENT_STATUS_OUT_OF_SYNC'
  | 'EDGE_ENROLLMENT_STATUS_OFFLINE'

export interface EdgeEnrollment {
  assetId: string
  unitId: string
  posture: IngressPosture
  listenAddress: string
  upstreamUrl: string
  trafficKey: string
  trustedProxyCidrs: string[]
  modelProfile: ModelProfile
  modelIngressWindow: ModelIngressWindow
  modelsideId: string
  specificationDigest: string
  expectedListenPlanVersion: string
  expectedGenerationId: string
  expectedGenerationSeq: string
  status: EdgeEnrollmentStatus
  lastHeartbeatAt?: string
  currentListenPlanVersion: string
  currentGenerationId: string
  currentGenerationSeq: string
  modelsideStatus: EdgeEnrollmentStatus
  modelsideLastResultAt?: string
  modelProfileDigest: string
}

export type ProducerOutput =
  | 'PRODUCER_OUTPUT_UNSPECIFIED'
  | 'PRODUCER_OUTPUT_CRITICAL_EVENT'
  | 'PRODUCER_OUTPUT_ORDINARY_SAMPLE'
  | 'PRODUCER_OUTPUT_TICKET_FEATURES'

export type SensorType = 'SENSOR_TYPE_UNSPECIFIED' | 'SENSOR_TYPE_HTTP' | 'SENSOR_TYPE_CORAZA'

export interface ModelIngressWindow {
  maxItems: number
  maxRetainedBytes: string
  maxQueueAge: string
}

export type ModelIngressWindowState =
  | 'MODEL_INGRESS_WINDOW_STATE_UNSPECIFIED'
  | 'MODEL_INGRESS_WINDOW_STATE_APPLIED'
  | 'MODEL_INGRESS_WINDOW_STATE_DEGRADED'
  | 'MODEL_INGRESS_WINDOW_STATE_CONVERGING'
  | 'MODEL_INGRESS_WINDOW_STATE_DISABLED'

export type ModelIngressDegradationReason =
  | 'MODEL_INGRESS_DEGRADATION_REASON_UNSPECIFIED'
  | 'MODEL_INGRESS_DEGRADATION_REASON_MAX_ITEMS'
  | 'MODEL_INGRESS_DEGRADATION_REASON_MAX_RETAINED_BYTES'
  | 'MODEL_INGRESS_DEGRADATION_REASON_MAX_QUEUE_AGE'

export interface ModelIngressDropCounters {
  evictedOldest: string
  expired: string
  itemTooLarge: string
  inFlightCapacity: string
  transportFailed: string
  modelsideRejected: string
  admissionBudget: string
}

export interface ProducerCapabilities {
  outputs: ProducerOutput[]
  projectionVersions: string[]
  postures: IngressPosture[]
  sensors: SensorType[]
  localEvidenceRing: boolean
  localAsyncBypass: boolean
  maxEventBatch: number
  maxInFlightRequests: number
  maxSpoolBytes: string
  maxEvidenceEntries: number
  modelIngressHardLimit?: ModelIngressWindow
  maxModelIngressBatchItems: number
}

export interface ProducerHealth {
  bufferedCriticalEvents: string
  bufferedOrdinarySamples: string
  droppedCriticalEvents: string
  droppedOrdinarySamples: string
  droppedLocalBypassItems: string
  projectionFailures: string
  healthyProjectionVersions: string[]
  effectiveModelIngressWindow?: ModelIngressWindow
  modelIngressWindowState: ModelIngressWindowState
  modelIngressDegradationReasons: ModelIngressDegradationReason[]
  modelIngressQueuedItems: string
  modelIngressQueuedBytes: string
  modelIngressInFlightItems: string
  modelIngressInFlightBytes: string
  modelIngressOldestAgeMillis: string
  modelIngressDrops: ModelIngressDropCounters
}

export interface UnitProjection {
  unitId: string
  kind: string
  version: string
  health: string
  capabilities: ProducerCapabilities
  producerHealth: ProducerHealth
  posture: IngressPosture
  trafficKey: string
  lastHeartbeatAt?: string
  currentGenerationId?: string
  currentGenerationSeq?: string
  currentListenPlanVersion?: string
}

export interface ModelIngressWindowStatus {
  assetId: string
  unitId: string
  desired: ModelIngressWindow
  effective?: ModelIngressWindow
  desiredListenPlanVersion: string
  appliedListenPlanVersion: string
  state: ModelIngressWindowState
  degradationReasons: ModelIngressDegradationReason[]
}

export type TrafficReviewMode =
  | 'TRAFFIC_REVIEW_MODE_UNSPECIFIED'
  | 'TRAFFIC_REVIEW_MODE_OFF'
  | 'TRAFFIC_REVIEW_MODE_STATISTICS_ONLY'
  | 'TRAFFIC_REVIEW_MODE_REDACTED_CASES'
  | 'TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL'
  | 'TRAFFIC_REVIEW_MODE_SHADOW_CANDIDATES'

export interface TrafficReviewPolicy {
  windowSeconds: number
  topRouteCells: number
  maxCandidatesPerWindow: number
  maxEvidenceBytes: number
  vaultMaxBytes: string
  evidenceTtlSeconds: string
  mode: TrafficReviewMode
}

export interface TrafficReviewPolicyStatus {
  policy: TrafficReviewPolicy
  generationId: string
  generationSeq: string
  policyDigest: string
  edgeSupported: boolean
}

/** UpdateAsset 允许修改的字段（见 docs/api.md §9）。 */
export interface AssetPatch {
  displayName?: string
  labels?: Record<string, string>
  criticality?: Criticality
  maxAutoTier?: Tier
  accessMode?: AccessMode
}

/* ---------- 事件（yufeng.event.v1） ---------- */

export type EventKind =
  | 'KIND_UNSPECIFIED'
  | 'KIND_TRAFFIC'
  | 'KIND_SENSOR'
  | 'KIND_INTEL'
  | 'KIND_AGENT'
  | 'KIND_MODEL_ALERT'
  | 'KIND_MODEL_REVIEW_SAMPLE'

export type Verdict =
  | 'VERDICT_UNSPECIFIED'
  | 'VERDICT_ALLOW'
  | 'VERDICT_BLOCK'
  | 'VERDICT_OBSERVE'
  | 'VERDICT_ESCALATE'

export interface HttpPayload {
  method: string
  path: string
  queryRedacted: string
  headersRedacted: Record<string, string>
  /** protojson bytes：base64 字符串。 */
  bodyRedacted: string
  srcPseudonym: string
  dst: string
  statusCode: number
  /** int64 → string（微秒）。 */
  latencyMicros: string
}

export interface AiPayload {
  provider: string
  model: string
  roleCounts: Record<string, number>
  toolCalls: { name: string; argsDigest: string }[]
}

export type InspectionSurface =
  | 'INSPECTION_SURFACE_UNSPECIFIED'
  | 'INSPECTION_SURFACE_PATH'
  | 'INSPECTION_SURFACE_QUERY'
  | 'INSPECTION_SURFACE_HEADER'
  | 'INSPECTION_SURFACE_BODY'

export type CoverageStatus =
  | 'COVERAGE_STATUS_UNSPECIFIED'
  | 'COVERAGE_STATUS_FULL'
  | 'COVERAGE_STATUS_PARTIAL'
  | 'COVERAGE_STATUS_ABSENT'
  | 'COVERAGE_STATUS_UNSUPPORTED'
  | 'COVERAGE_STATUS_ERROR'

export type ObservationState =
  | 'OBSERVATION_STATE_UNSPECIFIED'
  | 'OBSERVATION_STATE_SYNC_DETECTED'
  | 'OBSERVATION_STATE_SYNC_NO_DETECTION'
  | 'OBSERVATION_STATE_INSPECTION_PARTIAL'
  | 'OBSERVATION_STATE_INSPECTION_ERROR'

export type TriageReason =
  | 'TRIAGE_REASON_UNSPECIFIED'
  | 'TRIAGE_REASON_DETECTED_UNMITIGATED'
  | 'TRIAGE_REASON_DETECTED_UNMAPPED'
  | 'TRIAGE_REASON_SUSPECTED_MISS'
  | 'TRIAGE_REASON_INSPECTION_INCOMPLETE'
  | 'TRIAGE_REASON_DETECTOR_FAILURE'

export type IngressPosture =
  | 'INGRESS_POSTURE_UNSPECIFIED'
  | 'INGRESS_POSTURE_REVERSE_PROXY'
  | 'INGRESS_POSTURE_EXT_AUTHZ'
  | 'INGRESS_POSTURE_TAP_ALERT'
  | 'INGRESS_POSTURE_MIRROR_OBSERVE'

export type AttackClass =
  | 'ATTACK_CLASS_UNSPECIFIED'
  | 'ATTACK_CLASS_SQLI'
  | 'ATTACK_CLASS_XSS'
  | 'ATTACK_CLASS_PATH_TRAVERSAL'
  | 'ATTACK_CLASS_SSRF'
  | 'ATTACK_CLASS_CMDI'
  | 'ATTACK_CLASS_UNMAPPED'

export interface InspectionCoverage {
  target: InspectionSurface
  status: CoverageStatus
  inspectedBytes: string
  totalBytesKnown: string
}

export interface Detection {
  detectorId: string
  ruleId: string
  /** [0,1]。 */
  confidence: number
  message: string
  tier: Tier
  attackClass?: AttackClass
  anomalyScore?: number
  key?: DetectionKey
  rawTags?: string[]
  taxonomyVersion?: string
  matchedVariable?: string
  evidenceSpan?: string
  inspectionCoverageRef?: string
}

export interface ReleaseTrace {
  releaseId: string
  artifactId: string
  mode: ReleaseMode
  canaryPercent: number
  canarySelected: boolean
  matched: boolean
}

export interface Event {
  id: string
  occurredAt: string
  assetId: string
  source: string
  kind: EventKind
  verdict: Verdict
  http?: HttpPayload
  ai?: AiPayload
  detections: Detection[]
  labels: Record<string, string>
  unitId: string
  requestId: string
  releaseTraces: ReleaseTrace[]
  coverage: InspectionCoverage[]
  observation: ObservationState
  triageReason: TriageReason
  generationId: string
  generationSeq: string
  clusterId: string
  wouldHaveBlocked: boolean
  ingressPosture: IngressPosture
  trafficKey: string
}

export interface ModelInference {
  inferenceId: string
  eventId: string
  modelGroup: string
  modelType: string
  modelVersion: string
  threshold: number
  score: number
  attackClass: AttackClass
  taxonomyVersion: string
  recordedAt?: string
  modelProfileDigest: string
  requestId: string
  resultKind: string
}

export interface TriageDelivery {
  caseId: string
  instructionId: string
  handlerId: string
  kind: string
  status: string
  createdAt?: string
  acknowledgedAt?: string
}

export interface EventDetail {
  event: Event
  modelInferences: ModelInference[]
  triageDeliveries: TriageDelivery[]
}

/* ---------- 制品与发布（yufeng.artifact.v1 / yufeng.govern.v1） ---------- */

export type ArtifactKind =
  | 'KIND_UNSPECIFIED'
  | 'KIND_RULE'
  | 'KIND_PROFILE'
  | 'KIND_PROCEDURE'
  | 'KIND_SKILL'
  | 'KIND_TOOL_DESCRIPTOR'
  | 'KIND_BPF_OBJECT'
  | 'KIND_SECCOMP_PROFILE'
  | 'KIND_NFTABLES_RULES'

export interface Scope {
  assetIds: string[]
  routeSelector: string
}

export type ProposalKind = 'PROPOSAL_KIND_UNSPECIFIED' | 'PROPOSAL_KIND_POLICY' | 'PROPOSAL_KIND_SHAPE'

/** 检测键：生产提案意图的匹配键（选择器由服务端抄写）。 */
export interface DetectionKey {
  detectorId?: string
  detectorVersion?: string
  detectorManifestDigest?: string
  ruleId: string
  phase?: string
  targetLocation?: string
  targetSelector?: string
  normalizationProfileDigest?: string
}

/** 形状源文：仅 SUSPECTED_MISS。 */
export interface ShapeSource {
  methods?: string[]
  routeTemplate?: string
  pathPrefix?: string
  constraints?: Array<{
    selector: string
    minLen: number
    maxLen: number
    charset: 'ascii_print' | 'digit' | 'alpha' | 'hex' | 'uuid'
  }>
}

/** 生产提案意图；禁止 KIND_RULE / rules/v1（docs/api.md §18.1.2）。 */
export interface ProposalIntent {
  kind: ProposalKind
  clusterId?: string
  detectionKeys?: DetectionKey[]
  shapeSource?: ShapeSource
  methods?: string[]
  routeTemplate?: string
}

export interface ProposeArtifactRequest {
  intent: ProposalIntent
  scope: Scope
  ttl?: string
  evidenceRefs?: string[]
}

/** 会话消息（SessionService）；sequence 是 protojson int64 字符串。 */
export type SessionAttachmentKind =
  | 'SESSION_ATTACHMENT_KIND_UNSPECIFIED'
  | 'SESSION_ATTACHMENT_KIND_CASE'
  | 'SESSION_ATTACHMENT_KIND_APPROVAL'
  | 'SESSION_ATTACHMENT_KIND_RUN'
  | 'SESSION_ATTACHMENT_KIND_FINDING'
  | 'SESSION_ATTACHMENT_KIND_SHADOW_RELEASE'
  | 'SESSION_ATTACHMENT_KIND_WORKER_ENROLLMENT'
  | 'SESSION_ATTACHMENT_KIND_WORKER_CAPACITY'

export interface SessionAttachment {
  kind: SessionAttachmentKind
  refId: string
  moduleId: string
}

export interface ChatMessage {
  sequence: string
  sessionId: string
  sender: string
  content: string
  occurredAt?: string
  attachments?: SessionAttachment[]
}

/* ---------- 调查案件、审批与模块目录 ---------- */

export type InvestigationCaseState =
  | 'INVESTIGATION_CASE_STATE_UNSPECIFIED'
  | 'INVESTIGATION_CASE_STATE_OPEN'
  | 'INVESTIGATION_CASE_STATE_WAITING_EVIDENCE_APPROVAL'
  | 'INVESTIGATION_CASE_STATE_QUEUED'
  | 'INVESTIGATION_CASE_STATE_INVESTIGATING'
  | 'INVESTIGATION_CASE_STATE_FINDING_READY'
  | 'INVESTIGATION_CASE_STATE_SHADOW_OBSERVING'
  | 'INVESTIGATION_CASE_STATE_RESOLVED'
  | 'INVESTIGATION_CASE_STATE_FAILED'
  | 'INVESTIGATION_CASE_STATE_EVIDENCE_EXPIRED'

export type TrafficFindingDisposition =
  | 'TRAFFIC_FINDING_DISPOSITION_UNSPECIFIED'
  | 'TRAFFIC_FINDING_DISPOSITION_SUSPECTED_MALICIOUS'
  | 'TRAFFIC_FINDING_DISPOSITION_SUSPECTED_FALSE_POSITIVE'
  | 'TRAFFIC_FINDING_DISPOSITION_SUSPECTED_MISS'
  | 'TRAFFIC_FINDING_DISPOSITION_BENIGN'
  | 'TRAFFIC_FINDING_DISPOSITION_INSUFFICIENT_EVIDENCE'

export type CaseResolution =
  | 'CASE_RESOLUTION_UNSPECIFIED'
  | 'CASE_RESOLUTION_CONFIRMED_MALICIOUS'
  | 'CASE_RESOLUTION_FALSE_POSITIVE'
  | 'CASE_RESOLUTION_BENIGN'
  | 'CASE_RESOLUTION_INSUFFICIENT_EVIDENCE'
  | 'CASE_RESOLUTION_EVIDENCE_DENIED'
  | 'CASE_RESOLUTION_SHADOW_PUBLISHED'
  | 'CASE_RESOLUTION_FAILED'

export interface ReviewCandidate {
  candidateId: string
  windowId: string
  unitId: string
  assetId: string
  occurredAt?: string
  method: string
  routeTemplate: string
  riskScore: number
  riskReasons: string[]
  evidenceHandle: string
  evidenceDigest: string
  evidenceExpiresAt?: string
  baseline: boolean
  reviewMode?: string
}

export interface TrafficFinding {
  disposition: TrafficFindingDisposition
  confidence: number
  evidenceRefs: string[]
  attackClass: string
  routeTemplate: string
  selectors: string[]
  rationale: string
  optionalShapeDraft?: ShapeSource
}

export interface InvestigationCase {
  caseId: string
  moduleId: string
  assetId: string
  clusterId: string
  state: InvestigationCaseState
  priority: number
  title: string
  summary: string
  representatives: ReviewCandidate[]
  finding?: TrafficFinding
  shadowReleaseId: string
  assignedAgentId: string
  assignedAgentDisplayName: string
  resolution?: CaseResolution
  automationSuppressedReason?: string
  assignedRunId?: string
  assignedAgentConfigDigest?: string
  resolvedAt?: string
  createdAt?: string
  updatedAt?: string
}

export type CaseActivityKind =
  | 'CASE_ACTIVITY_KIND_UNSPECIFIED'
  | 'CASE_ACTIVITY_KIND_CREATED'
  | 'CASE_ACTIVITY_KIND_EVIDENCE_REQUESTED'
  | 'CASE_ACTIVITY_KIND_APPROVAL_DECIDED'
  | 'CASE_ACTIVITY_KIND_RUN_PROGRESS'
  | 'CASE_ACTIVITY_KIND_FINDING'
  | 'CASE_ACTIVITY_KIND_SHADOW_CANDIDATE'
  | 'CASE_ACTIVITY_KIND_RECOMMENDATION'
  | 'CASE_ACTIVITY_KIND_STATE_CHANGED'
  | 'CASE_ACTIVITY_KIND_APPROVAL_REQUESTED'

export interface CaseActivity {
  sequence: string
  caseId: string
  kind: CaseActivityKind
  refId: string
  summary: string
  occurredAt?: string
}

export interface ApprovalView {
  approvalId: string
  kind: 'APPROVAL_KIND_UNSPECIFIED' | 'APPROVAL_KIND_EVIDENCE' | 'APPROVAL_KIND_WORKER_CAPACITY'
  state: string
  caseId: string
  assetId: string
  workerId: string
  modelHost: string
  modelName: string
  modelConfigDigest: string
  allowedFields: string[]
  maxBytes: string
  previousCapacity: number
  requestedCapacity: number
  expiresAt?: string
  createdAt?: string
}

export interface DefenseModule {
  moduleId: string
  displayName: string
  version: string
  requiredProducerCapabilities: string[]
  caseActivitySchemas: string[]
  surfaces: string[]
  active: boolean
}

export type AgentProfileKind = 'AGENT_PROFILE_KIND_UNSPECIFIED' | 'AGENT_PROFILE_KIND_TRAFFIC_REVIEW'
export type AgentProfileState =
  | 'AGENT_PROFILE_STATE_UNSPECIFIED'
  | 'AGENT_PROFILE_STATE_ENABLED'
  | 'AGENT_PROFILE_STATE_DISABLED'
  | 'AGENT_PROFILE_STATE_TOMBSTONED'
export type AgentExecutionMode = 'AGENT_EXECUTION_MODE_UNSPECIFIED' | 'AGENT_EXECUTION_MODE_EPHEMERAL_RUN'

/** 受管 Agent 档案是 Jarvis 委派模板，不代表已安装的 Agent 进程。 */
export interface ManagedAgentProfile {
  agentId: string
  displayName: string
  kind: AgentProfileKind
  state: AgentProfileState
  tools: string[]
  bindings: BindingRef[]
  executionMode?: AgentExecutionMode
  configDigest?: string
  activeRunCount?: number
  lastRunAt?: string
  tombstonedAt?: string
  lastWorkerId?: string
  lastWorkerPlatform?: string
  /** 服务端按完整、未裁剪的资产范围计算；客户端不得自行推断。 */
  canManage: boolean
  createdBy: string
  createdAt?: string
  updatedAt?: string
}

export interface AgentProfileInput {
  displayName: string
  tools: string[]
  bindings: BindingRef[]
}

export interface WorkerRecord {
  workerId: string
  workerKind: string
  version: string
  operatingSystem: string
  architecture: string
  sandboxCapabilities: string[]
  maxConcurrency: number
  lastSeenAt?: string
  investigationEligible: boolean
  missingSandboxCapabilities: string[]
}

export interface WorkerEnrollmentRecord {
  enrollmentId: string
  workerId: string
  workerKind: string
  publicKeyFingerprint: string
  hostname: string
  operatingSystem: string
  architecture: string
  sandboxCapabilities: string[]
  state: string
  bindings: string[]
  maxConcurrency: number
  requestedAt?: string
  decidedAt?: string
  activationPublicKeyFingerprint?: string
  approvedManifestDigest?: string
  version?: string
  memoryCapacityBytes?: string
  logicalCpuCapacity?: number
  sandboxChallengeId?: string
}

export interface WorkerEnrollmentDecision {
  enrollmentId: string
  state: string
  activationBundleRef: string
  approvedManifestDigest: string
}

export interface ReplayReport {
  maliciousTotal: number
  maliciousBlocked: number
  benignTotal: number
  benignBlocked: number
  passed: boolean
  corpusRef: string
  managementTotal: number
  managementBlocked: number
}

/** 制品薄类型：页面只读元数据，payload/签名不展示。 */
export interface Artifact {
  id: string
  kind: ArtifactKind
  payloadSchema: string
  scope?: Scope
  /** protojson Duration。 */
  ttl: string
  supersedes: string
  evidenceRefs: string[]
  replayReport?: ReplayReport
  createdAt?: string
  createdBy: string
}

export interface Release {
  releaseId: string
  state: ReleaseState
  artifact?: Artifact
  proposedAt?: string
  signedAt?: string
  shadowStartedAt?: string
  canaryStartedAt?: string
  enforcedAt?: string
  retiredAt?: string
  retireReason: RetireReason
  createdBy: string
}

/** GateArtifact 的返回：不通过不是错误，看 replayReport.passed（docs/api.md §7.3）。 */
export interface GateOutcome {
  releaseId: string
  state: ReleaseState
  replayReport?: ReplayReport
}

export interface TimelineEntry {
  /** int64 → string。 */
  sequence: string
  releaseId: string
  fromState: ReleaseState
  toState: ReleaseState
  actor: string
  reason: string
  gateReportRef: string
  occurredAt: string
}

export interface ReleaseWindowStats {
  /** protojson Duration。 */
  duration: string
  /** 以下计数均为 uint64/int64 → string。 */
  requests: string
  blocks: string
  observes: string
  canarySelected: string
  denyFeedbackTotal: string
  upstream5xx: string
  p99Micros: string
}

export interface GuardStats {
  consecutiveBadWindows: number
  lastBadWindowAt?: string
  lastBadReasons: string[]
}

export interface ReleaseStats {
  releaseId: string
  state: ReleaseState
  shadow?: ReleaseWindowStats
  canary?: ReleaseWindowStats
  enforce?: ReleaseWindowStats
  guard?: GuardStats
  computedAt?: string
}

/* ---------- 门禁（yufeng.common.v1，经 failed_precondition details 返回） ---------- */

export interface GateCheck {
  gateKey: string
  passed: boolean
  required: string
  actual: string
  message: string
}

/* ---------- 审计（yufeng.audit.v1） ---------- */

export interface AuditEntry {
  /** int64 → string。 */
  sequence: string
  occurredAt: string
  actorType: string
  actorId: string
  action: string
  objectType: string
  objectId: string
  details: string
  previousHash: string
  entryHash: string
}

export interface ChainVerification {
  valid: boolean
  startHash: string
  endHash: string
  entriesChecked: number
}

/* ---------- 控制台总览（yufeng.console.v1） ---------- */

/** Dashboard 汇总。注意：proto 中 releasesByState 是 map<string,int64>，键为 ReleaseState 枚举名。 */
export interface DashboardSummary {
  assetsTotal: string
  degradedUnits: string
  releasesByState: Record<string, string>
  events24hTotal: string
  events24hBlocked: string
  pendingRetireSoon: string
  modelAlerts24h: string
}
