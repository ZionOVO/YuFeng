// 控制台客户端接口（ConsoleClient）：页面与状态管理只依赖本接口。
// 方法名与 docs/api.md §17.4 的页面 → RPC 映射一一对应；运行时实现只有
// ConnectClient（connect.ts），按 Connect JSON 协议直连 brain。
// 待 proto 的 Connect-ES 生成代码落地后，ConnectClient 内部可替换为生成客户端，页面不变（docs/api.md §17.8）。

import type {
  AccessMode,
  AssetDetail,
  AssetPatch,
  AuditEntry,
  ChainVerification,
  Criticality,
  DashboardSummary,
  Event,
  EventKind,
  GateOutcome,
  LoginConfig,
  Release,
  ReleaseState,
  ReleaseStats,
  Session,
  Tier,
  TimelineEntry,
  User,
  UserPatch,
  UserRole,
  UserState,
  Verdict,
  BindingRef,
  ChatMessage,
  EffectiveAccess,
  Grant,
  ModelDialect,
  ModelGateway,
  Onboarding,
  ProposeArtifactRequest,
  ApprovalView,
  CaseActivity,
  CaseResolution,
  DefenseModule,
  InvestigationCase,
  InvestigationCaseState,
  ManagedAgentProfile,
  AgentProfileInput,
  AgentProfileState,
  WorkerRecord,
  WorkerEnrollmentDecision,
  WorkerEnrollmentRecord,
  TrafficReviewMode,
  TrafficReviewPolicyStatus,
  ModelIngressWindow,
  ModelIngressWindowStatus,
  ModelProfile,
  EdgeEnrollment,
  EventDetail,
} from './types'

export type EdgeEnrollmentInput = {
  unitId: string
  assetId: string
  posture: 'INGRESS_POSTURE_REVERSE_PROXY' | 'INGRESS_POSTURE_EXT_AUTHZ'
  listenAddress: string
  upstreamUrl: string
  trafficKey: string
  trustedProxyCidrs: string[]
  modelProfile: ModelProfile
  modelIngressWindow: ModelIngressWindow
}

/** 分页请求：pageSize 默认 50、上限 200；pageToken 不透明，只回传不解析（docs/api.md §0.6）。 */
export interface PageQuery {
  pageSize?: number
  pageToken?: string
}

/** 分页响应：nextPageToken 为空串表示没有下一页。 */
export interface Page<T> {
  items: T[]
  nextPageToken: string
}

export interface ListEventsFilter {
  assetId?: string
  releaseId?: string
  verdict?: Verdict
  kind?: EventKind
  since?: string
  until?: string
  /** 路径 / 规则关键词。 */
  query?: string
}

export interface ListReleasesFilter {
  states?: ReleaseState[]
  assetId?: string
  /** 匹配 releaseId / artifactId / createdBy。 */
  query?: string
}

export interface ListAssetsFilter {
  query?: string
  /** proto 中该过滤字段是 string，传 Criticality 枚举名（如 'CRITICALITY_P0'）。 */
  criticality?: Criticality
}

export interface ListUsersFilter {
  query?: string
  role?: UserRole
  state?: UserState
}

export interface ListAuditFilter {
  /** 支持 release / asset / unit 等对象类型。 */
  objectType?: string
  objectId?: string
  actor?: string
  since?: string
  until?: string
}

export interface ListCasesFilter {
  assetId?: string
  moduleId?: string
  state?: InvestigationCaseState
}

export interface ConsoleClient {
  /* ----- AuthService（公开域 + 自助） ----- */
  login(req: { username: string; password: string }): Promise<Session>
  logout(): Promise<void>
  /** 返回 user，并把 GetMe.access 写进会话侧：页面应再调 getMyAccess 或读 Session.access。 */
  getMe(): Promise<User>
  getMyAccess(): Promise<EffectiveAccess>
  changePassword(req: { oldPassword: string; newPassword: string }): Promise<void>
  getLoginConfig(): Promise<LoginConfig>

  /* ----- UserService（仅 ADMIN） ----- */
  createUser(req: { username: string; password: string; displayName: string; role: UserRole }): Promise<User>
  listUsers(filter?: ListUsersFilter, page?: PageQuery): Promise<Page<User>>
  getUser(userId: string): Promise<User>
  updateUser(userId: string, patch: UserPatch): Promise<User>
  /** 软删除（state=DELETED）；不可删除最后一个 ACTIVE ADMIN。 */
  deleteUser(userId: string): Promise<User>
  adminResetPassword(userId: string, newPassword: string, revokeSessions?: boolean): Promise<User>

  /* ----- GrantService（docs/api.md §6.1） ----- */
  listGrants(subjectUserId?: string): Promise<Grant[]>
  putGrant(req: { subjectUserId: string; tools: string[]; bindings: BindingRef[] }): Promise<Grant>
  revokeGrant(grantId: string): Promise<void>

  /* ----- AssetService ----- */
  listAssets(filter?: ListAssetsFilter, page?: PageQuery): Promise<Page<AssetDetail>>
  getAsset(assetId: string): Promise<AssetDetail>
  createAsset(input: { displayName: string; accessMode?: AccessMode; criticality?: Criticality; maxAutoTier?: Tier }): Promise<AssetDetail>
  updateAsset(assetId: string, patch: AssetPatch, expectedUpdatedAt?: string): Promise<AssetDetail>
  deleteAsset(assetId: string): Promise<void>
  attachUnit(assetId: string, unitId: string): Promise<AssetDetail>
  detachUnit(assetId: string, unitId: string): Promise<AssetDetail>
  putEdgeEnrollment(req: EdgeEnrollmentInput): Promise<EdgeEnrollment>
  getEdgeEnrollment(assetId: string, unitId: string): Promise<EdgeEnrollment>
  getTrafficReviewPolicy(assetId: string): Promise<TrafficReviewPolicyStatus>
  updateTrafficReviewPolicy(assetId: string, mode: TrafficReviewMode, expectedGenerationId?: string): Promise<TrafficReviewPolicyStatus>
  getModelIngressWindow(assetId: string, unitId: string): Promise<ModelIngressWindowStatus>
  updateModelIngressWindow(assetId: string, unitId: string, desired: ModelIngressWindow, expectedListenPlanVersion?: string): Promise<ModelIngressWindowStatus>

  /* ----- ConsoleService ----- */
  dashboard(): Promise<DashboardSummary>
  listEvents(filter?: ListEventsFilter, page?: PageQuery): Promise<Page<Event>>
  getEvent(eventId: string): Promise<EventDetail>

  /* ----- GovernService（写操作均带 Idempotency-Key） ----- */
  /** 门禁不通过不是错误：返回的 replayReport.passed === false，状态留在 draft。 */
  gateArtifact(releaseId: string, opts?: { corpusRef?: string; budget?: string }): Promise<GateOutcome>
  startShadow(releaseId: string): Promise<Release>
  /** canaryPercent 范围 1–25，缺省 5。门槛不满足抛 failed_precondition（ApiError.gateChecks）。 */
  promoteCanary(releaseId: string, canaryPercent?: number): Promise<Release>
  promoteEnforce(releaseId: string): Promise<Release>
  /** shadow/canary/enforce → retired，retireReason=ROLLBACK。 */
  rollbackRelease(releaseId: string, reason: string): Promise<Release>
  /** 操作域只允许人工退休（retireReason=MANUAL）。 */
  retireRelease(releaseId: string, reason: string): Promise<Release>
  /** 误报举报：release 必须 canary/enforce，事件 verdict=BLOCK 且属于该 release。 */
  denyFeedback(releaseId: string, eventId: string, note: string): Promise<Release>
  getRelease(releaseId: string): Promise<Release>
  listReleases(filter?: ListReleasesFilter, page?: PageQuery): Promise<Page<Release>>
  getReleaseTimeline(releaseId: string, page?: PageQuery): Promise<Page<TimelineEntry>>
  getReleaseStats(releaseId: string): Promise<ReleaseStats>

  /* ----- AuditService ----- */
  listAuditEntries(filter?: ListAuditFilter, page?: PageQuery): Promise<Page<AuditEntry>>
  verifyChain(startSequence: string, endSequence: string): Promise<ChainVerification>

  /* ----- OnboardingService（docs/api.md §19） ----- */
  getOnboarding(): Promise<Onboarding>
  putModelConfig(req: { baseUrl: string; secret: string; model?: string; dialect?: ModelDialect }): Promise<void>
  testModelConnectivity(): Promise<void>
  completeOnboarding(): Promise<void>

  /* ----- ModelGatewayService（docs/api.md §19.4；仅管理员；引导完成后） ----- */
  getModelGateway(): Promise<ModelGateway>
  updateModelGateway(req: { baseUrl: string; secret?: string; model?: string; dialect?: ModelDialect }): Promise<ModelGateway>
  probeModelGateway(): Promise<{ ok: boolean; lastError: string }>

  /* ----- GovernService.ProposeArtifact（生产只收 intent） ----- */
  proposeArtifact(req: ProposeArtifactRequest): Promise<Release>

  /* ----- SessionService（只认 Login.token + 属主，不查授予 Tools） ----- */
  createSession(req?: { title?: string }): Promise<{ sessionId: string }>
  sendMessage(req: { sessionId: string; content: string }): Promise<{ messageSequence: string }>
  pollMessages(req: { sessionId: string; cursor?: string; longPollSeconds?: number }): Promise<{
    messages: ChatMessage[]
    nextCursor: string
  }>
  listMessages(req: { sessionId: string }, page?: PageQuery): Promise<Page<ChatMessage>>

  /* ----- AgentProfileService（逻辑调查岗位，不创建进程身份） ----- */
  listAgentProfiles(page?: PageQuery): Promise<Page<ManagedAgentProfile>>
  createAgentProfile(req: AgentProfileInput): Promise<ManagedAgentProfile>
  updateAgentProfile(req: AgentProfileInput & { agentId: string; state: AgentProfileState }): Promise<ManagedAgentProfile>
  deleteAgentProfile(agentId: string): Promise<void>
  batchUpdateAgentProfiles(req: { agentIds: string[]; tools: string[]; bindings: BindingRef[] }): Promise<ManagedAgentProfile[]>

  /* ----- 案件、审批、模块目录与执行池 ----- */
  listCases(filter?: ListCasesFilter, page?: PageQuery): Promise<Page<InvestigationCase>>
  getCase(caseId: string): Promise<InvestigationCase>
  pollCaseActivities(req: { caseId: string; afterSequence?: string; longPollSeconds?: number }): Promise<{
    activities: CaseActivity[]
    nextAfterSequence: string
  }>
  resolveCase(req: { caseId: string; resolution: CaseResolution; note?: string }): Promise<InvestigationCase>
  reopenCase(req: { caseId: string; note?: string }): Promise<InvestigationCase>
  recordCaseFeedback(req: { caseId: string; resolution: CaseResolution; note?: string }): Promise<InvestigationCase>
  getApproval(approvalId: string): Promise<ApprovalView>
  decideApproval(req: { approvalId: string; approved: boolean; reason?: string }): Promise<{ approvalId: string; state: string }>
  listModules(): Promise<DefenseModule[]>
  listWorkers(page?: PageQuery): Promise<Page<WorkerRecord>>
  listWorkerEnrollments(state?: string, page?: PageQuery): Promise<Page<WorkerEnrollmentRecord>>
  decideWorkerEnrollment(req: { enrollmentId: string; approved: boolean; bindings: string[]; maxConcurrency?: number }): Promise<WorkerEnrollmentDecision>
}
