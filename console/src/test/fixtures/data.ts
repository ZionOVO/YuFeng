// 页面测试夹具使用固定基准时钟、标识与计数，保证渲染断言可重复。
// 本文件只从测试目录导入，不进入控制台运行时依赖图。

import type {
  Artifact,
  AssetDetail,
  AuditEntry,
  DashboardSummary,
  Event,
  ManagedAgentProfile,
  Grant,
  Release,
  ReleaseStats,
  TimelineEntry,
  UnitProjection,
  User,
} from '../../api/types'

export const ALL_ASSET_BINDINGS = [
  { kind: 'asset' as const, id: 'asset-01' },
  { kind: 'asset' as const, id: 'asset-02' },
  { kind: 'asset' as const, id: 'asset-03' },
]

/** 全部时间戳的固定基准（不读真实时钟，保证确定性）。 */
export const FIXTURE_EPOCH = '2026-08-16T00:00:00.000Z'

function iso(offsetSeconds: number): string {
  return new Date(Date.parse(FIXTURE_EPOCH) + offsetSeconds * 1000).toISOString()
}

/** 系统引导授予：具体 ID 快照，不是 *。 */
export const FIXTURE_GRANTS: Grant[] = [
  {
    grantId: 'gr_admin_scope',
    subjectUserId: 'usr_01',
    tools: [
      'console.read',
      'user.admin',
      'grant.write',
      'asset.create',
      'asset.update',
      'asset.delete',
      'asset.attach',
      'asset.detach',
      'govern.propose',
      'govern.gate',
      'govern.start_shadow',
      'case.read',
      'case.manage',
      'evidence.approve',
      'worker.enroll',
      'worker.capacity.approve',
      'agent.manage',
    ],
    bindings: ALL_ASSET_BINDINGS,
    createdBy: 'system',
    createdAt: iso(-86400 * 30),
  },
  {
    grantId: 'gr_chen_ops',
    subjectUserId: 'usr_02',
    tools: ['console.read', 'govern.propose', 'govern.gate', 'govern.start_shadow', 'run.create', 'case.read', 'case.manage'],
    bindings: [
      { kind: 'asset', id: 'asset-01' },
      { kind: 'asset', id: 'asset-02' },
    ],
    createdBy: 'system',
    createdAt: iso(-86400 * 21),
  },
  {
    grantId: 'gr_li_read',
    subjectUserId: 'usr_03',
    tools: ['console.read', 'case.read'],
    bindings: [{ kind: 'asset', id: 'asset-01' }],
    createdBy: 'system',
    createdAt: iso(-86400 * 14),
  },
  {
    grantId: 'gr_wu_promote',
    subjectUserId: 'usr_05',
    tools: ['console.read', 'govern.promote_canary', 'govern.promote_enforce', 'govern.rollback', 'govern.retire', 'govern.deny_feedback'],
    bindings: ALL_ASSET_BINDINGS,
    createdBy: 'system',
    createdAt: iso(-86400 * 10),
  },
]

/** FNV-1a 32 位哈希的十六进制形式，用于生成确定性的伪散列字段。 */
export function fakeHash(input: string): string {
  let h = 0x811c9dc5
  for (let i = 0; i < input.length; i++) {
    h ^= input.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return (h >>> 0).toString(16).padStart(8, '0')
}

/* ---------- 用户（含演示口令，仅 ConsoleClientFixture 校验用） ---------- */

export interface FixtureAccount {
  user: User
  password: string
}

export const FIXTURE_ACCOUNTS: FixtureAccount[] = [
  {
    user: { userId: 'usr_01', username: 'admin', displayName: '管理员', role: 'USER_ROLE_ADMIN', state: 'USER_STATE_ACTIVE', createdAt: iso(-86400 * 30), lastLoginAt: iso(-3600) },
    password: 'admin123456',
  },
  {
    user: { userId: 'usr_02', username: 'operator-chen', displayName: '陈运维', role: 'USER_ROLE_OPERATOR', state: 'USER_STATE_ACTIVE', createdAt: iso(-86400 * 21), lastLoginAt: iso(-7200) },
    password: 'operator123456',
  },
  {
    user: { userId: 'usr_03', username: 'viewer-li', displayName: '李观察', role: 'USER_ROLE_VIEWER', state: 'USER_STATE_ACTIVE', createdAt: iso(-86400 * 14), lastLoginAt: iso(-18000) },
    password: 'viewer123456',
  },
  {
    user: { userId: 'usr_04', username: 'temp-ops', displayName: '临时账户', role: 'USER_ROLE_OPERATOR', state: 'USER_STATE_DISABLED', createdAt: iso(-86400 * 7) },
    password: 'disabled123456',
  },
  {
    user: { userId: 'usr_05', username: 'promoter-wu', displayName: '吴推进', role: 'USER_ROLE_OPERATOR', state: 'USER_STATE_ACTIVE', createdAt: iso(-86400 * 10), lastLoginAt: iso(-4000) },
    password: 'promoter123456',
  },
]

/** 演示令牌：固定字符串，便于测试断言；真实令牌由 brain 签发。 */
export function fixtureTokenFor(username: string): string {
  return `fixture-token-${username}`
}

export const FIXTURE_SESSION_EXPIRES_AT = '2026-08-16T12:00:00.000Z'

export function createManagedAgentProfiles(): ManagedAgentProfile[] {
  return [
    {
      agentId: 'profile-checkout-review',
      displayName: '结算流量审查员',
      kind: 'AGENT_PROFILE_KIND_TRAFFIC_REVIEW',
      state: 'AGENT_PROFILE_STATE_ENABLED',
      tools: ['case.get', 'case.request_evidence', 'run.create', 'case.complete'],
      bindings: [{ kind: 'asset', id: 'asset-01' }],
      executionMode: 'AGENT_EXECUTION_MODE_EPHEMERAL_RUN',
      configDigest: `sha256:${fakeHash('profile-checkout-review')}`,
      activeRunCount: 1,
      lastRunAt: iso(-900),
      lastWorkerId: 'agentd-central',
      lastWorkerPlatform: 'linux/amd64',
      canManage: true,
      createdBy: 'usr_01',
      createdAt: iso(-86400 * 8),
      updatedAt: iso(-3600),
    },
    {
      agentId: 'profile-mall-review',
      displayName: '商城流量审查员',
      kind: 'AGENT_PROFILE_KIND_TRAFFIC_REVIEW',
      state: 'AGENT_PROFILE_STATE_ENABLED',
      tools: ['case.get', 'case.request_evidence', 'run.create', 'case.complete'],
      bindings: [{ kind: 'asset', id: 'asset-02' }],
      executionMode: 'AGENT_EXECUTION_MODE_EPHEMERAL_RUN',
      configDigest: `sha256:${fakeHash('profile-mall-review')}`,
      activeRunCount: 0,
      lastWorkerId: '',
      lastWorkerPlatform: '',
      canManage: true,
      createdBy: 'usr_01',
      createdAt: iso(-86400 * 5),
      updatedAt: iso(-7200),
    },
  ]
}

/* ---------- 资产 ---------- */

function producerUnit(unitId: string): UnitProjection {
  return {
    unitId,
    kind: 'edge',
    version: 'dev',
    health: 'UNIT_HEALTH_HEALTHY',
    posture: 'INGRESS_POSTURE_REVERSE_PROXY',
    trafficKey: 'enterprise-site',
    lastHeartbeatAt: iso(-30),
    capabilities: {
      outputs: ['PRODUCER_OUTPUT_CRITICAL_EVENT', 'PRODUCER_OUTPUT_ORDINARY_SAMPLE', 'PRODUCER_OUTPUT_TICKET_FEATURES'],
      projectionVersions: ['event/v1'],
      postures: ['INGRESS_POSTURE_REVERSE_PROXY', 'INGRESS_POSTURE_EXT_AUTHZ'],
      sensors: ['SENSOR_TYPE_HTTP', 'SENSOR_TYPE_CORAZA'],
      localEvidenceRing: true,
      localAsyncBypass: true,
      maxEventBatch: 100,
      maxInFlightRequests: 4096,
      maxSpoolBytes: String(64 * 1024 * 1024),
      maxEvidenceEntries: 1024,
      modelIngressHardLimit: { maxItems: 16384, maxRetainedBytes: String(256 * 1024 * 1024), maxQueueAge: '300s' },
      maxModelIngressBatchItems: 32,
    },
    producerHealth: {
      bufferedCriticalEvents: '0',
      bufferedOrdinarySamples: '0',
      droppedCriticalEvents: '0',
      droppedOrdinarySamples: '0',
      droppedLocalBypassItems: '0',
      projectionFailures: '0',
      healthyProjectionVersions: ['event/v1'],
      effectiveModelIngressWindow: { maxItems: 4096, maxRetainedBytes: String(128 * 1024 * 1024), maxQueueAge: '2s' },
      modelIngressWindowState: 'MODEL_INGRESS_WINDOW_STATE_APPLIED',
      modelIngressDegradationReasons: [],
      modelIngressQueuedItems: '0',
      modelIngressQueuedBytes: '0',
      modelIngressInFlightItems: '0',
      modelIngressInFlightBytes: '0',
      modelIngressOldestAgeMillis: '0',
      modelIngressDrops: { evictedOldest: '0', expired: '0', itemTooLarge: '0', inFlightCapacity: '0', transportFailed: '0', modelsideRejected: '0', admissionBudget: '0' },
    },
    currentListenPlanVersion: '1',
  }
}

export function createAssets(): AssetDetail[] {
  return [
    {
      asset: {
        id: 'asset-01',
        displayName: 'core-payments',
        accessMode: 'ACCESS_MODE_EMBEDDED',
        transports: [{ kind: 'KIND_LOCAL', endpoint: 'local://yufeng-host' }],
        capabilities: {
          kernelVersion: '6.1.0',
          bpfLsm: true,
          seccomp: true,
          nftables: true,
          landlock: true,
          packageManagers: ['apt'],
        },
        criticality: 'CRITICALITY_P0',
        maxAutoTier: 'TIER_L2_RUNTIME',
        labels: { env: 'prod', biz: 'payments' },
        lastProbeAt: iso(-45),
      },
      unitIds: ['unit-edge-01'],
      units: [producerUnit('unit-edge-01')],
      edgeEnrollments: [],
      health: 'UNIT_HEALTH_HEALTHY',
      activeReleaseCount: 2,
    },
    {
      asset: {
        id: 'asset-02',
        displayName: 'mall-gateway',
        accessMode: 'ACCESS_MODE_EMBEDDED',
        transports: [
          { kind: 'KIND_LOCAL', endpoint: 'local://yufeng-host' },
          { kind: 'KIND_SSH', endpoint: 'ssh://10.0.0.22:22' },
        ],
        capabilities: {
          kernelVersion: '5.15.0',
          bpfLsm: true,
          seccomp: false,
          nftables: true,
          landlock: false,
          packageManagers: ['apt', 'apk'],
        },
        criticality: 'CRITICALITY_P1',
        maxAutoTier: 'TIER_L1_TRAFFIC',
        labels: { env: 'prod', biz: 'mall' },
        lastProbeAt: iso(-120),
      },
      unitIds: ['unit-edge-02'],
      units: [producerUnit('unit-edge-02')],
      edgeEnrollments: [],
      health: 'UNIT_HEALTH_HEALTHY',
      activeReleaseCount: 1,
    },
    {
      asset: {
        id: 'asset-03',
        displayName: 'legacy-erp',
        accessMode: 'ACCESS_MODE_NETWORK',
        transports: [],
        criticality: 'CRITICALITY_P2',
        maxAutoTier: 'TIER_L0_REPORT',
        labels: { env: 'staging', biz: 'erp' },
      },
      unitIds: [],
      units: [],
      edgeEnrollments: [],
      health: 'UNIT_HEALTH_UNSPECIFIED',
      activeReleaseCount: 0,
    },
  ]
}

/* ---------- 发布 ---------- */

function artifact(partial: Partial<Artifact> & Pick<Artifact, 'createdBy'>): Artifact {
  return {
    id: '',
    kind: 'KIND_RULE',
    payloadSchema: 'rules/v1',
    scope: { assetIds: ['asset-01'], routeSelector: '' },
    ttl: '86400s',
    supersedes: '',
    evidenceRefs: [],
    ...partial,
  }
}

function signedArtifact(id: string, createdBy: string, createdAt: string): Artifact {
  return artifact({
    id: `sha256:${fakeHash(id).repeat(8)}`,
    createdBy,
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
  })
}

export function createReleases(): Release[] {
  return [
    {
      releaseId: 'rel_01J8SQGT',
      state: 'RELEASE_STATE_ENFORCE',
      artifact: signedArtifact('rel_01J8SQGT', 'operator-chen', iso(-86400 * 4)),
      proposedAt: iso(-86400 * 4),
      signedAt: iso(-86400 * 4 + 120),
      shadowStartedAt: iso(-86400 * 4 + 600),
      canaryStartedAt: iso(-86400 * 4 + 3600),
      enforcedAt: iso(-86400 * 4 + 10800),
      retireReason: 'RETIRE_REASON_UNSPECIFIED',
      createdBy: 'operator-chen',
    },
    {
      releaseId: 'rel_01J8TM12',
      state: 'RELEASE_STATE_ENFORCE',
      artifact: signedArtifact('rel_01J8TM12', 'jarvis', iso(-86400 * 3)),
      proposedAt: iso(-86400 * 3),
      signedAt: iso(-86400 * 3 + 150),
      shadowStartedAt: iso(-86400 * 3 + 900),
      canaryStartedAt: iso(-86400 * 3 + 4200),
      enforcedAt: iso(-86400 * 3 + 14400),
      retireReason: 'RETIRE_REASON_UNSPECIFIED',
      createdBy: 'jarvis',
    },
    {
      releaseId: 'rel_01J8VN8P',
      state: 'RELEASE_STATE_CANARY',
      artifact: signedArtifact('rel_01J8VN8P', 'operator-chen', iso(-86400 * 2)),
      proposedAt: iso(-86400 * 2),
      signedAt: iso(-86400 * 2 + 180),
      shadowStartedAt: iso(-86400 * 2 + 1200),
      canaryStartedAt: iso(-86400 * 2 + 5400),
      retireReason: 'RETIRE_REASON_UNSPECIFIED',
      createdBy: 'operator-chen',
    },
    {
      releaseId: 'rel_01J8W33K',
      state: 'RELEASE_STATE_CANARY',
      artifact: signedArtifact('rel_01J8W33K', 'admin', iso(-86400)),
      proposedAt: iso(-86400),
      signedAt: iso(-86400 + 200),
      shadowStartedAt: iso(-86400 + 1500),
      canaryStartedAt: iso(-86400 + 6600),
      retireReason: 'RETIRE_REASON_UNSPECIFIED',
      createdBy: 'admin',
    },
    {
      releaseId: 'rel_01J8XH7D',
      state: 'RELEASE_STATE_SHADOW',
      artifact: signedArtifact('rel_01J8XH7D', 'jarvis', iso(-3600 * 6)),
      proposedAt: iso(-3600 * 6),
      signedAt: iso(-3600 * 6 + 160),
      shadowStartedAt: iso(-3600 * 6 + 800),
      retireReason: 'RETIRE_REASON_UNSPECIFIED',
      createdBy: 'jarvis',
    },
    {
      releaseId: 'rel_01J90N2Q',
      state: 'RELEASE_STATE_SHADOW',
      artifact: signedArtifact('rel_01J90N2Q', 'operator-chen', iso(-3600 * 5)),
      proposedAt: iso(-3600 * 5),
      signedAt: iso(-3600 * 5 + 140),
      shadowStartedAt: iso(-3600 * 5 + 700),
      retireReason: 'RETIRE_REASON_UNSPECIFIED',
      createdBy: 'operator-chen',
    },
    {
      releaseId: 'rel_01J8YPC9',
      state: 'RELEASE_STATE_SIGNED',
      artifact: signedArtifact('rel_01J8YPC9', 'operator-chen', iso(-3600 * 2)),
      proposedAt: iso(-3600 * 2),
      signedAt: iso(-3600 * 2 + 170),
      retireReason: 'RETIRE_REASON_UNSPECIFIED',
      createdBy: 'operator-chen',
    },
    {
      releaseId: 'rel_01J8ZTB4',
      state: 'RELEASE_STATE_DRAFT',
      artifact: artifact({ createdBy: 'admin' }),
      proposedAt: iso(-3600),
      retireReason: 'RETIRE_REASON_UNSPECIFIED',
      createdBy: 'admin',
    },
    {
      releaseId: 'rel_01J90K6F',
      state: 'RELEASE_STATE_RETIRED',
      artifact: signedArtifact('rel_01J90K6F', 'operator-chen', iso(-86400 * 6)),
      proposedAt: iso(-86400 * 6),
      signedAt: iso(-86400 * 6 + 130),
      shadowStartedAt: iso(-86400 * 6 + 700),
      canaryStartedAt: iso(-86400 * 6 + 4000),
      enforcedAt: iso(-86400 * 6 + 12000),
      retiredAt: iso(-86400 * 5),
      retireReason: 'RETIRE_REASON_ROLLBACK',
      createdBy: 'operator-chen',
    },
  ]
}

/** 各发布统计块。rel_01J8XH7D 的 shadow 请求数不足（门槛失败路径），rel_01J90N2Q 充足（成功路径）。 */
export function createStats(): Record<string, ReleaseStats> {
  const window = (requests: string, overrides: Partial<ReleaseStats['shadow'] & object> = {}) => ({
    duration: '302s',
    requests,
    blocks: '2',
    observes: '0',
    canarySelected: '6',
    denyFeedbackTotal: '0',
    upstream5xx: '0',
    p99Micros: '18200',
    ...overrides,
  })
  return {
    rel_01J8SQGT: {
      releaseId: 'rel_01J8SQGT',
      state: 'RELEASE_STATE_ENFORCE',
      shadow: window('612'),
      canary: window('453'),
      enforce: window('9811', { canarySelected: '0' }),
      guard: { consecutiveBadWindows: 0, lastBadReasons: [] },
      computedAt: iso(-60),
    },
    rel_01J8TM12: {
      releaseId: 'rel_01J8TM12',
      state: 'RELEASE_STATE_ENFORCE',
      shadow: window('540'),
      canary: window('388'),
      enforce: window('7204', { canarySelected: '0' }),
      guard: { consecutiveBadWindows: 0, lastBadReasons: [] },
      computedAt: iso(-60),
    },
    rel_01J8VN8P: {
      releaseId: 'rel_01J8VN8P',
      state: 'RELEASE_STATE_CANARY',
      shadow: window('128'),
      canary: window('302'),
      guard: { consecutiveBadWindows: 0, lastBadReasons: [] },
      computedAt: iso(-60),
    },
    // 误报举报计数非零：PromoteEnforce 门槛失败路径
    rel_01J8W33K: {
      releaseId: 'rel_01J8W33K',
      state: 'RELEASE_STATE_CANARY',
      shadow: window('144'),
      canary: window('356', { denyFeedbackTotal: '1' }),
      guard: { consecutiveBadWindows: 0, lastBadReasons: [] },
      computedAt: iso(-60),
    },
    // shadow 请求数低于门槛：PromoteCanary 失败路径
    rel_01J8XH7D: {
      releaseId: 'rel_01J8XH7D',
      state: 'RELEASE_STATE_SHADOW',
      shadow: window('12', { blocks: '0' }),
      guard: { consecutiveBadWindows: 0, lastBadReasons: [] },
      computedAt: iso(-60),
    },
    // shadow 指标健康：PromoteCanary 成功路径
    rel_01J90N2Q: {
      releaseId: 'rel_01J90N2Q',
      state: 'RELEASE_STATE_SHADOW',
      shadow: window('156', { blocks: '1' }),
      guard: { consecutiveBadWindows: 0, lastBadReasons: [] },
      computedAt: iso(-60),
    },
  }
}

/* ---------- 事件（57 条，可验证分页与筛选） ---------- */

const EVENT_PATHS = ['/api/items', '/login', '/api/users', '/api/orders', '/api/search']
const EVENT_RULES = ['sql-union', 'xss-reflected', 'path-traversal', 'ua-scanner', 'sensitive-file-read']
/** 带策略轨迹的事件归属这两个在役防护策略（canary / enforce 各一）。 */
const TRACE_TARGETS = [
  { releaseId: 'rel_01J8VN8P', mode: 'RELEASE_MODE_CANARY' as const },
  { releaseId: 'rel_01J8SQGT', mode: 'RELEASE_MODE_ENFORCE' as const },
]

export const FIXTURE_EVENT_TOTAL = 57

export function createEvents(): Event[] {
  const events: Event[] = []
  for (let i = 0; i < FIXTURE_EVENT_TOTAL; i++) {
    const isBlock = i % 7 === 0
    const isObserve = !isBlock && i % 5 === 0
    const verdict: Event['verdict'] = isBlock ? 'VERDICT_BLOCK' : isObserve ? 'VERDICT_OBSERVE' : 'VERDICT_ALLOW'
    const kind: Event['kind'] = i % 11 === 10 ? 'KIND_SENSOR' : 'KIND_TRAFFIC'
    const assetId = `asset-0${(i % 3) + 1}`
    const id = `evt_${fakeHash(`event-${i}`)}`
    const key = isBlock || isObserve
    const event: Event = {
      id,
      occurredAt: iso(-i * 137),
      assetId,
      source: 'yufeng-edge',
      kind,
      verdict,
      detections: [],
      labels: key ? { campaign: 'demo-week-33' } : {},
      unitId: `unit-edge-0${(i % 2) + 1}`,
      requestId: fakeHash(`req-${i}`),
      releaseTraces: [],
      coverage: [],
      observation: isBlock || isObserve ? 'OBSERVATION_STATE_SYNC_DETECTED' : 'OBSERVATION_STATE_SYNC_NO_DETECTION',
      triageReason: isBlock ? 'TRIAGE_REASON_DETECTED_UNMITIGATED' : 'TRIAGE_REASON_UNSPECIFIED',
      generationId: '',
      generationSeq: '0',
      clusterId: '',
      wouldHaveBlocked: isObserve,
      ingressPosture: 'INGRESS_POSTURE_REVERSE_PROXY',
      trafficKey: '',
    }
    if (kind === 'KIND_TRAFFIC') {
      event.http = {
        method: i % 4 === 3 ? 'POST' : 'GET',
        path: EVENT_PATHS[i % EVENT_PATHS.length],
        queryRedacted: isBlock ? 'id=1%20UNION%20SELECT' : '',
        headersRedacted: { host: 'api.example.internal', 'user-agent': 'curl/8.x' },
        bodyRedacted: '',
        srcPseudonym: `src-${fakeHash(`src-${i}`)}`,
        dst: '10.0.0.12:443',
        statusCode: isBlock ? 403 : 200,
        latencyMicros: String(9000 + i * 137),
      }
    } else {
      event.labels = { ...event.labels, probe: 'process' }
    }
    if (key) {
      event.detections = [
        {
          detectorId: 'det-waf-rules',
          ruleId: EVENT_RULES[i % EVENT_RULES.length],
          confidence: 1,
          message: '命中检测规则',
          tier: 'TIER_L1_TRAFFIC',
        },
      ]
      const target = TRACE_TARGETS[i % TRACE_TARGETS.length]
      event.releaseTraces = [
        {
          releaseId: target.releaseId,
          artifactId: `sha256:${fakeHash(target.releaseId).repeat(8)}`,
          mode: target.mode,
          canaryPercent: target.mode === 'RELEASE_MODE_CANARY' ? 5 : 0,
          canarySelected: target.mode === 'RELEASE_MODE_CANARY' && i % 2 === 0,
          matched: true,
        },
      ]
    }
    events.push(event)
  }
  return events
}

/** 给 DenyFeedback 用的确定锚点：第一条 BLOCK 事件与其归属发布。 */
export const FIXTURE_BLOCK_EVENT_ID = `evt_${fakeHash('event-0')}`
export const FIXTURE_BLOCK_EVENT_RELEASE_ID = TRACE_TARGETS[0].releaseId

/* ---------- 审计（23 条，哈希链形状确定） ---------- */

const AUDIT_ACTIONS: [string, string, string, string][] = [
  // [action, objectType, objectId, actorId]
  ['auth.login', 'user', 'usr_02', 'operator-chen'],
  ['asset.update', 'asset', 'asset-01', 'operator-chen'],
  ['unit.attach', 'asset', 'asset-03', 'admin'],
  ['release.propose', 'release', 'rel_01J8ZTB4', 'admin'],
  ['release.gate', 'release', 'rel_01J8YPC9', 'operator-chen'],
  ['release.start_shadow', 'release', 'rel_01J8XH7D', 'jarvis'],
  ['release.promote_canary', 'release', 'rel_01J8VN8P', 'operator-chen'],
  ['release.deny_feedback', 'release', 'rel_01J8W33K', 'operator-chen'],
  ['release.promote_enforce', 'release', 'rel_01J8SQGT', 'operator-chen'],
  ['release.rollback', 'release', 'rel_01J90K6F', 'operator-chen'],
  ['user.create', 'user', 'usr_03', 'admin'],
  ['user.update', 'user', 'usr_04', 'admin'],
  ['auth.password_change', 'user', 'usr_02', 'operator-chen'],
]

export const FIXTURE_AUDIT_TOTAL = 23

export function createAuditEntries(): AuditEntry[] {
  const entries: AuditEntry[] = []
  let previousHash = fakeHash('genesis')
  for (let i = 0; i < FIXTURE_AUDIT_TOTAL; i++) {
    const [action, objectType, objectId, actorId] = AUDIT_ACTIONS[i % AUDIT_ACTIONS.length]
    const sequence = String(i + 1)
    const entryHash = fakeHash(`${previousHash}:${sequence}:${action}:${objectId}`)
    entries.push({
      sequence,
      occurredAt: iso(-(FIXTURE_AUDIT_TOTAL - i) * 733),
      actorType: actorId.startsWith('unit-') ? 'unit' : 'user',
      actorId,
      action,
      objectType,
      objectId,
      details: `{"note":"fixture entry ${sequence}"}`,
      previousHash,
      entryHash,
    })
    previousHash = entryHash
  }
  return entries
}

/* ---------- 控制台总览 ---------- */

export function createDashboard(): DashboardSummary {
  return {
    assetsTotal: '3',
    degradedUnits: '0',
    releasesByState: {
      RELEASE_STATE_DRAFT: '1',
      RELEASE_STATE_SIGNED: '1',
      RELEASE_STATE_SHADOW: '2',
      RELEASE_STATE_CANARY: '2',
      RELEASE_STATE_ENFORCE: '2',
      RELEASE_STATE_RETIRED: '1',
    },
    events24hTotal: '1241',
    events24hBlocked: '37',
    modelAlerts24h: '0',
    pendingRetireSoon: '0',
  }
}

/* ---------- 时间线（按发布状态推导） ---------- */

export function createTimelines(): Record<string, TimelineEntry[]> {
  const timelines: Record<string, TimelineEntry[]> = {}
  for (const r of createReleases()) {
    const entries: TimelineEntry[] = []
    let seq = 0
    const push = (from: TimelineEntry['fromState'], to: TimelineEntry['toState'], at: string | undefined, reason = '', gateReportRef = '') => {
      seq += 1
      entries.push({
        sequence: String(seq),
        releaseId: r.releaseId,
        fromState: from,
        toState: to,
        actor: r.createdBy,
        reason,
        gateReportRef,
        occurredAt: at ?? iso(0),
      })
    }
    push('RELEASE_STATE_UNSPECIFIED', 'RELEASE_STATE_DRAFT', r.proposedAt)
    if (r.signedAt !== undefined) push('RELEASE_STATE_DRAFT', 'RELEASE_STATE_SIGNED', r.signedAt, '', 'builtin:l1-rules-v1')
    if (r.shadowStartedAt !== undefined) push('RELEASE_STATE_SIGNED', 'RELEASE_STATE_SHADOW', r.shadowStartedAt)
    if (r.canaryStartedAt !== undefined) push('RELEASE_STATE_SHADOW', 'RELEASE_STATE_CANARY', r.canaryStartedAt)
    if (r.enforcedAt !== undefined) push('RELEASE_STATE_CANARY', 'RELEASE_STATE_ENFORCE', r.enforcedAt)
    if (r.retiredAt !== undefined) {
      const from = r.enforcedAt !== undefined ? 'RELEASE_STATE_ENFORCE' : r.canaryStartedAt !== undefined ? 'RELEASE_STATE_CANARY' : 'RELEASE_STATE_SHADOW'
      push(from, 'RELEASE_STATE_RETIRED', r.retiredAt, 'rollback requested by operator')
    }
    timelines[r.releaseId] = entries
  }
  return timelines
}
