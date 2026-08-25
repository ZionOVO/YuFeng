// ConsoleClientFixture 为页面组件提供确定性的接口夹具。
// 它只在测试目录使用；真实服务语义由 Brain 的 PostgreSQL 集成测试负责。

import { ApiError } from '../../api/errors'
import type { ConnectErrorCode } from '../../api/errors'
import type { GateCheck } from '../../api/types'
import type {
  ListAssetsFilter,
  ListAuditFilter,
  ListEventsFilter,
  ListReleasesFilter,
  ListUsersFilter,
  ListCasesFilter,
  Page,
  PageQuery,
} from '../../api/client'
import type { ConsoleClient, EdgeEnrollmentInput } from '../../api/client'
import type {
  AssetDetail,
  AssetPatch,
  AuditEntry,
  ChainVerification,
  ChatMessage,
  DashboardSummary,
  EdgeEnrollment,
  Event,
  EventDetail,
  GateOutcome,
  BindingRef,
  EffectiveAccess,
  Grant,
  LoginConfig,
  ModelDialect,
  ModelGateway,
  ModelGatewayStatus,
  ModelProviderStat,
  Onboarding,
  OnboardingState,
  ProposeArtifactRequest,
  Release,
  ReleaseState,
  ReleaseStats,
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
  ModelIngressWindow,
  ModelIngressWindowStatus,
} from '../../api/types'
import { emptyAccess, hasTool } from '../../api/access'
import {
  createAssets,
  createAuditEntries,
  createDashboard,
  createEvents,
  createManagedAgentProfiles,
  createReleases,
  createStats,
  createTimelines,
  fakeHash,
  FIXTURE_ACCOUNTS,
  FIXTURE_GRANTS,
  FIXTURE_SESSION_EXPIRES_AT,
  fixtureTokenFor,
} from './data'

interface FixtureChatSession {
  sessionId: string
  owner: string
  title: string
  messages: ChatMessage[]
}

interface FixtureOnboarding extends Onboarding {
  secret: string
  modelLiveOk: boolean
}

interface FixtureGatewayCall {
  at: string
  kind: 'complete' | 'probe'
  ok: boolean
  host: string
  model: string
  error: string
}

interface FixtureState {
  accounts: { user: User; password: string }[]
  assets: AssetDetail[]
  events: Event[]
  releases: Release[]
  stats: Record<string, ReleaseStats>
  timelines: Record<string, TimelineEntry[]>
  audit: AuditEntry[]
  grants: Grant[]
  onboarding: FixtureOnboarding
  chats: FixtureChatSession[]
  gatewayCalls: FixtureGatewayCall[]
  cases: InvestigationCase[]
  caseActivities: Record<string, CaseActivity[]>
  approvals: ApprovalView[]
  modules: DefenseModule[]
  workers: WorkerRecord[]
  workerEnrollments: WorkerEnrollmentRecord[]
  agentProfiles: ManagedAgentProfile[]
}

const MODEL_GATEWAY_WINDOW_MS = 24 * 60 * 60 * 1000
const MODEL_GATEWAY_WINDOW_SECONDS = '86400'
const AGENT_PROFILE_TOOLS = new Set(['case.get', 'case.request_evidence', 'run.create', 'case.complete'])

function normalizedProfileInput(req: AgentProfileInput): AgentProfileInput {
  const displayName = req.displayName.trim()
  const tools = [...new Set(req.tools.map((tool) => tool.trim()))].sort()
  const assetIds = [...new Set(req.bindings.filter((binding) => binding.kind === 'asset').map((binding) => binding.id.trim()))].sort()
  if (displayName === '' || [...displayName].length > 80) throw err('invalid_argument', 'display name must be between 1 and 80 characters')
  if (tools.length === 0 || tools.some((tool) => !AGENT_PROFILE_TOOLS.has(tool))) throw err('invalid_argument', 'unsupported traffic review agent tool')
  if (assetIds.length === 0 || assetIds.some((id) => id === '' || id === '*') || assetIds.length !== req.bindings.length) {
    throw err('invalid_argument', 'managed agent bindings must be concrete assets')
  }
  return { displayName, tools, bindings: assetIds.map((id) => ({ kind: 'asset', id })) }
}

function modelHostOf(url: string): string {
  try {
    return new URL(url).host.toLowerCase()
  } catch {
    return ''
  }
}

function validProxyCIDR(value: string): boolean {
  const slash = value.lastIndexOf('/')
  if (slash <= 0 || slash === value.length - 1) return false
  const address = value.slice(0, slash)
  const prefix = Number(value.slice(slash + 1))
  if (!Number.isInteger(prefix)) return false
  const octets = address.split('.')
  if (octets.length === 4) {
    return prefix >= 0 && prefix <= 32 && octets.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255)
  }
  if (!address.includes(':') || prefix < 0 || prefix > 128 || address.includes('%')) return false
  try {
    return new URL(`http://[${address}]/`).hostname !== ''
  } catch {
    return false
  }
}

function seedGatewayCalls(onboarding: FixtureOnboarding): FixtureGatewayCall[] {
  if (!onboarding.hasSecret || onboarding.baseUrl === '') return []
  const host = modelHostOf(onboarding.baseUrl)
  const at = new Date().toISOString()
  return Array.from({ length: 24 }, () => ({
    at,
    kind: 'complete' as const,
    ok: true,
    host,
    model: onboarding.model,
    error: '',
  }))
}

function projectModelGateway(o: FixtureOnboarding, calls: FixtureGatewayCall[]): ModelGateway {
  const since = Date.now() - MODEL_GATEWAY_WINDOW_MS
  const windowed = calls.filter((c) => Date.parse(c.at) >= since)
  const byHost = new Map<string, ModelProviderStat>()
  for (const c of windowed) {
    const cur = byHost.get(c.host) ?? { host: c.host, callsTotal: '0', callsOk: '0' }
    const lastAt = cur.lastAt === undefined || Date.parse(c.at) >= Date.parse(cur.lastAt) ? c.at : cur.lastAt
    byHost.set(c.host, {
      host: c.host,
      callsTotal: String(Number(cur.callsTotal) + 1),
      callsOk: String(Number(cur.callsOk) + (c.ok ? 1 : 0)),
      lastAt,
    })
  }
  const current = modelHostOf(o.baseUrl)
  if (current !== '' && !byHost.has(current)) {
    byHost.set(current, { host: current, callsTotal: '0', callsOk: '0' })
  }
  const providers = [...byHost.values()].sort((a, b) => Number(b.callsTotal) - Number(a.callsTotal) || a.host.localeCompare(b.host))
  const total = windowed.length
  const ok = windowed.filter((c) => c.ok).length
  const last = [...windowed].sort((a, b) => Date.parse(b.at) - Date.parse(a.at))[0]
  const configured = o.hasSecret && o.baseUrl !== ''
  let status: ModelGatewayStatus = 'MODEL_GATEWAY_STATUS_UNCONFIGURED'
  if (configured) {
    if (total === 0) status = 'MODEL_GATEWAY_STATUS_READY'
    else if (ok === total) status = 'MODEL_GATEWAY_STATUS_LIVE'
    else if (ok === 0) status = 'MODEL_GATEWAY_STATUS_DOWN'
    else status = 'MODEL_GATEWAY_STATUS_DEGRADED'
  }
  return {
    baseUrl: o.baseUrl,
    model: o.model,
    dialect: o.dialect ?? 'MODEL_DIALECT_OPENAI_CHAT',
    hasSecret: o.hasSecret,
    secretHint: o.secretHint,
    status,
    providerCount: providers.filter((p) => p.host !== '').length,
    windowSeconds: MODEL_GATEWAY_WINDOW_SECONDS,
    callsTotal: String(total),
    callsOk: String(ok),
    lastCallAt: last?.at,
    lastError: last !== undefined && !last.ok ? last.error : '',
    providers,
  }
}

function defaultOnboarding(state: OnboardingState): FixtureOnboarding {
  const completed = state === 'ONBOARDING_STATE_COMPLETED'
  const modelLive = completed || state === 'ONBOARDING_STATE_EDGE_LIVE' || state === 'ONBOARDING_STATE_MODEL_LIVE'
  return {
    state: state === 'ONBOARDING_STATE_EDGE_LIVE' ? 'ONBOARDING_STATE_MODEL_LIVE' : state,
    baseUrl: modelLive ? 'https://api.x.ai/v1' : '',
    model: modelLive ? 'grok-4-1-fast-non-reasoning' : '',
    dialect: 'MODEL_DIALECT_OPENAI_CHAT',
    hasSecret: modelLive,
    secretHint: modelLive ? '****test' : '',
    jarvisOnline: completed,
    lastError: '',
    updatedAt: '2026-08-16T00:00:00.000Z',
    secret: modelLive ? 'sk-fixture-test' : '',
    modelLiveOk: modelLive,
  }
}

function freshState(onboardingState: OnboardingState = 'ONBOARDING_STATE_COMPLETED'): FixtureState {
  const completed = onboardingState === 'ONBOARDING_STATE_COMPLETED'
  return structuredClone({
    accounts: FIXTURE_ACCOUNTS,
    assets: completed ? createAssets() : [],
    events: createEvents(),
    releases: createReleases(),
    stats: createStats(),
    timelines: createTimelines(),
    audit: createAuditEntries(),
    grants: completed ? FIXTURE_GRANTS : [],
    onboarding: defaultOnboarding(onboardingState),
    chats: [],
    gatewayCalls: seedGatewayCalls(defaultOnboarding(onboardingState)),
    cases: createFixtureCases(),
    caseActivities: createFixtureCaseActivities(),
    approvals: createFixtureApprovals(),
    modules: [{
      moduleId: 'traffic-interception',
      displayName: '流量拦截',
      version: '1',
      requiredProducerCapabilities: ['traffic-window/v1', 'traffic-review-candidate/v1'],
      caseActivitySchemas: ['traffic-review/v1'],
      surfaces: ['MODULE_SURFACE_ASSET_BADGE', 'MODULE_SURFACE_CASE_CARD', 'MODULE_SURFACE_CASE_WORKSPACE', 'MODULE_SURFACE_STATISTICS'],
      active: true,
    }],
    workers: [{
      workerId: 'agentd-central',
      workerKind: 'WORKER_KIND_RUN_SUPERVISOR',
      version: 'dev',
      operatingSystem: 'linux',
      architecture: 'amd64',
      sandboxCapabilities: ['landlock', 'seccomp', 'resource_limits'],
      investigationEligible: true,
      missingSandboxCapabilities: [],
      maxConcurrency: 1,
      lastSeenAt: '2026-08-16T00:00:00.000Z',
    }],
    workerEnrollments: [{
      enrollmentId: 'enroll-external-01', workerId: 'agentd-branch-office', workerKind: 'WORKER_KIND_RUN_SUPERVISOR',
      publicKeyFingerprint: `sha256:${fakeHash('agentd-branch-office').repeat(8)}`, hostname: 'branch-office-worker',
      operatingSystem: 'darwin', architecture: 'arm64', sandboxCapabilities: ['sandbox_profile', 'resource_limits'],
      state: 'pending', bindings: [], maxConcurrency: 1, requestedAt: '2026-08-16T00:00:00.000Z',
    }],
    agentProfiles: createManagedAgentProfiles(),
  })
}

function createFixtureCases(): InvestigationCase[] {
  return [{
    caseId: 'case_traffic_01',
    moduleId: 'traffic-interception',
    assetId: 'asset-01',
    clusterId: 'cluster_checkout_shape',
    state: 'INVESTIGATION_CASE_STATE_WAITING_EVIDENCE_APPROVAL',
    priority: 92,
    title: '结算入口出现未映射请求形状',
    summary: '边缘统计发现高风险未拦截样本；Jarvis 已请求一次性证据访问。',
    representatives: [{
      candidateId: 'candidate_01', windowId: 'window_01', unitId: 'unit-edge-01', assetId: 'asset-01',
      occurredAt: '2026-08-16T00:00:00.000Z', method: 'POST', routeTemplate: '/checkout/:id', riskScore: 0.94,
      riskReasons: ['detected_unmapped', 'route_novelty'], evidenceHandle: 'evidence:opaque', evidenceDigest: `sha256:${fakeHash('evidence')}`,
      evidenceExpiresAt: '2026-08-17T00:00:00.000Z', baseline: false,
    }],
    shadowReleaseId: '',
    assignedAgentId: 'profile_traffic_review_primary',
    assignedAgentDisplayName: '边缘流量审查组',
    resolution: 'CASE_RESOLUTION_UNSPECIFIED',
    createdAt: '2026-08-16T00:00:00.000Z',
    updatedAt: '2026-08-16T00:03:00.000Z',
  }, {
    caseId: 'case_traffic_02', moduleId: 'traffic-interception', assetId: 'asset-02', clusterId: 'cluster_search_false_positive',
    state: 'INVESTIGATION_CASE_STATE_FINDING_READY', priority: 67, title: '搜索接口疑似误报',
    summary: '模型结论已通过结构校验；只追加反馈，不自动修改策略。', representatives: [], shadowReleaseId: '',
    assignedAgentId: 'profile_traffic_review_primary', assignedAgentDisplayName: '边缘流量审查组',
    resolution: 'CASE_RESOLUTION_UNSPECIFIED',
    finding: { disposition: 'TRAFFIC_FINDING_DISPOSITION_SUSPECTED_FALSE_POSITIVE', confidence: 0.86, evidenceRefs: ['candidate_02'],
      attackClass: '', routeTemplate: '/search', selectors: ['query.q'], rationale: '请求形状与近期良性基线一致。' },
    createdAt: '2026-08-15T22:00:00.000Z', updatedAt: '2026-08-16T00:05:00.000Z',
  }]
}

function createFixtureCaseActivities(): Record<string, CaseActivity[]> {
  return {
    case_traffic_01: [
      { sequence: '1', caseId: 'case_traffic_01', kind: 'CASE_ACTIVITY_KIND_CREATED', refId: '', summary: '案件已建立', occurredAt: '2026-08-16T00:00:00.000Z' },
      { sequence: '2', caseId: 'case_traffic_01', kind: 'CASE_ACTIVITY_KIND_EVIDENCE_REQUESTED', refId: 'approval_evidence_01', summary: 'Jarvis 请求一次性案件证据审批', occurredAt: '2026-08-16T00:03:00.000Z' },
    ],
    case_traffic_02: [{ sequence: '1', caseId: 'case_traffic_02', kind: 'CASE_ACTIVITY_KIND_FINDING', refId: 'finding_02', summary: '疑似误报，只追加反馈', occurredAt: '2026-08-16T00:05:00.000Z' }],
  }
}

function createFixtureApprovals(): ApprovalView[] {
  return [{
    approvalId: 'approval_evidence_01', kind: 'APPROVAL_KIND_EVIDENCE', state: 'pending', caseId: 'case_traffic_01', assetId: 'asset-01', workerId: '',
    modelHost: 'api.x.ai', modelName: 'grok-4-1-fast-non-reasoning', modelConfigDigest: `sha256:${fakeHash('model-config')}`,
    allowedFields: ['method', 'path', 'query', 'body'], maxBytes: '40960', previousCapacity: 0, requestedCapacity: 0,
    expiresAt: '2099-08-16T00:18:00.000Z', createdAt: '2026-08-16T00:03:00.000Z',
  }]
}

function err(code: ConnectErrorCode, message: string, extra: { reasonKey?: string; gateChecks?: GateCheck[] } = {}): ApiError {
  return new ApiError({ code, message, httpStatus: 0, ...extra })
}

/** ConsoleClientFixture 每次构造得到独立数据副本，测试之间互不影响。
 *  传入 token 时按演示令牌恢复会话（页面刷新后 GetMe 仍能恢复登录态）。 */
export class ConsoleClientFixture implements ConsoleClient {
  private state: FixtureState
  private session: Session | null = null
  private modelTestFailsLeft = 0
  private trafficReviewPolicies = new Map<string, TrafficReviewPolicyStatus>()
  private modelIngressWindows = new Map<string, ModelIngressWindowStatus>()

  constructor(opts: { token?: string | null; onboardingState?: OnboardingState } = {}) {
    this.state = freshState(opts.onboardingState ?? 'ONBOARDING_STATE_COMPLETED')
    if (typeof opts.token === 'string' && opts.token !== '') {
      const account = this.state.accounts.find((a) => fixtureTokenFor(a.user.username) === opts.token && a.user.state === 'USER_STATE_ACTIVE')
      if (account !== undefined) {
        this.session = {
          token: opts.token,
          expiresAt: FIXTURE_SESSION_EXPIRES_AT,
          user: structuredClone(account.user),
          access: this.expandAccess(account.user.userId),
        }
      }
    }
  }

  /** 随后若干次 TestModelConnectivity 写成 FAILED（供引导页重试用例）。 */
  failNextModelTests(n: number): void {
    this.modelTestFailsLeft = n
  }

  /** 单测用：模拟贾维斯已在线（与探测成败无关）。 */
  setJarvisOnline(on: boolean): void {
    this.state.onboarding.jarvisOnline = on
  }

  /* ----- 守卫 ----- */

  private me(): User {
    if (this.session === null) throw err('unauthenticated', 'missing or invalid session token')
    const account = this.state.accounts.find((a) => a.user.userId === this.session?.user.userId)
    if (account === undefined || account.user.state !== 'USER_STATE_ACTIVE') {
      throw err('unauthenticated', 'session user is no longer active')
    }
    return account.user
  }

  private onboardingCompleted(): boolean {
    return this.state.onboarding.state === 'ONBOARDING_STATE_COMPLETED'
  }

  private requireOnboardingComplete(): void {
    if (!this.onboardingCompleted()) {
      throw err('failed_precondition', 'onboarding is not complete', { reasonKey: 'onboarding_incomplete' })
    }
  }

  private expandAccess(userId: string): EffectiveAccess {
    const account = this.state.accounts.find((a) => a.user.userId === userId)
    if (!this.onboardingCompleted() && account?.user.role === 'USER_ROLE_ADMIN') {
      return {
        tools: [
          'user.admin',
          'grant.write',
          'console.read',
          'asset.create',
          'asset.update',
          'asset.delete',
          'asset.attach',
          'asset.detach',
        ],
        bindings: [],
      }
    }
    const access = emptyAccess()
    for (const g of this.state.grants) {
      if (g.subjectUserId !== userId) continue
      if (g.expiresAt !== undefined && g.expiresAt !== '' && g.expiresAt < '2026-08-16T00:00:00.000Z') continue
      for (const t of g.tools) {
        if (!access.tools.includes(t)) access.tools.push(t)
      }
      for (const b of g.bindings) {
        if (!access.bindings.some((x) => x.kind === b.kind && x.id === b.id)) access.bindings.push(b)
      }
    }
    return access
  }

  private myAccess(): EffectiveAccess {
    return this.expandAccess(this.me().userId)
  }

  private requireTool(tool: string): User {
    const user = this.me()
    if (!hasTool(this.expandAccess(user.userId), tool)) {
      throw err('permission_denied', `missing tool ${tool}`)
    }
    return user
  }

  private visibleAssetIds(): Set<string> {
    return new Set(this.myAccess().bindings.filter((b) => b.kind === 'asset').map((b) => b.id))
  }

  private requireAsset(tool: string, assetId: string): User {
    const user = this.requireTool(tool)
    if (!this.visibleAssetIds().has(assetId)) throw err('permission_denied', 'object out of bindings')
    return user
  }

  private requireAdmin(): User {
    return this.requireTool('user.admin')
  }

  private requireAdminRole(): User {
    const user = this.me()
    if (user.role !== 'USER_ROLE_ADMIN') throw err('permission_denied', 'admin role required')
    return user
  }

  private requireReleaseTool(tool: string, release: Release): User {
    const user = this.requireTool(tool)
    const ids = release.artifact?.scope?.assetIds ?? []
    const visible = this.visibleAssetIds()
    if (ids.length === 0 || !ids.every((id) => visible.has(id))) {
      throw err('permission_denied', 'object out of bindings')
    }
    return user
  }

  private requirePromote(tool: string, release: Release): User {
    const user = this.requireReleaseTool(tool, release)
    if (release.createdBy === user.username) {
      throw err('permission_denied', 'proposer cannot promote own release')
    }
    return user
  }

  private findRelease(releaseId: string): Release {
    const r = this.state.releases.find((x) => x.releaseId === releaseId)
    if (r === undefined) throw err('not_found', `release ${releaseId} not found`)
    return r
  }

  private findAsset(assetId: string): AssetDetail {
    const a = this.state.assets.find((x) => x.asset.id === assetId)
    if (a === undefined) throw err('not_found', `asset ${assetId} not found`)
    return a
  }

  private appendBinding(userId: string, assetId: string): void {
    for (const g of this.state.grants) {
      if (g.subjectUserId !== userId) continue
      if (!g.bindings.some((b) => b.kind === 'asset' && b.id === assetId)) {
        g.bindings.push({ kind: 'asset', id: assetId })
      }
    }
  }

  private pruneBinding(assetId: string): void {
    for (const g of this.state.grants) {
      g.bindings = g.bindings.filter((b) => !(b.kind === 'asset' && b.id === assetId))
    }
  }

  /** 追加审计条目（维持哈希链形状）。 */
  private audit(action: string, objectType: string, objectId: string, actor: string, note = 'fixture mutation'): void {
    const prev = this.state.audit[this.state.audit.length - 1]
    const sequence = String(this.state.audit.length + 1)
    const previousHash = prev?.entryHash ?? fakeHash('genesis')
    this.state.audit.push({
      sequence,
      occurredAt: '2026-08-16T00:00:00.000Z',
      actorType: 'user',
      actorId: actor,
      action,
      objectType,
      objectId,
      details: JSON.stringify({ note }),
      previousHash,
      entryHash: fakeHash(`${previousHash}:${sequence}:${action}:${objectId}`),
    })
  }

  /* ----- 分页（pageSize 默认 50 上限 200；pageToken 为 base64 偏移，不透明但可校验） ----- */

  private paginate<T>(items: T[], page: PageQuery): Page<T> {
    const pageSize = page.pageSize ?? 50
    if (!Number.isInteger(pageSize) || pageSize < 1 || pageSize > 200) {
      throw err('invalid_argument', 'page_size must be between 1 and 200')
    }
    let offset = 0
    if (page.pageToken !== undefined && page.pageToken !== '') {
      // 浏览器无 Buffer；token 只含 ASCII，用 atob/btoa
      const raw = atob(page.pageToken)
      const match = /^o:(\d+)$/.exec(raw)
      if (match === null) throw err('invalid_argument', 'invalid page_token')
      offset = Number(match[1])
    }
    const slice = items.slice(offset, offset + pageSize)
    const next = offset + pageSize
    return { items: slice, nextPageToken: next < items.length ? btoa(`o:${next}`) : '' }
  }

  /* ----- AuthService ----- */

  async login(req: { username: string; password: string }): Promise<Session> {
    const account = this.state.accounts.find((a) => a.user.username === req.username)
    if (account === undefined || account.password !== req.password) {
      throw err('unauthenticated', 'invalid username or password')
    }
    if (account.user.state !== 'USER_STATE_ACTIVE') {
      throw err('failed_precondition', 'user is disabled', { reasonKey: 'user_disabled' })
    }
    const session: Session = {
      token: fixtureTokenFor(req.username),
      expiresAt: FIXTURE_SESSION_EXPIRES_AT,
      user: structuredClone(account.user),
      access: this.expandAccess(account.user.userId),
    }
    this.session = session
    return structuredClone(session)
  }

  async logout(): Promise<void> {
    this.session = null
  }

  async getMe(): Promise<User> {
    return structuredClone(this.me())
  }

  async getMyAccess(): Promise<EffectiveAccess> {
    return structuredClone(this.myAccess())
  }

  async changePassword(req: { oldPassword: string; newPassword: string }): Promise<void> {
    const user = this.me()
    const account = this.state.accounts.find((a) => a.user.userId === user.userId)
    if (account === undefined || account.password !== req.oldPassword) {
      throw err('invalid_argument', 'old password does not match')
    }
    account.password = req.newPassword
  }

  async getLoginConfig(): Promise<LoginConfig> {
    return { allowSelfRegistration: false, passwordMinLength: 8, sessionTtl: '43200s' }
  }

  /* ----- UserService（仅 ADMIN） ----- */

  async createUser(req: { username: string; password: string; displayName: string; role: UserRole }): Promise<User> {
    const actor = this.requireAdmin()
    if (this.state.accounts.some((a) => a.user.username === req.username && a.user.state !== 'USER_STATE_DELETED')) {
      throw err('already_exists', `username ${req.username} already exists`)
    }
    const user: User = {
      userId: `usr_${fakeHash(req.username).slice(0, 6)}`,
      username: req.username,
      displayName: req.displayName,
      role: req.role,
      state: 'USER_STATE_ACTIVE',
      createdAt: '2026-08-16T00:00:00.000Z',
    }
    this.state.accounts.push({ user, password: req.password })
    this.audit('user.create', 'user', user.userId, actor.username)
    return structuredClone(user)
  }

  async listUsers(filter: ListUsersFilter = {}, page: PageQuery = {}): Promise<Page<User>> {
    this.requireAdmin()
    let users = this.state.accounts.map((a) => a.user)
    if (filter.query !== undefined && filter.query !== '') {
      users = users.filter((u) => u.username.includes(filter.query ?? '') || u.displayName.includes(filter.query ?? ''))
    }
    if (filter.role !== undefined) users = users.filter((u) => u.role === filter.role)
    if (filter.state !== undefined) users = users.filter((u) => u.state === filter.state)
    return this.paginate(structuredClone(users), page)
  }

  async getUser(userId: string): Promise<User> {
    this.requireAdmin()
    const account = this.state.accounts.find((a) => a.user.userId === userId)
    if (account === undefined) throw err('not_found', `user ${userId} not found`)
    return structuredClone(account.user)
  }

  async updateUser(userId: string, patch: UserPatch): Promise<User> {
    const actor = this.requireAdmin()
    const account = this.state.accounts.find((a) => a.user.userId === userId)
    if (account === undefined) throw err('not_found', `user ${userId} not found`)
    if (patch.displayName !== undefined) account.user.displayName = patch.displayName
    if (patch.role !== undefined) account.user.role = patch.role
    if (patch.state !== undefined) account.user.state = patch.state
    this.audit('user.update', 'user', userId, actor.username)
    return structuredClone(account.user)
  }

  async deleteUser(userId: string): Promise<User> {
    const actor = this.requireAdmin()
    const account = this.state.accounts.find((a) => a.user.userId === userId)
    if (account === undefined) throw err('not_found', `user ${userId} not found`)
    // 不可删除最后一个 ACTIVE ADMIN（docs/api.md §6）
    const activeAdmins = this.state.accounts.filter((a) => a.user.role === 'USER_ROLE_ADMIN' && a.user.state === 'USER_STATE_ACTIVE')
    if (account.user.role === 'USER_ROLE_ADMIN' && account.user.state === 'USER_STATE_ACTIVE' && activeAdmins.length <= 1) {
      throw err('failed_precondition', 'cannot delete the last active admin')
    }
    account.user.state = 'USER_STATE_DELETED'
    this.audit('user.delete', 'user', userId, actor.username)
    return structuredClone(account.user)
  }

  async adminResetPassword(userId: string, newPassword: string): Promise<User> {
    const actor = this.requireAdmin()
    const account = this.state.accounts.find((a) => a.user.userId === userId)
    if (account === undefined) throw err('not_found', `user ${userId} not found`)
    account.password = newPassword
    this.audit('user.reset_password', 'user', userId, actor.username)
    return structuredClone(account.user)
  }

  /* ----- AssetService ----- */

  async listAssets(filter: ListAssetsFilter = {}, page: PageQuery = {}): Promise<Page<AssetDetail>> {
    this.me()
    if (!this.onboardingCompleted()) {
      this.requireAdminRole()
      let assets: AssetDetail[] = []
      if (filter.query !== undefined && filter.query !== '') {
        assets = assets.filter((a) => a.asset.id.includes(filter.query ?? '') || a.asset.displayName.includes(filter.query ?? ''))
      }
      if (filter.criticality !== undefined) assets = assets.filter((a) => a.asset.criticality === filter.criticality)
      return this.paginate(structuredClone(assets), page)
    }
    this.requireTool('console.read')
    const visible = this.visibleAssetIds()
    let assets = this.state.assets.filter((a) => visible.has(a.asset.id))
    if (filter.query !== undefined && filter.query !== '') {
      assets = assets.filter((a) => a.asset.id.includes(filter.query ?? '') || a.asset.displayName.includes(filter.query ?? ''))
    }
    if (filter.criticality !== undefined) assets = assets.filter((a) => a.asset.criticality === filter.criticality)
    return this.paginate(structuredClone(assets), page)
  }

  async getAsset(assetId: string): Promise<AssetDetail> {
    if (!this.onboardingCompleted()) {
      this.requireAdminRole()
      return structuredClone(this.findAsset(assetId))
    }
    this.requireAsset('console.read', assetId)
    return structuredClone(this.findAsset(assetId))
  }

  async createAsset(input: {
    displayName: string
    accessMode?: AssetDetail['asset']['accessMode']
    criticality?: AssetDetail['asset']['criticality']
    maxAutoTier?: AssetDetail['asset']['maxAutoTier']
  }): Promise<AssetDetail> {
    const actor = this.requireAdminRole()
    if (this.onboardingCompleted()) this.requireTool('asset.create')
    const name = input.displayName.trim()
    if (name === '') throw err('invalid_argument', 'display_name is required')
    const id = `asset_${fakeHash(name + String(this.state.assets.length))}`
    const detail: AssetDetail = {
      asset: {
        id,
        displayName: name,
        accessMode: input.accessMode ?? 'ACCESS_MODE_NETWORK',
        transports: [],
        criticality: input.criticality ?? 'CRITICALITY_P2',
        maxAutoTier: input.maxAutoTier ?? 'TIER_L0_REPORT',
        labels: {},
      },
      unitIds: [],
      units: [],
      edgeEnrollments: [],
      health: 'UNIT_HEALTH_UNSPECIFIED',
      activeReleaseCount: 0,
    }
    this.state.assets.unshift(detail)
    this.appendBinding(actor.userId, id)
    this.audit('asset.create', 'asset', id, actor.username)
    return structuredClone(detail)
  }

  async updateAsset(assetId: string, patch: AssetPatch, expectedUpdatedAt?: string): Promise<AssetDetail> {
    const actor = this.requireAdminRole()
    if (this.onboardingCompleted()) this.requireAsset('asset.update', assetId)
    const detail = this.findAsset(assetId)
    if (expectedUpdatedAt !== undefined && expectedUpdatedAt !== '' && detail.asset.updatedAt !== undefined && detail.asset.updatedAt !== expectedUpdatedAt) {
      throw err('failed_precondition', 'version_mismatch')
    }
    if (patch.displayName !== undefined) detail.asset.displayName = patch.displayName
    if (patch.labels !== undefined) detail.asset.labels = patch.labels
    if (patch.criticality !== undefined) detail.asset.criticality = patch.criticality
    if (patch.maxAutoTier !== undefined) detail.asset.maxAutoTier = patch.maxAutoTier
    if (patch.accessMode !== undefined) detail.asset.accessMode = patch.accessMode
    detail.asset.updatedAt = new Date().toISOString()
    this.audit('asset.update', 'asset', assetId, actor.username)
    return structuredClone(detail)
  }

  async deleteAsset(assetId: string): Promise<void> {
    const actor = this.requireAdminRole()
    if (this.onboardingCompleted()) this.requireAsset('asset.delete', assetId)
    const idx = this.state.assets.findIndex((a) => a.asset.id === assetId)
    if (idx < 0) throw err('permission_denied', 'asset not found')
    if (this.state.assets[idx].edgeEnrollments.length > 0) {
      throw err('failed_precondition', 'asset has edge enrollments')
    }
    this.state.assets.splice(idx, 1)
    this.pruneBinding(assetId)
    this.audit('asset.delete', 'asset', assetId, actor.username)
  }

  async attachUnit(assetId: string, unitId: string): Promise<AssetDetail> {
    const actor = this.requireAdminRole()
    if (this.onboardingCompleted()) this.requireAsset('asset.attach', assetId)
    const detail = this.findAsset(assetId)
    if (!detail.unitIds.includes(unitId)) detail.unitIds.push(unitId)
    if (detail.health === 'UNIT_HEALTH_UNSPECIFIED') detail.health = 'UNIT_HEALTH_HEALTHY'
    this.audit('unit.attach', 'asset', assetId, actor.username)
    return structuredClone(detail)
  }

  async detachUnit(assetId: string, unitId: string): Promise<AssetDetail> {
    const actor = this.requireAdminRole()
    if (this.onboardingCompleted()) this.requireAsset('asset.detach', assetId)
    const detail = this.findAsset(assetId)
    if (!detail.unitIds.includes(unitId)) throw err('not_found', `unit ${unitId} is not attached to ${assetId}`)
    detail.unitIds = detail.unitIds.filter((u) => u !== unitId)
    this.audit('unit.detach', 'asset', assetId, actor.username)
    return structuredClone(detail)
  }

  async putEdgeEnrollment(req: EdgeEnrollmentInput): Promise<EdgeEnrollment> {
    const actor = this.requireAdminRole()
    if (this.onboardingCompleted()) this.requireAsset('asset.update', req.assetId)
    const detail = this.findAsset(req.assetId)
    const unitId = req.unitId.trim()
    const listenAddress = req.listenAddress.trim()
    const trafficKey = req.trafficKey.trim()
    const upstreamUrl = req.upstreamUrl.trim()
    if (unitId === '' || unitId.length > 64 || listenAddress === '' || trafficKey === '') {
      throw err('invalid_argument', 'unit_id, listen_address and traffic_key are required')
    }
    const trustedProxyCidrs = [...new Set(req.trustedProxyCidrs.map((value) => value.trim()).filter(Boolean))].sort()
    if (trustedProxyCidrs.length > 64 || trustedProxyCidrs.some((value) => !validProxyCIDR(value))) {
      throw err('invalid_argument', 'trusted_proxy_cidrs is invalid')
    }
    if (req.posture === 'INGRESS_POSTURE_REVERSE_PROXY') {
      let upstream: URL
      try {
        upstream = new URL(upstreamUrl)
      } catch {
        throw err('invalid_argument', 'upstream_url must be absolute')
      }
      if (!['http:', 'https:'].includes(upstream.protocol) || upstream.hostname === '' || upstream.username !== '' || upstream.password !== '' || upstream.hash !== '') {
        throw err('invalid_argument', 'upstream_url is invalid')
      }
    } else if (req.posture !== 'INGRESS_POSTURE_EXT_AUTHZ') {
      throw err('invalid_argument', 'unsupported edge enrollment posture')
    }
    const conflict = this.state.assets.find(
      (asset) => asset.asset.id !== req.assetId && asset.edgeEnrollments.some((enrollment) => enrollment.unitId === unitId),
    )
    if (conflict !== undefined) throw err('already_exists', `edge ${unitId} is already enrolled for another asset`)
    const normalized: EdgeEnrollmentInput = {
      ...structuredClone(req),
      assetId: req.assetId,
      unitId,
      listenAddress,
      upstreamUrl: req.posture === 'INGRESS_POSTURE_REVERSE_PROXY' ? upstreamUrl : '',
      trafficKey,
      trustedProxyCidrs,
      modelProfile: {
        ...structuredClone(req.modelProfile),
        profileId: req.modelProfile.profileId.trim(),
        modelGroup: req.modelProfile.modelGroup.trim(),
        modelType: req.modelProfile.modelType.trim(),
        modelVersion: req.modelProfile.modelVersion.trim(),
        allowedHeaders: [...new Set(req.modelProfile.allowedHeaders.map((value) => value.trim().toLowerCase()).filter(Boolean))].sort(),
      },
    }
    const specificationDigest = `sha256:${fakeHash(JSON.stringify(normalized)).repeat(8)}`
    const existingIndex = detail.edgeEnrollments.findIndex((enrollment) => enrollment.unitId === unitId)
    const existing = existingIndex >= 0 ? detail.edgeEnrollments[existingIndex] : undefined
    if (existing?.specificationDigest === specificationDigest) return structuredClone(existing)
    const nextListenPlan = String(Number(existing?.expectedListenPlanVersion ?? '0') + 1)
    const nextGenerationSeq = String(Number(existing?.expectedGenerationSeq ?? '0') + 1)
    const enrollment: EdgeEnrollment = {
      assetId: req.assetId,
      unitId,
      posture: normalized.posture,
      listenAddress,
      upstreamUrl: normalized.upstreamUrl,
      trafficKey,
      trustedProxyCidrs,
      modelProfile: normalized.modelProfile,
      modelIngressWindow: normalized.modelIngressWindow,
      modelsideId: `${unitId}-modelside`,
      specificationDigest,
      expectedListenPlanVersion: nextListenPlan,
      expectedGenerationId: `generation-${fakeHash(`${req.assetId}:${nextGenerationSeq}:${specificationDigest}`)}`,
      expectedGenerationSeq: nextGenerationSeq,
      status: existing?.lastHeartbeatAt === undefined
        ? 'EDGE_ENROLLMENT_STATUS_WAITING_FOR_REGISTRATION'
        : 'EDGE_ENROLLMENT_STATUS_OUT_OF_SYNC',
      lastHeartbeatAt: existing?.lastHeartbeatAt,
      currentListenPlanVersion: existing?.currentListenPlanVersion ?? '0',
      currentGenerationId: existing?.currentGenerationId ?? '',
      currentGenerationSeq: existing?.currentGenerationSeq ?? '0',
      modelsideStatus: existing?.modelsideStatus ?? 'EDGE_ENROLLMENT_STATUS_WAITING_FOR_REGISTRATION',
      modelsideLastResultAt: existing?.modelsideLastResultAt,
      modelProfileDigest: `sha256:${fakeHash(JSON.stringify(normalized.modelProfile)).repeat(8)}`,
    }
    if (existingIndex >= 0) detail.edgeEnrollments[existingIndex] = enrollment
    else detail.edgeEnrollments.push(enrollment)
    this.audit('asset.edge_enrollment.put', 'asset', req.assetId, actor.username)
    return structuredClone(enrollment)
  }

  async getEdgeEnrollment(assetId: string, unitId: string): Promise<EdgeEnrollment> {
    this.requireAsset('console.read', assetId)
    const enrollment = this.findAsset(assetId).edgeEnrollments.find((candidate) => candidate.unitId === unitId)
    if (enrollment === undefined) throw err('not_found', `edge ${unitId} is not enrolled for ${assetId}`)
    return structuredClone(enrollment)
  }

  async getTrafficReviewPolicy(assetId: string): Promise<TrafficReviewPolicyStatus> {
    this.requireAsset('console.read', assetId)
    this.findAsset(assetId)
    return structuredClone(this.trafficReviewPolicies.get(assetId) ?? {
      policy: {
        windowSeconds: 300,
        topRouteCells: 32,
        maxCandidatesPerWindow: 4,
        maxEvidenceBytes: 8192,
        vaultMaxBytes: '268435456',
        evidenceTtlSeconds: '86400',
        mode: 'TRAFFIC_REVIEW_MODE_OFF',
      },
      generationId: '',
      generationSeq: '0',
      policyDigest: '',
      edgeSupported: true,
    })
  }

  async updateTrafficReviewPolicy(assetId: string, mode: TrafficReviewMode, expectedGenerationId = ''): Promise<TrafficReviewPolicyStatus> {
    const actor = this.requireAsset('asset.update', assetId)
    const current = await this.getTrafficReviewPolicy(assetId)
    if (expectedGenerationId !== '' && expectedGenerationId !== current.generationId) throw err('failed_precondition', 'generation_mismatch')
    const order: TrafficReviewMode[] = [
      'TRAFFIC_REVIEW_MODE_OFF',
      'TRAFFIC_REVIEW_MODE_STATISTICS_ONLY',
      'TRAFFIC_REVIEW_MODE_REDACTED_CASES',
      'TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL',
      'TRAFFIC_REVIEW_MODE_SHADOW_CANDIDATES',
    ]
    const from = order.indexOf(current.policy.mode)
    const to = order.indexOf(mode)
    if (to < 0 || to > from + 1) throw err('failed_precondition', 'traffic review mode must be enabled one level at a time')
    const next: TrafficReviewPolicyStatus = {
      ...current,
      policy: { ...current.policy, mode },
      generationId: `generation-review-${Number(current.generationSeq) + 1}`,
      generationSeq: String(Number(current.generationSeq) + 1),
      policyDigest: `sha256:${fakeHash(`${assetId}:${mode}`)}`,
    }
    this.trafficReviewPolicies.set(assetId, next)
    this.audit('asset.traffic_review_policy.update', 'asset', assetId, actor.username)
    return structuredClone(next)
  }

  async getModelIngressWindow(assetId: string, unitId: string): Promise<ModelIngressWindowStatus> {
    this.requireAsset('console.read', assetId)
    const detail = this.findAsset(assetId)
    const unit = detail.units.find((candidate) => candidate.unitId === unitId && candidate.kind.toLowerCase() === 'edge')
    if (unit === undefined) throw err('not_found', `edge ${unitId} is not attached to ${assetId}`)
    const key = `${assetId}\u0000${unitId}`
    const desired: ModelIngressWindow = { maxItems: 4096, maxRetainedBytes: String(128 * 1024 * 1024), maxQueueAge: '2s' }
    return structuredClone(this.modelIngressWindows.get(key) ?? {
      assetId,
      unitId,
      desired,
      effective: desired,
      desiredListenPlanVersion: unit.currentListenPlanVersion ?? '1',
      appliedListenPlanVersion: unit.currentListenPlanVersion ?? '1',
      state: 'MODEL_INGRESS_WINDOW_STATE_APPLIED',
      degradationReasons: [],
    })
  }

  async updateModelIngressWindow(assetId: string, unitId: string, desired: ModelIngressWindow, expectedListenPlanVersion = ''): Promise<ModelIngressWindowStatus> {
    const actor = this.requireAsset('asset.update', assetId)
    const current = await this.getModelIngressWindow(assetId, unitId)
    if (expectedListenPlanVersion !== '' && expectedListenPlanVersion !== current.desiredListenPlanVersion) throw err('failed_precondition', 'listen_plan_version_mismatch')
    const unit = this.findAsset(assetId).units.find((candidate) => candidate.unitId === unitId)
    const hard = unit?.capabilities.modelIngressHardLimit
    if (unit === undefined || hard === undefined) throw err('failed_precondition', 'edge does not advertise model ingress window capability')
    const desiredAge = Number(desired.maxQueueAge.replace(/s$/, ''))
    const hardAge = Number(hard.maxQueueAge.replace(/s$/, ''))
    const reasons: ModelIngressWindowStatus['degradationReasons'] = []
    if (desired.maxItems > hard.maxItems) reasons.push('MODEL_INGRESS_DEGRADATION_REASON_MAX_ITEMS')
    if (Number(desired.maxRetainedBytes) > Number(hard.maxRetainedBytes)) reasons.push('MODEL_INGRESS_DEGRADATION_REASON_MAX_RETAINED_BYTES')
    if (desiredAge > hardAge) reasons.push('MODEL_INGRESS_DEGRADATION_REASON_MAX_QUEUE_AGE')
    const next: ModelIngressWindowStatus = {
      assetId,
      unitId,
      desired: structuredClone(desired),
      effective: {
        maxItems: Math.min(desired.maxItems, hard.maxItems),
        maxRetainedBytes: String(Math.min(Number(desired.maxRetainedBytes), Number(hard.maxRetainedBytes))),
        maxQueueAge: `${Math.min(desiredAge, hardAge)}s`,
      },
      desiredListenPlanVersion: String(Number(current.desiredListenPlanVersion) + 1),
      appliedListenPlanVersion: current.appliedListenPlanVersion,
      state: 'MODEL_INGRESS_WINDOW_STATE_CONVERGING',
      degradationReasons: reasons,
    }
    this.modelIngressWindows.set(`${assetId}\u0000${unitId}`, next)
    this.audit('asset.model_ingress_window.update', 'asset', assetId, actor.username)
    return structuredClone(next)
  }

  /* ----- ConsoleService ----- */

  async dashboard(): Promise<DashboardSummary> {
    this.requireOnboardingComplete()
    this.requireTool('console.read')
    const visible = this.visibleAssetIds()
    const releases = this.state.releases.filter((r) => (r.artifact?.scope?.assetIds ?? []).some((id) => visible.has(id)))
    const byState: Record<string, string> = {}
    for (const r of releases) {
      byState[r.state] = String(Number(byState[r.state] ?? '0') + 1)
    }
    const events = this.state.events.filter((e) => visible.has(e.assetId))
    const summary = createDashboard()
    summary.assetsTotal = String(visible.size)
    summary.events24hTotal = String(events.length)
    summary.events24hBlocked = String(events.filter((e) => e.verdict === 'VERDICT_BLOCK').length)
    summary.modelAlerts24h = String(events.filter((e) => e.kind === 'KIND_MODEL_ALERT').length)
    return { ...summary, releasesByState: byState }
  }

  async listEvents(filter: ListEventsFilter = {}, page: PageQuery = {}): Promise<Page<Event>> {
    this.requireOnboardingComplete()
    this.requireTool('console.read')
    const visible = this.visibleAssetIds()
    let events = this.state.events.filter((e) => visible.has(e.assetId))
    if (filter.assetId !== undefined && filter.assetId !== '') events = events.filter((e) => e.assetId === filter.assetId)
    if (filter.releaseId !== undefined && filter.releaseId !== '') {
      events = events.filter((e) => e.releaseTraces.some((t) => t.releaseId === filter.releaseId))
    }
    if (filter.verdict !== undefined) events = events.filter((e) => e.verdict === filter.verdict)
    if (filter.kind !== undefined) events = events.filter((e) => e.kind === filter.kind)
    if (filter.since !== undefined) events = events.filter((e) => e.occurredAt >= (filter.since ?? ''))
    if (filter.until !== undefined) events = events.filter((e) => e.occurredAt <= (filter.until ?? ''))
    if (filter.query !== undefined && filter.query !== '') {
      const q = filter.query
      events = events.filter(
        (e) => (e.http?.path.includes(q) ?? false) || e.detections.some((d) => d.ruleId.includes(q)),
      )
    }
    return this.paginate(structuredClone(events), page)
  }

  async getEvent(eventId: string): Promise<EventDetail> {
    this.requireOnboardingComplete()
    this.requireTool('console.read')
    const event = this.state.events.find((e) => e.id === eventId)
    if (event === undefined || !this.visibleAssetIds().has(event.assetId)) {
      throw err('permission_denied', 'object out of bindings')
    }
    return structuredClone({
      event,
      modelInferences: event.kind === 'KIND_MODEL_ALERT' || event.kind === 'KIND_MODEL_REVIEW_SAMPLE'
        ? [{
            inferenceId: `inference-${event.id}`,
            eventId: event.id,
            modelGroup: 'http-threat',
            modelType: 'PVM',
            modelVersion: 'gpvm-e9eceef3',
            threshold: 0.9,
            score: 0.97,
            attackClass: 'ATTACK_CLASS_SQLI',
            taxonomyVersion: 'http-threat/v1',
            recordedAt: event.occurredAt,
            modelProfileDigest: `sha256:${fakeHash('fixture-model-profile').repeat(8)}`,
            requestId: event.requestId,
            resultKind: event.kind === 'KIND_MODEL_ALERT' ? 'MODEL_RESULT_KIND_ALERT' : 'MODEL_RESULT_KIND_REVIEW_SAMPLE',
          }]
        : [],
      triageDeliveries: event.clusterId === ''
        ? []
        : [{
            caseId: this.state.cases.find((item) => item.assetId === event.assetId)?.caseId ?? '',
            instructionId: `instruction-${event.id}`,
            handlerId: 'jarvis',
            kind: 'INSTRUCTION_KIND_EVENT_TRIAGE',
            status: 'INSTRUCTION_STATUS_PENDING',
            createdAt: event.occurredAt,
          }],
    })
  }

  /* ----- GovernService：状态机按 docs/api.md §7.1 执行 ----- */

  private transition(release: Release, allowed: ReleaseState[], to: ReleaseState, actor: string): void {
    if (!allowed.includes(release.state)) {
      throw err('failed_precondition', `release is ${release.state}, cannot move to ${to}`, { reasonKey: 'release_state_conflict' })
    }
    release.state = to
    const at = '2026-08-16T00:00:00.000Z'
    if (to === 'RELEASE_STATE_SHADOW') release.shadowStartedAt = at
    if (to === 'RELEASE_STATE_CANARY') release.canaryStartedAt = at
    if (to === 'RELEASE_STATE_ENFORCE') release.enforcedAt = at
    this.audit(`release.${to.replace('RELEASE_STATE_', '').toLowerCase()}`, 'release', release.releaseId, actor)
  }

  async gateArtifact(releaseId: string): Promise<GateOutcome> {
    const release = this.findRelease(releaseId)
    const actor = this.requireReleaseTool('govern.gate', release)
    if (release.state !== 'RELEASE_STATE_DRAFT') {
      throw err('failed_precondition', `release is ${release.state}, gate requires draft`, { reasonKey: 'release_state_conflict' })
    }
    // 测试夹具门禁恒通过：写回放报告 → 计算 artifact id → signed（§7.3）
    const createdAt = '2026-08-16T00:00:00.000Z'
    release.artifact = {
      ...(release.artifact ?? { kind: 'KIND_RULE', payloadSchema: 'rules/v1', ttl: '86400s', supersedes: '', evidenceRefs: [], createdBy: release.createdBy }),
      id: `sha256:${fakeHash(releaseId).repeat(8)}`,
      createdAt,
      replayReport: {
        maliciousTotal: 120,
        maliciousBlocked: 120,
        benignTotal: 4800,
        benignBlocked: 0,
        passed: true,
        corpusRef: 'builtin:l1-rules-v1',
        managementTotal: 36,
        managementBlocked: 0,
      },
    }
    release.signedAt = createdAt
    this.transition(release, ['RELEASE_STATE_DRAFT'], 'RELEASE_STATE_SIGNED', actor.username)
    return structuredClone({ releaseId: release.releaseId, state: release.state, replayReport: release.artifact.replayReport })
  }

  async startShadow(releaseId: string): Promise<Release> {
    const release = this.findRelease(releaseId)
    const actor = this.requireReleaseTool('govern.start_shadow', release)
    this.transition(release, ['RELEASE_STATE_SIGNED'], 'RELEASE_STATE_SHADOW', actor.username)
    return structuredClone(release)
  }

  async promoteCanary(releaseId: string): Promise<Release> {
    const release = this.findRelease(releaseId)
    const actor = this.requirePromote('govern.promote_canary', release)
    if (release.state !== 'RELEASE_STATE_SHADOW') {
      throw err('failed_precondition', `release is ${release.state}, promote_canary requires shadow`, { reasonKey: 'release_state_conflict' })
    }
    // 门槛：shadow 请求数 ≥ 100（docs/api.md §7.5；测试夹具用统计块模拟）
    const shadow = this.state.stats[releaseId]?.shadow
    const requests = Number(shadow?.requests ?? '0')
    if (requests < 100) {
      throw err('failed_precondition', 'promotion gates not satisfied', {
        reasonKey: 'gate_not_satisfied',
        gateChecks: [
          { gateKey: 'shadow_min_requests', passed: false, required: '>= 100', actual: String(requests), message: '影子阶段请求数不足' },
          { gateKey: 'shadow_min_duration', passed: true, required: '>= 300s', actual: '302s', message: '影子阶段时长达标' },
          { gateKey: 'replay_passed', passed: true, required: 'true', actual: 'true', message: '回放门禁已通过' },
        ],
      })
    }
    this.transition(release, ['RELEASE_STATE_SHADOW'], 'RELEASE_STATE_CANARY', actor.username)
    return structuredClone(release)
  }

  async promoteEnforce(releaseId: string): Promise<Release> {
    const release = this.findRelease(releaseId)
    const actor = this.requirePromote('govern.promote_enforce', release)
    if (release.state !== 'RELEASE_STATE_CANARY' && release.state !== 'RELEASE_STATE_SHADOW') {
      throw err('failed_precondition', `release is ${release.state}, promote_enforce requires canary or shadow`, {
        reasonKey: 'release_state_conflict',
      })
    }
    if (release.state === 'RELEASE_STATE_CANARY') {
      // 门槛：deny_feedback_total == 0 且守护窗口健康（docs/api.md §7.6）
      const stats = this.state.stats[releaseId]
      const denyTotal = Number(stats?.canary?.denyFeedbackTotal ?? '0')
      const badWindows = stats?.guard?.consecutiveBadWindows ?? 0
      if (denyTotal > 0 || badWindows > 0) {
        throw err('failed_precondition', 'promotion gates not satisfied', {
          reasonKey: 'gate_not_satisfied',
          gateChecks: [
            { gateKey: 'deny_feedback_total', passed: denyTotal === 0, required: '== 0', actual: String(denyTotal), message: '存在未处理的误报举报' },
            { gateKey: 'guard_windows', passed: badWindows === 0, required: '== 0', actual: String(badWindows), message: '守护窗口存在连续异常' },
          ],
        })
      }
    }
    this.transition(release, ['RELEASE_STATE_CANARY', 'RELEASE_STATE_SHADOW'], 'RELEASE_STATE_ENFORCE', actor.username)
    return structuredClone(release)
  }

  async rollbackRelease(releaseId: string, reason: string): Promise<Release> {
    return this.retire(releaseId, reason, 'RETIRE_REASON_ROLLBACK')
  }

  async retireRelease(releaseId: string, reason: string): Promise<Release> {
    return this.retire(releaseId, reason, 'RETIRE_REASON_MANUAL')
  }

  private async retire(releaseId: string, reason: string, why: Release['retireReason']): Promise<Release> {
    const release = this.findRelease(releaseId)
    const tool = why === 'RETIRE_REASON_ROLLBACK' ? 'govern.rollback' : 'govern.retire'
    const actor = this.requireReleaseTool(tool, release)
    if (!['RELEASE_STATE_SHADOW', 'RELEASE_STATE_CANARY', 'RELEASE_STATE_ENFORCE'].includes(release.state)) {
      throw err('failed_precondition', `release is ${release.state}, retire requires shadow/canary/enforce`, { reasonKey: 'release_state_conflict' })
    }
    release.state = 'RELEASE_STATE_RETIRED'
    release.retiredAt = '2026-08-16T00:00:00.000Z'
    release.retireReason = why
    this.audit(why === 'RETIRE_REASON_ROLLBACK' ? 'release.rollback' : 'release.retire', 'release', releaseId, actor.username, reason)
    return structuredClone(release)
  }

  async denyFeedback(releaseId: string, eventId: string, note: string): Promise<Release> {
    const release0 = this.findRelease(releaseId)
    const actor = this.requireReleaseTool('govern.deny_feedback', release0)
    if (note.trim() === '' || note.length > 2000) throw err('invalid_argument', 'note must be 1-2000 characters')
    const release = this.findRelease(releaseId)
    if (!['RELEASE_STATE_CANARY', 'RELEASE_STATE_ENFORCE'].includes(release.state)) {
      throw err('failed_precondition', 'deny feedback requires canary/enforce release', { reasonKey: 'release_state_conflict' })
    }
    const event = this.state.events.find((e) => e.id === eventId)
    if (event === undefined) throw err('not_found', `event ${eventId} not found`)
    if (event.verdict !== 'VERDICT_BLOCK') throw err('failed_precondition', 'deny feedback requires a BLOCK event')
    if (!event.releaseTraces.some((t) => t.releaseId === releaseId)) {
      throw err('failed_precondition', `event ${eventId} does not belong to ${releaseId}`)
    }
    const stats = this.state.stats[releaseId]
    const win = release.state === 'RELEASE_STATE_CANARY' ? stats?.canary : stats?.enforce
    if (win !== undefined) win.denyFeedbackTotal = String(Number(win.denyFeedbackTotal) + 1)
    this.audit('release.deny_feedback', 'release', releaseId, actor.username)
    return structuredClone(release)
  }

  async getRelease(releaseId: string): Promise<Release> {
    this.requireOnboardingComplete()
    this.requireTool('console.read')
    const release = this.findRelease(releaseId)
    const ids = release.artifact?.scope?.assetIds ?? []
    const visible = this.visibleAssetIds()
    if (ids.length === 0 || !ids.every((id) => visible.has(id))) throw err('permission_denied', 'object out of bindings')
    return structuredClone(release)
  }

  async listReleases(filter: ListReleasesFilter = {}, page: PageQuery = {}): Promise<Page<Release>> {
    this.requireOnboardingComplete()
    this.requireTool('console.read')
    const visible = this.visibleAssetIds()
    let releases = this.state.releases.filter((r) => (r.artifact?.scope?.assetIds ?? []).every((id) => visible.has(id)))
    if (filter.states !== undefined && filter.states.length > 0) releases = releases.filter((r) => filter.states?.includes(r.state))
    if (filter.assetId !== undefined && filter.assetId !== '') {
      releases = releases.filter((r) => r.artifact?.scope?.assetIds.includes(filter.assetId ?? '') ?? false)
    }
    if (filter.query !== undefined && filter.query !== '') {
      const q = filter.query
      releases = releases.filter((r) => r.releaseId.includes(q) || (r.artifact?.id.includes(q) ?? false) || r.createdBy.includes(q))
    }
    return this.paginate(structuredClone(releases), page)
  }

  async getReleaseTimeline(releaseId: string, page: PageQuery = {}): Promise<Page<TimelineEntry>> {
    this.me()
    this.findRelease(releaseId)
    const entries = this.state.timelines[releaseId] ?? []
    return this.paginate(structuredClone([...entries].reverse()), page)
  }

  async getReleaseStats(releaseId: string): Promise<ReleaseStats> {
    this.me()
    const release = this.findRelease(releaseId)
    const stats = this.state.stats[releaseId]
    return structuredClone(
      stats ?? {
        releaseId,
        state: release.state,
        guard: { consecutiveBadWindows: 0, lastBadReasons: [] },
        computedAt: '2026-08-16T00:00:00.000Z',
      },
    )
  }

  /* ----- AuditService ----- */

  async listAuditEntries(filter: ListAuditFilter = {}, page: PageQuery = {}): Promise<Page<AuditEntry>> {
    this.requireOnboardingComplete()
    this.me()
    let entries = [...this.state.audit].reverse()
    if (filter.objectType !== undefined && filter.objectType !== '') entries = entries.filter((e) => e.objectType === filter.objectType)
    if (filter.objectId !== undefined && filter.objectId !== '') entries = entries.filter((e) => e.objectId.includes(filter.objectId ?? ''))
    if (filter.actor !== undefined && filter.actor !== '') entries = entries.filter((e) => e.actorId.includes(filter.actor ?? ''))
    if (filter.since !== undefined) entries = entries.filter((e) => e.occurredAt >= (filter.since ?? ''))
    if (filter.until !== undefined) entries = entries.filter((e) => e.occurredAt <= (filter.until ?? ''))
    return this.paginate(structuredClone(entries), page)
  }

  async verifyChain(startSequence: string, endSequence: string): Promise<ChainVerification> {
    this.me()
    const start = Number(startSequence)
    const end = Number(endSequence)
    if (!Number.isInteger(start) || !Number.isInteger(end) || start < 1 || end < start) {
      throw err('invalid_argument', 'invalid sequence range')
    }
    const slice = this.state.audit.filter((e) => Number(e.sequence) >= start && Number(e.sequence) <= end)
    if (slice.length === 0) throw err('not_found', 'no entries in range')
    // 测试夹具链恒完整：逐条校验 prev 链接
    let valid = true
    for (let i = 1; i < slice.length; i++) {
      if (slice[i].previousHash !== slice[i - 1].entryHash) valid = false
    }
    return {
      valid,
      startHash: slice[0].previousHash,
      endHash: slice[slice.length - 1].entryHash,
      entriesChecked: slice.length,
    }
  }

  /* ----- GrantService（docs/api.md §6.1） ----- */

  async listGrants(subjectUserId?: string): Promise<Grant[]> {
    const me = this.me()
    const subject = subjectUserId !== undefined && subjectUserId !== '' ? subjectUserId : me.userId
    if (subject !== me.userId) this.requireTool('grant.write')
    return structuredClone(this.state.grants.filter((g) => g.subjectUserId === subject))
  }

  async putGrant(req: { subjectUserId: string; tools: string[]; bindings: BindingRef[] }): Promise<Grant> {
    const actor = this.requireTool('grant.write')
    if (req.subjectUserId === actor.userId) throw err('permission_denied', 'cannot grant to self', { reasonKey: 'grant_self' })
    if (req.bindings.some((b) => b.id === '' || b.id === '*')) {
      throw err('permission_denied', 'wildcard bindings forbidden', { reasonKey: 'grant_wildcard' })
    }
    const mine = this.expandAccess(actor.userId).bindings
    const accountOnly = new Set(['user.admin', 'grant.write', 'catalog.manage', 'worker.enroll', 'worker.capacity.approve'])
    const needsAsset = req.tools.some((tool) => !accountOnly.has(tool))
    if (needsAsset && req.bindings.length === 0) throw err('permission_denied', 'wildcard bindings forbidden', { reasonKey: 'grant_wildcard' })
    if (!req.bindings.every((b) => mine.some((m) => m.kind === b.kind && m.id === b.id))) {
      throw err('permission_denied', 'granted scope exceeds caller bindings', { reasonKey: 'grant_scope' })
    }
    const grant: Grant = {
      grantId: `gr_${fakeHash(req.subjectUserId + req.tools.join(','))}`,
      subjectUserId: req.subjectUserId,
      tools: [...req.tools],
      bindings: [...req.bindings],
      createdBy: actor.username,
      createdAt: '2026-08-16T00:00:00.000Z',
    }
    this.state.grants = this.state.grants.filter((g) => g.subjectUserId !== req.subjectUserId)
    this.state.grants.push(grant)
    this.audit('grant.put', 'user', req.subjectUserId, actor.username)
    return structuredClone(grant)
  }

  async revokeGrant(grantId: string): Promise<void> {
    const actor = this.requireTool('grant.write')
    const idx = this.state.grants.findIndex((g) => g.grantId === grantId)
    if (idx < 0) throw err('not_found', `grant ${grantId} not found`)
    this.state.grants.splice(idx, 1)
    this.audit('grant.revoke', 'grant', grantId, actor.username)
  }

  /* ----- OnboardingService ----- */

  private projectOnboarding(): Onboarding {
    const o = this.state.onboarding
    return {
      state: o.state,
      baseUrl: o.baseUrl,
      model: o.model,
      dialect: o.dialect ?? 'MODEL_DIALECT_OPENAI_CHAT',
      hasSecret: o.hasSecret,
      secretHint: o.secretHint,
      jarvisOnline: o.jarvisOnline,
      lastError: o.lastError,
      updatedAt: o.updatedAt,
    }
  }

  async getOnboarding(): Promise<Onboarding> {
    this.me()
    return this.projectOnboarding()
  }

  async putModelConfig(req: { baseUrl: string; secret: string; model?: string; dialect?: ModelDialect }): Promise<void> {
    const actor = this.me()
    if (actor.role !== 'USER_ROLE_ADMIN') throw err('permission_denied', 'only admin may put model config')
    if (this.onboardingCompleted()) throw err('failed_precondition', 'onboarding already completed')
    if (!req.baseUrl.startsWith('https://')) {
      throw err('invalid_argument', 'base_url must be an absolute https url')
    }
    if (req.secret.trim() === '') throw err('invalid_argument', 'secret is required')
    const model = req.model !== undefined && req.model !== '' ? req.model : 'grok-4-1-fast-non-reasoning'
    const hint = `****${req.secret.slice(-4)}`
    this.state.onboarding.baseUrl = req.baseUrl
    this.state.onboarding.secret = req.secret
    this.state.onboarding.hasSecret = true
    this.state.onboarding.secretHint = hint
    this.state.onboarding.model = model
    this.state.onboarding.dialect = req.dialect ?? 'MODEL_DIALECT_OPENAI_CHAT'
    this.state.onboarding.modelLiveOk = false
    this.state.onboarding.lastError = ''
    this.state.onboarding.state = 'ONBOARDING_STATE_MODEL_CONFIGURED'
    this.state.onboarding.updatedAt = '2026-08-16T00:00:00.000Z'
  }

  async testModelConnectivity(): Promise<void> {
    const actor = this.me()
    if (actor.role !== 'USER_ROLE_ADMIN') throw err('permission_denied', 'only admin may test model')
    if (!this.state.onboarding.hasSecret) {
      this.state.onboarding.state = 'ONBOARDING_STATE_FAILED'
      this.state.onboarding.lastError = 'model secret is missing'
      throw err('failed_precondition', 'model secret is missing')
    }
    if (this.modelTestFailsLeft > 0) {
      this.modelTestFailsLeft -= 1
      this.state.onboarding.modelLiveOk = false
      this.state.onboarding.lastError = 'model endpoint unreachable'
      this.state.onboarding.state = 'ONBOARDING_STATE_FAILED'
      throw err('unavailable', 'model endpoint unreachable')
    }
    this.state.onboarding.modelLiveOk = true
    this.state.onboarding.lastError = ''
    this.state.onboarding.state = 'ONBOARDING_STATE_MODEL_LIVE'
  }

  async completeOnboarding(): Promise<void> {
    const actor = this.me()
    if (actor.role !== 'USER_ROLE_ADMIN') throw err('permission_denied', 'only admin may complete onboarding')
    const o = this.state.onboarding
    const missing: number[] = []
    if (!o.modelLiveOk || !o.hasSecret) missing.push(1)
    if (!o.jarvisOnline) missing.push(2)
    if (missing.length > 0) {
      throw new ApiError({
        code: 'failed_precondition',
        message: 'onboarding predicates not met',
        httpStatus: 0,
        missingPredicates: missing,
      })
    }
    this.state.grants = this.state.grants.filter((g) => g.subjectUserId !== actor.userId)
    this.state.grants.push({
      grantId: 'gr_admin_system',
      subjectUserId: actor.userId,
      tools: [
        'grant.write',
        'user.admin',
        'console.read',
        'asset.create',
        'asset.update',
        'asset.delete',
        'asset.attach',
        'asset.detach',
      ],
      bindings: this.state.assets.map((a) => ({ kind: 'asset' as const, id: a.asset.id })),
      createdBy: 'system',
      createdAt: '2026-08-16T00:00:00.000Z',
    })
    o.state = 'ONBOARDING_STATE_COMPLETED'
    o.lastError = ''
  }

  async getModelGateway(): Promise<ModelGateway> {
    const actor = this.me()
    if (actor.role !== 'USER_ROLE_ADMIN') throw err('permission_denied', 'admin role required')
    this.requireOnboardingComplete()
    return projectModelGateway(this.state.onboarding, this.state.gatewayCalls)
  }

  async updateModelGateway(req: { baseUrl: string; secret?: string; model?: string; dialect?: ModelDialect }): Promise<ModelGateway> {
    const actor = this.me()
    if (actor.role !== 'USER_ROLE_ADMIN') throw err('permission_denied', 'admin role required')
    this.requireOnboardingComplete()
    if (!req.baseUrl.startsWith('https://')) {
      throw err('invalid_argument', 'base_url must be an absolute https url')
    }
    const o = this.state.onboarding
    const secret = req.secret ?? ''
    if (secret.trim() !== '') {
      o.secret = secret
      o.hasSecret = true
      o.secretHint = `****${secret.slice(-4)}`
    }
    o.baseUrl = req.baseUrl
    o.model = req.model !== undefined && req.model !== '' ? req.model : o.model !== '' ? o.model : 'grok-4-1-fast-non-reasoning'
    if (req.dialect !== undefined) o.dialect = req.dialect
    o.lastError = ''
    o.updatedAt = new Date().toISOString()
    return projectModelGateway(o, this.state.gatewayCalls)
  }

  async probeModelGateway(): Promise<{ ok: boolean; lastError: string }> {
    const actor = this.me()
    if (actor.role !== 'USER_ROLE_ADMIN') throw err('permission_denied', 'admin role required')
    this.requireOnboardingComplete()
    const o = this.state.onboarding
    if (!o.hasSecret) throw err('failed_precondition', 'model secret is missing')
    this.state.gatewayCalls.push({
      at: new Date().toISOString(),
      kind: 'probe',
      ok: true,
      host: modelHostOf(o.baseUrl),
      model: o.model,
      error: '',
    })
    return { ok: true, lastError: '' }
  }

  /* ----- ProposeArtifact ----- */

  async proposeArtifact(req: ProposeArtifactRequest): Promise<Release> {
    this.requireOnboardingComplete()
    const actor = this.requireTool('govern.propose')
    if (req.intent === undefined || req.intent.kind === 'PROPOSAL_KIND_UNSPECIFIED') {
      throw err('failed_precondition', 'production requires proposal intent')
    }
    const ids = req.scope.assetIds
    if (ids.length === 0) throw err('invalid_argument', 'scope.asset_ids is required')
    const visible = this.visibleAssetIds()
    if (!ids.every((id) => visible.has(id))) throw err('permission_denied', 'object out of bindings')
    const release: Release = {
      releaseId: `rel_${fakeHash(req.intent.clusterId ?? actor.userId + String(this.state.releases.length))}`,
      state: 'RELEASE_STATE_DRAFT',
      artifact: {
        id: '',
        kind: req.intent.kind === 'PROPOSAL_KIND_SHAPE' ? 'KIND_UNSPECIFIED' : 'KIND_UNSPECIFIED',
        payloadSchema: req.intent.kind === 'PROPOSAL_KIND_POLICY' ? 'policy/v1' : 'shape/v1',
        scope: { assetIds: [...ids], routeSelector: req.intent.routeTemplate ?? '' },
        ttl: req.ttl ?? '86400s',
        supersedes: '',
        evidenceRefs: req.evidenceRefs ?? [],
        createdBy: actor.userId,
      },
      proposedAt: '2026-08-16T00:00:00.000Z',
      retireReason: 'RETIRE_REASON_UNSPECIFIED',
      createdBy: actor.userId,
    }
    this.state.releases.unshift(release)
    this.audit('release.propose', 'release', release.releaseId, actor.username)
    return structuredClone(release)
  }

  /* ----- SessionService：只认属主，不查授予 ----- */

  async createSession(req: { title?: string } = {}): Promise<{ sessionId: string }> {
    this.requireOnboardingComplete()
    const user = this.me()
    const sessionId = `ses_${fakeHash(user.userId + String(this.state.chats.length))}`
    this.state.chats.push({ sessionId, owner: user.userId, title: req.title ?? '', messages: [] })
    return { sessionId }
  }

  // 与生产 session.reply 对齐：短 UTF-8 正文。不编思考/工具/实例/待盖印信封。
  private jarvisTurn(content: string): string {
    void content
    return '收到。我不会把它当成命令去执行。'
  }

  private chatOf(sessionId: string, userId: string): FixtureChatSession {
    const chat = this.state.chats.find((c) => c.sessionId === sessionId)
    if (chat === undefined) throw err('not_found', 'session not found')
    if (chat.owner !== userId) throw err('permission_denied', 'session belongs to another user')
    return chat
  }

  async sendMessage(req: { sessionId: string; content: string }): Promise<{ messageSequence: string }> {
    this.requireOnboardingComplete()
    const user = this.me()
    if (req.content === '' || req.content.length > 8192) throw err('invalid_argument', 'content must be 1-8192 bytes')
    const chat = this.chatOf(req.sessionId, user.userId)
    const seq = String(chat.messages.length + 1)
    chat.messages.push({
      sequence: seq,
      sessionId: req.sessionId,
      sender: user.userId,
      content: req.content,
      occurredAt: '2026-08-16T00:00:00.000Z',
    })
    const replySeq = String(chat.messages.length + 1)
    chat.messages.push({
      sequence: replySeq,
      sessionId: req.sessionId,
      sender: 'jarvis-1',
      content: this.jarvisTurn(req.content),
      occurredAt: '2026-08-16T00:00:01.000Z',
    })
    return { messageSequence: seq }
  }

  async pollMessages(req: { sessionId: string; cursor?: string; longPollSeconds?: number }): Promise<{
    messages: ChatMessage[]
    nextCursor: string
  }> {
    this.requireOnboardingComplete()
    const user = this.me()
    if (req.longPollSeconds !== undefined && req.longPollSeconds > 60) {
      throw err('invalid_argument', 'long_poll_seconds exceeds max')
    }
    const chat = this.chatOf(req.sessionId, user.userId)
    const after = Number(req.cursor ?? '0')
    const messages = chat.messages.filter((m) => Number(m.sequence) > (Number.isFinite(after) ? after : 0))
    const last = messages.length > 0 ? messages[messages.length - 1].sequence : String(Number.isFinite(after) ? after : 0)
    return { messages: structuredClone(messages), nextCursor: last }
  }

  async listMessages(req: { sessionId: string }, page: PageQuery = {}): Promise<Page<ChatMessage>> {
    this.requireOnboardingComplete()
    const user = this.me()
    const chat = this.chatOf(req.sessionId, user.userId)
    return this.paginate(structuredClone([...chat.messages].reverse()), page)
  }

  async listCases(filter: ListCasesFilter = {}, page: PageQuery = {}): Promise<Page<InvestigationCase>> {
    this.requireTool('case.read')
    const visible = this.visibleAssetIds()
    let cases = this.state.cases.filter((item) => visible.has(item.assetId))
    if (filter.assetId !== undefined && filter.assetId !== '') cases = cases.filter((item) => item.assetId === filter.assetId)
    if (filter.moduleId !== undefined && filter.moduleId !== '') cases = cases.filter((item) => item.moduleId === filter.moduleId)
    if (filter.state !== undefined) cases = cases.filter((item) => item.state === filter.state)
    return this.paginate(structuredClone(cases), page)
  }

  async getCase(caseId: string): Promise<InvestigationCase> {
    this.requireTool('case.read')
    const item = this.state.cases.find((candidate) => candidate.caseId === caseId)
    if (item === undefined || !this.visibleAssetIds().has(item.assetId)) throw err('permission_denied', 'object out of bindings')
    return structuredClone(item)
  }

  async pollCaseActivities(req: { caseId: string; afterSequence?: string; longPollSeconds?: number }): Promise<{
    activities: CaseActivity[]
    nextAfterSequence: string
  }> {
    await this.getCase(req.caseId)
    const after = Number(req.afterSequence ?? '0')
    const activities = (this.state.caseActivities[req.caseId] ?? []).filter((activity) => Number(activity.sequence) > after)
    return { activities: structuredClone(activities), nextAfterSequence: activities.at(-1)?.sequence ?? String(after) }
  }

  async resolveCase(req: { caseId: string; resolution: CaseResolution; note?: string }): Promise<InvestigationCase> {
    this.requireAsset('case.manage', (await this.getCase(req.caseId)).assetId)
    const item = this.state.cases.find((candidate) => candidate.caseId === req.caseId)
    if (item === undefined || item.state === 'INVESTIGATION_CASE_STATE_RESOLVED') throw err('failed_precondition', 'case is already resolved')
    item.state = 'INVESTIGATION_CASE_STATE_RESOLVED'
    item.resolution = req.resolution
    item.resolvedAt = new Date().toISOString()
    item.updatedAt = item.resolvedAt
    return structuredClone(item)
  }

  async reopenCase(req: { caseId: string; note?: string }): Promise<InvestigationCase> {
    this.requireAsset('case.manage', (await this.getCase(req.caseId)).assetId)
    const item = this.state.cases.find((candidate) => candidate.caseId === req.caseId)
    if (item === undefined || !['INVESTIGATION_CASE_STATE_RESOLVED', 'INVESTIGATION_CASE_STATE_FAILED', 'INVESTIGATION_CASE_STATE_EVIDENCE_EXPIRED'].includes(item.state)) throw err('failed_precondition', 'case is not terminal')
    item.state = 'INVESTIGATION_CASE_STATE_OPEN'
    item.resolution = 'CASE_RESOLUTION_UNSPECIFIED'
    item.resolvedAt = undefined
    item.updatedAt = new Date().toISOString()
    return structuredClone(item)
  }

  async recordCaseFeedback(req: { caseId: string; resolution: CaseResolution; note?: string }): Promise<InvestigationCase> {
    const item = await this.getCase(req.caseId)
    this.requireAsset('case.manage', item.assetId)
    return item
  }

  async getApproval(approvalId: string): Promise<ApprovalView> {
    const approval = this.state.approvals.find((item) => item.approvalId === approvalId)
    if (approval === undefined) throw err('not_found', 'approval not found')
    if (approval.kind === 'APPROVAL_KIND_EVIDENCE') this.requireAsset('case.read', approval.assetId)
    else this.requireTool('worker.capacity.approve')
    return structuredClone(approval)
  }

  async decideApproval(req: { approvalId: string; approved: boolean; reason?: string }): Promise<{ approvalId: string; state: string }> {
    const approval = this.state.approvals.find((item) => item.approvalId === req.approvalId)
    if (approval === undefined) throw err('not_found', 'approval not found')
    if (approval.state !== 'pending') throw err('failed_precondition', 'approval is expired or already decided')
    if (approval.kind === 'APPROVAL_KIND_EVIDENCE') this.requireAsset('evidence.approve', approval.assetId)
    else this.requireTool('worker.capacity.approve')
    approval.state = req.approved ? 'approved' : 'denied'
    return { approvalId: approval.approvalId, state: approval.state }
  }

  async listModules(): Promise<DefenseModule[]> {
    this.me()
    return structuredClone(this.state.modules)
  }

  async listAgentProfiles(page: PageQuery = {}): Promise<Page<ManagedAgentProfile>> {
    this.requireTool('console.read')
    const visible = this.visibleAssetIds()
    const canManage = hasTool(this.myAccess(), 'agent.manage')
    const profiles = this.state.agentProfiles
      .filter((profile) => profile.bindings.some((binding) => binding.kind === 'asset' && visible.has(binding.id)))
      .map((profile) => ({
        ...profile,
        bindings: profile.bindings.filter((binding) => binding.kind === 'asset' && visible.has(binding.id)),
        canManage: canManage && profile.bindings.every((binding) => binding.kind === 'asset' && visible.has(binding.id)),
      }))
    return this.paginate(structuredClone(profiles), page)
  }

  async createAgentProfile(req: AgentProfileInput): Promise<ManagedAgentProfile> {
    this.requireTool('agent.manage')
    const input = normalizedProfileInput(req)
    const visible = this.visibleAssetIds()
    if (!input.bindings.every((binding) => visible.has(binding.id))) throw err('permission_denied', 'object out of bindings')
    const suffix = String(this.state.agentProfiles.length + 1).padStart(2, '0')
    const profile: ManagedAgentProfile = {
      agentId: `profile-review-${suffix}`,
      displayName: input.displayName,
      kind: 'AGENT_PROFILE_KIND_TRAFFIC_REVIEW',
      state: 'AGENT_PROFILE_STATE_ENABLED',
      tools: input.tools,
      bindings: input.bindings,
      canManage: true,
      createdBy: this.me().userId,
      createdAt: '2026-08-16T00:10:00.000Z',
      updatedAt: '2026-08-16T00:10:00.000Z',
    }
    this.state.agentProfiles.unshift(profile)
    return structuredClone(profile)
  }

  async updateAgentProfile(req: AgentProfileInput & { agentId: string; state: AgentProfileState }): Promise<ManagedAgentProfile> {
    this.requireTool('agent.manage')
    if (req.agentId.toLowerCase().startsWith('jarvis')) throw err('invalid_argument', 'managed agent id is required')
    const input = normalizedProfileInput(req)
    if (req.state !== 'AGENT_PROFILE_STATE_ENABLED' && req.state !== 'AGENT_PROFILE_STATE_DISABLED') throw err('invalid_argument', 'managed agent state is required')
    const profile = this.state.agentProfiles.find((item) => item.agentId === req.agentId)
    if (profile === undefined) throw err('permission_denied', 'object out of bindings')
    const visible = this.visibleAssetIds()
    if (![...profile.bindings, ...input.bindings].every((binding) => visible.has(binding.id))) throw err('permission_denied', 'object out of bindings')
    Object.assign(profile, input, { state: req.state, updatedAt: '2026-08-16T00:11:00.000Z' })
    return structuredClone(profile)
  }

  async deleteAgentProfile(agentId: string): Promise<void> {
    this.requireTool('agent.manage')
    if (agentId.toLowerCase().startsWith('jarvis')) throw err('invalid_argument', 'managed agent id is required')
    const index = this.state.agentProfiles.findIndex((item) => item.agentId === agentId)
    if (index < 0) throw err('permission_denied', 'object out of bindings')
    const profile = this.state.agentProfiles[index]
    const visible = this.visibleAssetIds()
    if (!profile.bindings.every((binding) => visible.has(binding.id))) throw err('permission_denied', 'object out of bindings')
    profile.state = 'AGENT_PROFILE_STATE_TOMBSTONED'
    profile.tombstonedAt = '2026-08-16T00:13:00.000Z'
    profile.updatedAt = profile.tombstonedAt
  }

  async batchUpdateAgentProfiles(req: { agentIds: string[]; tools: string[]; bindings: BindingRef[] }): Promise<ManagedAgentProfile[]> {
    this.requireTool('agent.manage')
    const ids = [...new Set(req.agentIds)]
    if (ids.length === 0 || ids.some((id) => id.toLowerCase().startsWith('jarvis'))) throw err('invalid_argument', 'managed agent ids must not include jarvis')
    const input = normalizedProfileInput({ displayName: 'batch', tools: req.tools, bindings: req.bindings })
    const profiles = ids.map((id) => this.state.agentProfiles.find((item) => item.agentId === id))
    if (profiles.some((profile) => profile === undefined)) throw err('permission_denied', 'object out of bindings')
    const visible = this.visibleAssetIds()
    if (![...profiles.flatMap((profile) => profile?.bindings ?? []), ...input.bindings].every((binding) => visible.has(binding.id))) {
      throw err('permission_denied', 'object out of bindings')
    }
    for (const profile of profiles) {
      if (profile === undefined) continue
      profile.tools = [...input.tools]
      profile.bindings = structuredClone(input.bindings)
      profile.updatedAt = '2026-08-16T00:12:00.000Z'
    }
    return structuredClone(profiles as ManagedAgentProfile[])
  }

  async listWorkers(page: PageQuery = {}): Promise<Page<WorkerRecord>> {
    this.requireTool('worker.enroll')
    return this.paginate(structuredClone(this.state.workers), page)
  }

  async listWorkerEnrollments(state = '', page: PageQuery = {}): Promise<Page<WorkerEnrollmentRecord>> {
    this.requireTool('worker.enroll')
    const values = state === '' ? this.state.workerEnrollments : this.state.workerEnrollments.filter((item) => item.state === state)
    return this.paginate(structuredClone(values), page)
  }

  async decideWorkerEnrollment(req: { enrollmentId: string; approved: boolean; bindings: string[]; maxConcurrency?: number }): Promise<WorkerEnrollmentDecision> {
    this.requireTool('worker.enroll')
    const enrollment = this.state.workerEnrollments.find((item) => item.enrollmentId === req.enrollmentId)
    if (enrollment === undefined) throw err('not_found', 'worker enrollment not found')
    if (enrollment.state !== 'pending') throw err('failed_precondition', 'worker enrollment was already decided')
    enrollment.state = req.approved ? 'approved' : 'denied'
    enrollment.bindings = [...req.bindings]
    enrollment.maxConcurrency = req.maxConcurrency ?? 1
    return {
      enrollmentId: enrollment.enrollmentId, state: enrollment.state,
	  activationBundleRef: req.approved ? `activation:${enrollment.enrollmentId}` : '',
	  approvedManifestDigest: req.approved ? `sha256:${fakeHash(enrollment.enrollmentId)}` : '',
    }
  }
}
