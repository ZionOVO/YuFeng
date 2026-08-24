// ConnectClient：按 Connect JSON 协议直连 brain 的 ConsoleClient 实现（docs/api.md §0.2、§17）。
// 路由 POST /{proto包名.服务名}/{方法名}。同源部署经相对路径调用。
// 开发期由 Vite 代理 /yufeng → brain。待 Connect-ES 落地后只替换本文件内部。

import { ApiError, apiErrorFromResponse, networkError } from './errors'
import type {
  ListAssetsFilter,
  ListAuditFilter,
  ListEventsFilter,
  ListReleasesFilter,
  ListUsersFilter,
  ListCasesFilter,
  Page,
  PageQuery,
} from './client'
import type {
  AccessMode,
  AssetDetail,
  AssetPatch,
  Criticality,
  Tier,
  AuditEntry,
  ChainVerification,
  ChatMessage,
  DashboardSummary,
  Event,
  GateOutcome,
  LoginConfig,
  ModelDialect,
  ModelGateway,
  ModelGatewayStatus,
  ModelProviderStat,
  Onboarding,
  ProposeArtifactRequest,
  Release,
  ReleaseStats,
  BindingRef,
  EffectiveAccess,
  Grant,
  Session,
  TimelineEntry,
  User,
  UserPatch,
  UserRole,
  ApprovalView,
  CaseActivity,
  CaseResolution,
  DefenseModule,
  InvestigationCase,
  ManagedAgentProfile,
  AgentProfileInput,
  AgentProfileState,
  WorkerRecord,
  WorkerEnrollmentDecision,
  WorkerEnrollmentRecord,
  TrafficReviewMode,
  TrafficReviewPolicyStatus,
} from './types'
import type { ConsoleClient, EdgeDeploymentCoordinates, EdgeDeploymentSpecification } from './client'
import {
  normalizeApprovalView,
  normalizeAssetDetail,
  normalizeCaseActivity,
  normalizeDashboard,
  normalizeDefenseModule,
  normalizeEvent,
  normalizeEffectiveAccess,
  normalizeGrant,
  normalizeInvestigationCase,
  normalizeManagedAgentProfile,
  normalizeRelease,
  normalizeReleaseStats,
  normalizeSession,
  normalizeTrafficReviewPolicyStatus,
  normalizeWorkerEnrollmentDecision,
  normalizeWorkerEnrollmentRecord,
  normalizeWorkerRecord,
  normalizeUser,
} from './normalize'
import { SessionLongPollDefault, SessionLongPollMax } from './limits'

export interface ConnectClientOptions {
  /** 默认 ''（同源相对路径，见 docs/api.md §17.1）。 */
  baseUrl?: string
  /** 每次请求调用；返回 null 表示未登录（公开接口除外会得到 unauthenticated）。 */
  getToken: () => string | null
  /** 收到 unauthenticated 时回调（清会话 + 回登录页由 AuthProvider 负责）。 */
  onUnauthenticated: () => void
  /** 测试注入用。 */
  fetchFn?: typeof fetch
}

/** protojson 请求体：剥离 undefined 字段后序列化。 */
function encode(body: Record<string, unknown>): string {
  return JSON.stringify(body, (_k, v) => (v === undefined ? undefined : v))
}

function int64String(v: unknown): string {
  if (typeof v === 'string' && v !== '') return v
  if (typeof v === 'number' && Number.isFinite(v)) return String(Math.trunc(v))
  return '0'
}

function normalizeModelGateway(raw: Partial<ModelGateway> & { providers?: Array<Partial<ModelProviderStat>> }): ModelGateway {
  const status = raw.status
  const known: ModelGatewayStatus[] = [
    'MODEL_GATEWAY_STATUS_UNSPECIFIED',
    'MODEL_GATEWAY_STATUS_UNCONFIGURED',
    'MODEL_GATEWAY_STATUS_READY',
    'MODEL_GATEWAY_STATUS_LIVE',
    'MODEL_GATEWAY_STATUS_DEGRADED',
    'MODEL_GATEWAY_STATUS_DOWN',
  ]
  return {
    baseUrl: raw.baseUrl ?? '',
    model: raw.model ?? '',
    dialect: raw.dialect === 'MODEL_DIALECT_OPENAI_RESPONSES' || raw.dialect === 'MODEL_DIALECT_CLAUDE_MESSAGES' || raw.dialect === 'MODEL_DIALECT_OPENAI_CHAT' ? raw.dialect : 'MODEL_DIALECT_OPENAI_CHAT',
    hasSecret: raw.hasSecret === true,
    secretHint: raw.secretHint ?? '',
    status: status !== undefined && known.includes(status) ? status : 'MODEL_GATEWAY_STATUS_UNSPECIFIED',
    providerCount: typeof raw.providerCount === 'number' ? raw.providerCount : Number(raw.providerCount ?? 0) || 0,
    windowSeconds: int64String(raw.windowSeconds),
    callsTotal: int64String(raw.callsTotal),
    callsOk: int64String(raw.callsOk),
    lastCallAt: raw.lastCallAt,
    lastError: raw.lastError ?? '',
    providers: (raw.providers ?? []).map((p) => ({
      host: p.host ?? '',
      callsTotal: int64String(p.callsTotal),
      callsOk: int64String(p.callsOk),
      lastAt: p.lastAt,
    })),
  }
}

export class ConnectClient implements ConsoleClient {
  private readonly baseUrl: string
  private readonly getToken: () => string | null
  private readonly onUnauthenticated: () => void
  private readonly fetchFn: typeof fetch
  /** 同一次用户重试复用 Idempotency-Key：按「服务/方法/请求体」记住键，成功后再清。 */
  private readonly idemKeys = new Map<string, string>()

  constructor(opts: ConnectClientOptions) {
    this.baseUrl = opts.baseUrl ?? ''
    this.getToken = opts.getToken
    this.onUnauthenticated = opts.onUnauthenticated
    this.fetchFn = opts.fetchFn ?? fetch.bind(globalThis)
  }

  /**
   * 统一调用入口。
   * auth=false 仅用于公开域（Login / GetLoginConfig）；idempotent=true 时按请求摘要
   * 复用 Idempotency-Key（同一次用户重试不得每次 randomUUID，docs/api.md §17.7）。
   */
  private async call<T>(
    service: string,
    method: string,
    req: Record<string, unknown>,
    opts: { auth?: boolean; idempotent?: boolean; timeoutMs?: number } = {},
  ): Promise<T> {
    const headers: Record<string, string> = {
      'Connect-Protocol-Version': '1',
      'Content-Type': 'application/json',
    }
    if (opts.auth !== false) {
      const token = this.getToken()
      if (token !== null) headers.Authorization = `Bearer ${token}`
    }
    const body = encode(req)
    let digest = ''
    if (opts.idempotent === true) {
      digest = `${service}/${method}:${body}`
      let key = this.idemKeys.get(digest)
      if (key === undefined) {
        key = crypto.randomUUID()
        this.idemKeys.set(digest, key)
      }
      headers['Idempotency-Key'] = key
    }

    let res: Response
	const controller = opts.timeoutMs !== undefined ? new AbortController() : undefined
	const timeout =
		controller !== undefined
			? setTimeout(() => controller.abort(new DOMException('request timed out', 'TimeoutError')), opts.timeoutMs)
			: undefined
    try {
      res = await this.fetchFn(`${this.baseUrl}/${service}/${method}`, {
        method: 'POST',
        headers,
        body,
		signal: controller?.signal,
      })
    } catch (err) {
      throw networkError(err)
	} finally {
		if (timeout !== undefined) clearTimeout(timeout)
    }
    if (!res.ok) {
      const apiErr: ApiError = await apiErrorFromResponse(res)
      if (apiErr.code === 'unauthenticated' && opts.auth !== false) {
        this.onUnauthenticated()
      }
      throw apiErr
    }
    if (digest !== '') this.idemKeys.delete(digest)
    return (await res.json()) as T
  }

  /* ----- AuthService ----- */

  async login(req: { username: string; password: string }): Promise<Session> {
    return normalizeSession(await this.call('yufeng.auth.v1.AuthService', 'Login', req, { auth: false }))
  }

  async logout(): Promise<void> {
    await this.call('yufeng.auth.v1.AuthService', 'Logout', {})
  }

  async getMe(): Promise<User> {
    const res = await this.call<{ user?: unknown }>('yufeng.auth.v1.AuthService', 'GetMe', {})
    return normalizeUser(res.user)
  }

  async getMyAccess(): Promise<EffectiveAccess> {
    const res = await this.call<{ access?: unknown }>('yufeng.auth.v1.AuthService', 'GetMe', {})
    return normalizeEffectiveAccess(res.access)
  }

  async changePassword(req: { oldPassword: string; newPassword: string }): Promise<void> {
    await this.call('yufeng.auth.v1.AuthService', 'ChangePassword', req)
  }

  async getLoginConfig(): Promise<LoginConfig> {
    return this.call<LoginConfig>('yufeng.auth.v1.AuthService', 'GetLoginConfig', {}, { auth: false })
  }

  /* ----- UserService（仅 ADMIN；写操作带 Idempotency-Key，docs/api.md §6） ----- */

  async createUser(req: { username: string; password: string; displayName: string; role: UserRole }): Promise<User> {
    const res = await this.call<{ user: User }>('yufeng.user.v1.UserService', 'CreateUser', req, { idempotent: true })
    return normalizeUser(res.user)
  }

  async listUsers(filter: ListUsersFilter = {}, page: PageQuery = {}): Promise<Page<User>> {
    const res = await this.call<{ users?: User[]; nextPageToken?: string }>('yufeng.user.v1.UserService', 'ListUsers', {
      query: filter.query,
      role: filter.role,
      state: filter.state,
      pageSize: page.pageSize,
      pageToken: page.pageToken,
    })
    return { items: (res.users ?? []).map(normalizeUser), nextPageToken: res.nextPageToken ?? '' }
  }

  async getUser(userId: string): Promise<User> {
    const res = await this.call<{ user: User }>('yufeng.user.v1.UserService', 'GetUser', { userId })
    return normalizeUser(res.user)
  }

  async updateUser(userId: string, patch: UserPatch): Promise<User> {
    const res = await this.call<{ user: User }>(
      'yufeng.user.v1.UserService',
      'UpdateUser',
      { userId, user: patch, updateMask: Object.keys(patch).join(',') },
      { idempotent: true },
    )
    return normalizeUser(res.user)
  }

  async deleteUser(userId: string): Promise<User> {
    const res = await this.call<{ user: User }>('yufeng.user.v1.UserService', 'DeleteUser', { userId }, { idempotent: true })
    return normalizeUser(res.user)
  }

  async adminResetPassword(userId: string, newPassword: string, revokeSessions = true): Promise<User> {
    const res = await this.call<{ user: User }>(
      'yufeng.user.v1.UserService',
      'AdminResetPassword',
      { userId, newPassword, revokeSessions },
      { idempotent: true },
    )
    return normalizeUser(res.user)
  }

  /* ----- AssetService ----- */

  async listAssets(filter: ListAssetsFilter = {}, page: PageQuery = {}): Promise<Page<AssetDetail>> {
    const res = await this.call<{ assets?: AssetDetail[]; nextPageToken?: string }>('yufeng.asset.v1.AssetService', 'ListAssets', {
      query: filter.query,
      criticality: filter.criticality,
      pageSize: page.pageSize,
      pageToken: page.pageToken,
    })
    return { items: (res.assets ?? []).map(normalizeAssetDetail), nextPageToken: res.nextPageToken ?? '' }
  }

  async getAsset(assetId: string): Promise<AssetDetail> {
    const res = await this.call<{ asset: AssetDetail }>('yufeng.asset.v1.AssetService', 'GetAsset', { assetId })
    return normalizeAssetDetail(res.asset)
  }

  async createAsset(input: {
    displayName: string
    accessMode?: AccessMode
    criticality?: Criticality
    maxAutoTier?: Tier
  }): Promise<AssetDetail> {
    const res = await this.call<{ asset?: unknown }>(
      'yufeng.asset.v1.AssetService',
      'CreateAsset',
      { asset: input },
      { idempotent: true },
    )
    return normalizeAssetDetail({ asset: res.asset, unitIds: [], health: '', activeReleaseCount: 0 })
  }

  async updateAsset(assetId: string, patch: AssetPatch, expectedUpdatedAt?: string): Promise<AssetDetail> {
    // docs/api.md §9：只允许操作方字段；capabilities/lastProbeAt 由单元探针上报
    const body: Record<string, unknown> = {
      assetId,
      asset: patch,
      // FieldMask 的 protojson 字符串使用 lowerCamel 字段名；下划线会被服务端拒绝。
      updateMask: Object.keys(patch).join(','),
    }
    if (expectedUpdatedAt !== undefined && expectedUpdatedAt !== '') {
      body.expectedUpdatedAt = expectedUpdatedAt
    }
    const res = await this.call<{ asset?: unknown }>('yufeng.asset.v1.AssetService', 'UpdateAsset', body)
    return normalizeAssetDetail({ asset: res.asset, unitIds: [], health: '', activeReleaseCount: 0 })
  }

  async deleteAsset(assetId: string): Promise<void> {
    await this.call('yufeng.asset.v1.AssetService', 'DeleteAsset', { assetId }, { idempotent: true })
  }

  async attachUnit(assetId: string, unitId: string): Promise<AssetDetail> {
    const res = await this.call<{ asset: AssetDetail }>(
      'yufeng.asset.v1.AssetService',
      'AttachUnit',
      { assetId, unitId },
      { idempotent: true },
    )
    return normalizeAssetDetail(res.asset)
  }

  async detachUnit(assetId: string, unitId: string): Promise<AssetDetail> {
    const res = await this.call<{ asset: AssetDetail }>(
      'yufeng.asset.v1.AssetService',
      'DetachUnit',
      { assetId, unitId },
      { idempotent: true },
    )
    return normalizeAssetDetail(res.asset)
  }

  async getTrafficReviewPolicy(assetId: string): Promise<TrafficReviewPolicyStatus> {
	const res = await this.call<{ status: TrafficReviewPolicyStatus }>(
	  'yufeng.asset.v1.AssetService',
	  'GetTrafficReviewPolicy',
	  { assetId },
	)
	return normalizeTrafficReviewPolicyStatus(res.status)
  }

  async updateTrafficReviewPolicy(assetId: string, mode: TrafficReviewMode, expectedGenerationId = ''): Promise<TrafficReviewPolicyStatus> {
	const res = await this.call<{ status: TrafficReviewPolicyStatus }>(
	  'yufeng.asset.v1.AssetService',
	  'UpdateTrafficReviewPolicy',
	  { assetId, mode, expectedGenerationId },
	  { idempotent: true },
	)
	return normalizeTrafficReviewPolicyStatus(res.status)
  }

  /* ----- ConsoleService ----- */

  async dashboard(): Promise<DashboardSummary> {
    return normalizeDashboard(await this.call<DashboardSummary>('yufeng.console.v1.ConsoleService', 'Dashboard', {}))
  }

  async listEvents(filter: ListEventsFilter = {}, page: PageQuery = {}): Promise<Page<Event>> {
    const res = await this.call<{ events?: Event[]; nextPageToken?: string }>('yufeng.console.v1.ConsoleService', 'ListEvents', {
      assetId: filter.assetId,
      releaseId: filter.releaseId,
      verdict: filter.verdict,
      kind: filter.kind,
      since: filter.since,
      until: filter.until,
      query: filter.query,
      pageSize: page.pageSize,
      pageToken: page.pageToken,
    })
    return { items: (res.events ?? []).map(normalizeEvent), nextPageToken: res.nextPageToken ?? '' }
  }

  async getEvent(eventId: string): Promise<Event> {
    const res = await this.call<{ event: Event }>('yufeng.console.v1.ConsoleService', 'GetEvent', { eventId })
    return normalizeEvent(res.event)
  }

  /* ----- GovernService（全部写 RPC 带 Idempotency-Key，docs/api.md §7.1） ----- */

  async gateArtifact(releaseId: string, opts: { corpusRef?: string; budget?: string } = {}): Promise<GateOutcome> {
    return this.call<GateOutcome>(
      'yufeng.govern.v1.GovernService',
      'GateArtifact',
      { releaseId, corpusRef: opts.corpusRef, budget: opts.budget },
      { idempotent: true },
    )
  }

  async startShadow(releaseId: string): Promise<Release> {
    const res = await this.call<{ release: Release }>('yufeng.govern.v1.GovernService', 'StartShadow', { releaseId }, { idempotent: true })
    return normalizeRelease(res.release)
  }

  async promoteCanary(releaseId: string, canaryPercent?: number): Promise<Release> {
    const res = await this.call<{ release: Release }>(
      'yufeng.govern.v1.GovernService',
      'PromoteCanary',
      { releaseId, canaryPercent },
      { idempotent: true },
    )
    return normalizeRelease(res.release)
  }

  async promoteEnforce(releaseId: string): Promise<Release> {
    const res = await this.call<{ release: Release }>('yufeng.govern.v1.GovernService', 'PromoteEnforce', { releaseId }, { idempotent: true })
    return normalizeRelease(res.release)
  }

  async rollbackRelease(releaseId: string, reason: string): Promise<Release> {
    const res = await this.call<{ release: Release }>(
      'yufeng.govern.v1.GovernService',
      'RollbackRelease',
      { releaseId, reason },
      { idempotent: true },
    )
    return normalizeRelease(res.release)
  }

  async retireRelease(releaseId: string, reason: string): Promise<Release> {
    const res = await this.call<{ release: Release }>(
      'yufeng.govern.v1.GovernService',
      'RetireRelease',
      { releaseId, reason },
      { idempotent: true },
    )
    return normalizeRelease(res.release)
  }

  async denyFeedback(releaseId: string, eventId: string, note: string): Promise<Release> {
    const res = await this.call<{ release: Release }>(
      'yufeng.govern.v1.GovernService',
      'DenyFeedback',
      { releaseId, eventId, note },
      { idempotent: true },
    )
    return normalizeRelease(res.release)
  }

  async getRelease(releaseId: string): Promise<Release> {
    const res = await this.call<{ release: Release }>('yufeng.govern.v1.GovernService', 'GetRelease', { releaseId })
    return normalizeRelease(res.release)
  }

  async listReleases(filter: ListReleasesFilter = {}, page: PageQuery = {}): Promise<Page<Release>> {
    const res = await this.call<{ releases?: Release[]; nextPageToken?: string }>('yufeng.govern.v1.GovernService', 'ListReleases', {
      states: filter.states && filter.states.length > 0 ? filter.states : undefined,
      assetId: filter.assetId,
      query: filter.query,
      pageSize: page.pageSize,
      pageToken: page.pageToken,
    })
    return { items: (res.releases ?? []).map(normalizeRelease), nextPageToken: res.nextPageToken ?? '' }
  }

  async getReleaseTimeline(releaseId: string, page: PageQuery = {}): Promise<Page<TimelineEntry>> {
    const res = await this.call<{ entries?: TimelineEntry[]; nextPageToken?: string }>(
      'yufeng.govern.v1.GovernService',
      'GetReleaseTimeline',
      { releaseId, pageSize: page.pageSize, pageToken: page.pageToken },
    )
    return { items: res.entries ?? [], nextPageToken: res.nextPageToken ?? '' }
  }

  async getReleaseStats(releaseId: string): Promise<ReleaseStats> {
    return normalizeReleaseStats(
      await this.call<ReleaseStats>('yufeng.govern.v1.GovernService', 'GetReleaseStats', { releaseId }),
    )
  }

  /* ----- AuditService ----- */

  async listAuditEntries(filter: ListAuditFilter = {}, page: PageQuery = {}): Promise<Page<AuditEntry>> {
    const res = await this.call<{ entries?: AuditEntry[]; nextPageToken?: string }>('yufeng.audit.v1.AuditService', 'ListAuditEntries', {
      objectType: filter.objectType,
      objectId: filter.objectId,
      actorId: filter.actor,
      since: filter.since,
      until: filter.until,
      pageSize: page.pageSize,
      pageToken: page.pageToken,
    })
    return { items: res.entries ?? [], nextPageToken: res.nextPageToken ?? '' }
  }

  async verifyChain(startSequence: string, endSequence: string): Promise<ChainVerification> {
    return this.call<ChainVerification>('yufeng.audit.v1.AuditService', 'VerifyChain', { startSequence, endSequence })
  }

  async listGrants(subjectUserId?: string): Promise<Grant[]> {
    const res = await this.call<{ grants?: Grant[] }>('yufeng.grant.v1.GrantService', 'ListGrants', {
      subjectUserId: subjectUserId ?? '',
    })
    return (res.grants ?? []).map(normalizeGrant)
  }

  async putGrant(req: { subjectUserId: string; tools: string[]; bindings: BindingRef[] }): Promise<Grant> {
    const res = await this.call<{ grant: Grant }>('yufeng.grant.v1.GrantService', 'PutGrant', req, { idempotent: true })
    return normalizeGrant(res.grant)
  }

  async revokeGrant(grantId: string): Promise<void> {
    await this.call('yufeng.grant.v1.GrantService', 'RevokeGrant', { grantId }, { idempotent: true })
  }

  /* ----- OnboardingService ----- */

  async getOnboarding(): Promise<Onboarding> {
    const res = await this.call<Onboarding>('yufeng.onboarding.v1.OnboardingService', 'GetOnboarding', {})
    return {
      state: res.state ?? 'ONBOARDING_STATE_UNSPECIFIED',
      baseUrl: res.baseUrl ?? '',
      model: res.model ?? '',
      hasSecret: res.hasSecret === true,
      secretHint: res.secretHint ?? '',
      jarvisOnline: res.jarvisOnline === true,
      edgeReady: res.edgeReady === true,
      localAssetId: res.localAssetId ?? '',
      localUnitId: res.localUnitId ?? '',
      deploymentSpecDigest: res.deploymentSpecDigest ?? '',
      expectedGenerationId: res.expectedGenerationId ?? '',
      expectedGenerationSeq: int64String(res.expectedGenerationSeq),
      expectedListenPlanVersion: int64String(res.expectedListenPlanVersion),
      lastError: res.lastError ?? '',
      updatedAt: res.updatedAt,
      dialect: res.dialect ?? 'MODEL_DIALECT_OPENAI_CHAT',
    }
  }

  async putModelConfig(req: { baseUrl: string; secret: string; model?: string; dialect?: ModelDialect }): Promise<void> {
    await this.call(
      'yufeng.onboarding.v1.OnboardingService',
      'PutModelConfig',
      { baseUrl: req.baseUrl, secret: req.secret, model: req.model, dialect: req.dialect },
      { idempotent: true },
    )
  }

  async testModelConnectivity(): Promise<void> {
    await this.call('yufeng.onboarding.v1.OnboardingService', 'TestModelConnectivity', {}, { idempotent: true })
  }

  async putDeploymentSpecification(req: EdgeDeploymentSpecification): Promise<EdgeDeploymentCoordinates> {
    const response = await this.call<Partial<EdgeDeploymentCoordinates>>(
      'yufeng.onboarding.v1.OnboardingService',
      'PutDeploymentSpecification',
      req,
      { idempotent: true },
    )
    return {
      unitId: response.unitId ?? '',
      assetId: response.assetId ?? '',
      deploymentSpecDigest: response.deploymentSpecDigest ?? '',
      listenPlanVersion: int64String(response.listenPlanVersion),
      generationId: response.generationId ?? '',
      generationSeq: int64String(response.generationSeq),
    }
  }

  async completeOnboarding(): Promise<void> {
    await this.call('yufeng.onboarding.v1.OnboardingService', 'CompleteOnboarding', {}, { idempotent: true })
  }

  async getModelGateway(): Promise<ModelGateway> {
    const res = await this.call<Partial<ModelGateway>>('yufeng.model.v1.ModelGatewayService', 'GetModelGateway', {})
    return normalizeModelGateway(res)
  }

  async updateModelGateway(req: { baseUrl: string; secret?: string; model?: string; dialect?: ModelDialect }): Promise<ModelGateway> {
    const res = await this.call<Partial<ModelGateway>>(
      'yufeng.model.v1.ModelGatewayService',
      'UpdateModelGateway',
      { baseUrl: req.baseUrl, secret: req.secret ?? '', model: req.model ?? '', dialect: req.dialect },
      { idempotent: true },
    )
    return normalizeModelGateway(res)
  }

  async probeModelGateway(): Promise<{ ok: boolean; lastError: string }> {
    const res = await this.call<{ ok?: boolean; lastError?: string }>(
      'yufeng.model.v1.ModelGatewayService',
      'ProbeModelGateway',
      {},
    )
    return { ok: res.ok === true, lastError: res.lastError ?? '' }
  }

  async proposeArtifact(req: ProposeArtifactRequest): Promise<Release> {
    const res = await this.call<{ releaseId: string; state: Release['state']; artifact?: Release['artifact'] }>(
      'yufeng.govern.v1.GovernService',
      'ProposeArtifact',
      {
        intent: {
          kind: req.intent.kind,
          clusterId: req.intent.clusterId,
          detectionKeys: req.intent.detectionKeys,
          shapeSource: req.intent.shapeSource,
          methods: req.intent.methods,
          routeTemplate: req.intent.routeTemplate,
        },
        scope: { assetIds: req.scope.assetIds, routeSelector: req.scope.routeSelector },
        ttl: req.ttl,
        evidenceRefs: req.evidenceRefs,
      },
      { idempotent: true },
    )
    return normalizeRelease({
      releaseId: res.releaseId,
      state: res.state,
      artifact: res.artifact,
      createdBy: res.artifact?.createdBy ?? '',
      retireReason: 'RETIRE_REASON_UNSPECIFIED',
    })
  }

  async createSession(req: { title?: string } = {}): Promise<{ sessionId: string }> {
    const res = await this.call<{ sessionId: string }>('yufeng.session.v1.SessionService', 'CreateSession', {
      title: req.title ?? '',
    })
    return { sessionId: res.sessionId }
  }

  async sendMessage(req: { sessionId: string; content: string }): Promise<{ messageSequence: string }> {
    const res = await this.call<{ messageSequence: string | number }>('yufeng.session.v1.SessionService', 'SendMessage', {
      sessionId: req.sessionId,
      content: req.content,
    })
    return { messageSequence: String(res.messageSequence) }
  }

  async pollMessages(req: { sessionId: string; cursor?: string; longPollSeconds?: number }): Promise<{
    messages: ChatMessage[]
    nextCursor: string
  }> {
    const longPollSeconds = req.longPollSeconds ?? 0
    const timeoutSeconds = longPollSeconds === 0
      ? SessionLongPollDefault
      : Math.min(SessionLongPollMax, Math.max(0, longPollSeconds))
    const res = await this.call<{ messages?: ChatMessage[]; nextCursor?: string }>(
      'yufeng.session.v1.SessionService',
      'PollMessages',
      {
        sessionId: req.sessionId,
        cursor: req.cursor ?? '',
		longPollSeconds,
      },
		{ timeoutMs: (timeoutSeconds + 5) * 1000 },
    )
    return {
      messages: (res.messages ?? []).map((m) => ({ ...m, sequence: String(m.sequence) })),
      nextCursor: res.nextCursor ?? '',
    }
  }

  async listMessages(req: { sessionId: string }, page: PageQuery = {}): Promise<Page<ChatMessage>> {
    const res = await this.call<{ messages?: ChatMessage[]; nextPageToken?: string }>(
      'yufeng.session.v1.SessionService',
      'ListMessages',
      {
        sessionId: req.sessionId,
        pageSize: page.pageSize,
        pageToken: page.pageToken,
      },
    )
    return {
      items: (res.messages ?? []).map((m) => ({ ...m, sequence: String(m.sequence) })),
      nextPageToken: res.nextPageToken ?? '',
    }
  }

  async listCases(filter: ListCasesFilter = {}, page: PageQuery = {}): Promise<Page<InvestigationCase>> {
    const res = await this.call<{ cases?: InvestigationCase[]; nextPageToken?: string }>('yufeng.case.v1.CaseService', 'ListCases', {
      assetId: filter.assetId,
      moduleId: filter.moduleId,
      state: filter.state,
      pageSize: page.pageSize,
      pageToken: page.pageToken,
    })
    return { items: (res.cases ?? []).map(normalizeInvestigationCase), nextPageToken: res.nextPageToken ?? '' }
  }

  async getCase(caseId: string): Promise<InvestigationCase> {
    const res = await this.call<{ case: InvestigationCase }>('yufeng.case.v1.CaseService', 'GetCase', { caseId })
    return normalizeInvestigationCase(res.case)
  }

  async pollCaseActivities(req: { caseId: string; afterSequence?: string; longPollSeconds?: number }): Promise<{
    activities: CaseActivity[]
    nextAfterSequence: string
  }> {
    const seconds = Math.min(SessionLongPollMax, Math.max(0, req.longPollSeconds ?? SessionLongPollDefault))
    const res = await this.call<{ activities?: CaseActivity[]; nextAfterSequence?: string | number }>(
      'yufeng.case.v1.CaseService',
      'PollCaseActivities',
      { caseId: req.caseId, afterSequence: req.afterSequence ?? '0', longPollSeconds: seconds },
      { timeoutMs: (seconds + 5) * 1000 },
    )
    return {
      activities: (res.activities ?? []).map(normalizeCaseActivity),
      nextAfterSequence: String(res.nextAfterSequence ?? req.afterSequence ?? '0'),
    }
  }

  async resolveCase(req: { caseId: string; resolution: CaseResolution; note?: string }): Promise<InvestigationCase> {
    const res = await this.call<{ case: InvestigationCase }>('yufeng.case.v1.CaseService', 'ResolveCase', req, { idempotent: true })
    return normalizeInvestigationCase(res.case)
  }

  async reopenCase(req: { caseId: string; note?: string }): Promise<InvestigationCase> {
    const res = await this.call<{ case: InvestigationCase }>('yufeng.case.v1.CaseService', 'ReopenCase', req, { idempotent: true })
    return normalizeInvestigationCase(res.case)
  }

  async recordCaseFeedback(req: { caseId: string; resolution: CaseResolution; note?: string }): Promise<InvestigationCase> {
    const res = await this.call<{ case: InvestigationCase }>('yufeng.case.v1.CaseService', 'RecordCaseFeedback', req, { idempotent: true })
    return normalizeInvestigationCase(res.case)
  }

  async getApproval(approvalId: string): Promise<ApprovalView> {
    const res = await this.call<{ approval: ApprovalView }>('yufeng.agent.v1.AgentInteractionService', 'GetApproval', { approvalId })
    return normalizeApprovalView(res.approval)
  }

  async decideApproval(req: { approvalId: string; approved: boolean; reason?: string }): Promise<{ approvalId: string; state: string }> {
    return this.call('yufeng.agent.v1.AgentInteractionService', 'DecideApproval', req, { idempotent: true })
  }

  async listModules(): Promise<DefenseModule[]> {
    const res = await this.call<{ modules?: DefenseModule[] }>('yufeng.module.v1.ModuleCatalogService', 'ListModules', {})
    return (res.modules ?? []).map(normalizeDefenseModule)
  }

  async listAgentProfiles(page: PageQuery = {}): Promise<Page<ManagedAgentProfile>> {
    const res = await this.call<{ profiles?: ManagedAgentProfile[]; nextPageToken?: string }>('yufeng.agent.v1.AgentProfileService', 'ListAgentProfiles', {
      pageSize: page.pageSize,
      pageToken: page.pageToken,
    })
    return { items: (res.profiles ?? []).map(normalizeManagedAgentProfile), nextPageToken: res.nextPageToken ?? '' }
  }

  async createAgentProfile(req: AgentProfileInput): Promise<ManagedAgentProfile> {
    const res = await this.call<{ profile: ManagedAgentProfile }>('yufeng.agent.v1.AgentProfileService', 'CreateAgentProfile', {
      displayName: req.displayName,
      tools: req.tools,
      bindings: req.bindings,
    }, { idempotent: true })
    return normalizeManagedAgentProfile(res.profile)
  }

  async updateAgentProfile(req: AgentProfileInput & { agentId: string; state: AgentProfileState }): Promise<ManagedAgentProfile> {
    const res = await this.call<{ profile: ManagedAgentProfile }>('yufeng.agent.v1.AgentProfileService', 'UpdateAgentProfile', {
      agentId: req.agentId,
      displayName: req.displayName,
      state: req.state,
      tools: req.tools,
      bindings: req.bindings,
    }, { idempotent: true })
    return normalizeManagedAgentProfile(res.profile)
  }

  async deleteAgentProfile(agentId: string): Promise<void> {
    await this.call('yufeng.agent.v1.AgentProfileService', 'DeleteAgentProfile', { agentId }, { idempotent: true })
  }

  async batchUpdateAgentProfiles(req: { agentIds: string[]; tools: string[]; bindings: BindingRef[] }): Promise<ManagedAgentProfile[]> {
    const res = await this.call<{ profiles?: ManagedAgentProfile[] }>('yufeng.agent.v1.AgentProfileService', 'BatchUpdateAgentProfiles', req, { idempotent: true })
    return (res.profiles ?? []).map(normalizeManagedAgentProfile)
  }

  async listWorkers(page: PageQuery = {}): Promise<Page<WorkerRecord>> {
    const res = await this.call<{ workers?: WorkerRecord[]; nextPageToken?: string }>('yufeng.worker.v1.WorkerService', 'ListWorkers', {
      pageSize: page.pageSize,
      pageToken: page.pageToken,
    })
    return { items: (res.workers ?? []).map(normalizeWorkerRecord), nextPageToken: res.nextPageToken ?? '' }
  }

  async listWorkerEnrollments(state = '', page: PageQuery = {}): Promise<Page<WorkerEnrollmentRecord>> {
    const res = await this.call<{ enrollments?: WorkerEnrollmentRecord[]; nextPageToken?: string }>('yufeng.worker.v1.WorkerService', 'ListWorkerEnrollments', {
      state,
      pageSize: page.pageSize,
      pageToken: page.pageToken,
    })
    return { items: (res.enrollments ?? []).map(normalizeWorkerEnrollmentRecord), nextPageToken: res.nextPageToken ?? '' }
  }

  async decideWorkerEnrollment(req: { enrollmentId: string; approved: boolean; bindings: string[]; maxConcurrency?: number }): Promise<WorkerEnrollmentDecision> {
    return normalizeWorkerEnrollmentDecision(
      await this.call('yufeng.worker.v1.WorkerService', 'DecideWorkerEnrollment', req, { idempotent: true }),
    )
  }
}
