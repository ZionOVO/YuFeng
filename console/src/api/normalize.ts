// protojson 省略空 repeated、空 map 与零值枚举。Connect 入口在此补齐页面按必有字段读取的默认值。
// 缺 detections / unitIds / labels 时列表页会抛错，React 无错误边界即整页空白。

import type {
  AccessMode,
  AgentExecutionMode,
  AgentProfileKind,
  AgentProfileState,
  AiPayload,
  ApprovalView,
  Artifact,
  ArtifactKind,
  Asset,
  AssetDetail,
  EdgeEnrollment,
  EdgeEnrollmentStatus,
  AttackClass,
  BindingRef,
  CapabilityMatrix,
  CoverageStatus,
  Criticality,
  CaseActivity,
  CaseActivityKind,
  CaseResolution,
  DashboardSummary,
  DefenseModule,
  Detection,
  DetectionKey,
  Event,
  EventDetail,
  EventKind,
  EffectiveAccess,
  Grant,
  HttpPayload,
  IngressPosture,
  InspectionCoverage,
  InspectionSurface,
  InvestigationCase,
  InvestigationCaseState,
  ManagedAgentProfile,
  ObservationState,
  ReviewCandidate,
  Release,
  ReleaseStats,
  ReleaseWindowStats,
  ReleaseMode,
  ReleaseState,
  ReleaseTrace,
  RetireReason,
  Scope,
  Session,
  ShapeSource,
  Tier,
  TrafficFinding,
  TrafficFindingDisposition,
  TrafficReviewMode,
  TrafficReviewPolicyStatus,
  ModelIngressWindow,
  ModelIngressWindowStatus,
  ModelInference,
  ModelProfile,
  Transport,
  TriageReason,
  TriageDelivery,
  UnitProjection,
  User,
  UserRole,
  UserState,
  Verdict,
  WorkerEnrollmentDecision,
  WorkerEnrollmentRecord,
  WorkerRecord,
} from './types'

function isObj(v: unknown): v is Record<string, unknown> {
  return v !== undefined && v !== null && typeof v === 'object' && !Array.isArray(v)
}

function asObj(v: unknown): Record<string, unknown> {
  return isObj(v) ? v : {}
}

function asArray(v: unknown): unknown[] {
  return Array.isArray(v) ? v : []
}

function asString(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

function asNumber(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : 0
}

function asBool(v: unknown): boolean {
  return v === true
}

function optionalString(v: unknown): string | undefined {
  return typeof v === 'string' && v !== '' ? v : undefined
}

function asStringRecord(v: unknown): Record<string, string> {
  if (!isObj(v)) return {}
  const out: Record<string, string> = {}
  for (const [k, val] of Object.entries(v)) {
    if (typeof val === 'string') out[k] = val
  }
  return out
}

function asNumberRecord(v: unknown): Record<string, number> {
  if (!isObj(v)) return {}
  const out: Record<string, number> = {}
  for (const [k, val] of Object.entries(v)) {
    if (typeof val === 'number' && Number.isFinite(val)) out[k] = val
  }
  return out
}

function normalizeBinding(raw: unknown): BindingRef {
  const src = asObj(raw)
  return {
    kind: asString(src.kind) as BindingRef['kind'],
    id: asString(src.id),
  }
}

/** normalizeUser 补齐认证与用户列表中可能省略的枚举和文本字段。 */
export function normalizeUser(raw: unknown): User {
  const src = asObj(raw)
  return {
    userId: asString(src.userId),
    username: asString(src.username),
    displayName: asString(src.displayName),
    role: (asString(src.role) || 'USER_ROLE_UNSPECIFIED') as UserRole,
    state: (asString(src.state) || 'USER_STATE_UNSPECIFIED') as UserState,
    createdAt: optionalString(src.createdAt),
    updatedAt: optionalString(src.updatedAt),
    lastLoginAt: optionalString(src.lastLoginAt),
  }
}

/** normalizeEffectiveAccess 保证空权限投影仍有可安全读取的 Tools 与 Bindings。 */
export function normalizeEffectiveAccess(raw: unknown): EffectiveAccess {
  const src = asObj(raw)
  return {
    tools: asArray(src.tools).map(asString),
    bindings: asArray(src.bindings).map(normalizeBinding),
  }
}

/** normalizeSession 补齐登录响应中的用户与权限投影。 */
export function normalizeSession(raw: unknown): Session {
  const src = asObj(raw)
  return {
    token: asString(src.token),
    expiresAt: asString(src.expiresAt),
    user: normalizeUser(src.user),
    access: normalizeEffectiveAccess(src.access),
  }
}

/** normalizeGrant 补齐兼容授予页直接读取的 repeated 字段。 */
export function normalizeGrant(raw: unknown): Grant {
  const src = asObj(raw)
  return {
    grantId: asString(src.grantId),
    subjectUserId: asString(src.subjectUserId),
    tools: asArray(src.tools).map(asString),
    bindings: asArray(src.bindings).map(normalizeBinding),
    createdBy: asString(src.createdBy),
    createdAt: asString(src.createdAt),
    expiresAt: optionalString(src.expiresAt),
  }
}

// normalizeEvent 把 ListEvents/GetEvent 的 protojson 补成 Event 约定形状。
export function normalizeEvent(raw: unknown): Event {
  const src = asObj(raw)
  return {
    id: asString(src.id),
    occurredAt: asString(src.occurredAt),
    assetId: asString(src.assetId),
    source: asString(src.source),
    kind: (asString(src.kind) || 'KIND_UNSPECIFIED') as EventKind,
    verdict: (asString(src.verdict) || 'VERDICT_UNSPECIFIED') as Verdict,
    http: src.http !== undefined ? normalizeHttp(src.http) : undefined,
    ai: src.ai !== undefined ? normalizeAi(src.ai) : undefined,
    detections: asArray(src.detections).map(normalizeDetection),
    labels: asStringRecord(src.labels),
    unitId: asString(src.unitId),
    requestId: asString(src.requestId),
    releaseTraces: asArray(src.releaseTraces).map(normalizeTrace),
    coverage: asArray(src.coverage).map(normalizeCoverage),
    observation: (asString(src.observation) || 'OBSERVATION_STATE_UNSPECIFIED') as ObservationState,
    triageReason: (asString(src.triageReason) || 'TRIAGE_REASON_UNSPECIFIED') as TriageReason,
    generationId: asString(src.generationId),
    generationSeq: asString(src.generationSeq) || '0',
    clusterId: asString(src.clusterId),
    wouldHaveBlocked: asBool(src.wouldHaveBlocked),
    ingressPosture: (asString(src.ingressPosture) || 'INGRESS_POSTURE_UNSPECIFIED') as IngressPosture,
    trafficKey: asString(src.trafficKey),
  }
}

/** normalizeEventDetail 补齐异步模型推理与贾维斯交付的只读投影。 */
export function normalizeEventDetail(raw: unknown): EventDetail {
  const src = asObj(raw)
  return {
    event: normalizeEvent(src.event),
    modelInferences: asArray(src.modelInferences).map(normalizeModelInference),
    triageDeliveries: asArray(src.triageDeliveries).map(normalizeTriageDelivery),
  }
}

function normalizeModelInference(raw: unknown): ModelInference {
  const inference = asObj(raw)
  return {
    inferenceId: asString(inference.inferenceId),
    eventId: asString(inference.eventId),
    modelGroup: asString(inference.modelGroup),
    modelType: asString(inference.modelType),
    modelVersion: asString(inference.modelVersion),
    threshold: asNumber(inference.threshold),
    score: asNumber(inference.score),
    attackClass: (asString(inference.attackClass) || 'ATTACK_CLASS_UNSPECIFIED') as AttackClass,
    taxonomyVersion: asString(inference.taxonomyVersion),
    recordedAt: optionalString(inference.recordedAt),
    modelProfileDigest: asString(inference.modelProfileDigest),
    requestId: asString(inference.requestId),
    resultKind: asString(inference.resultKind),
  }
}

function normalizeTriageDelivery(raw: unknown): TriageDelivery {
  const delivery = asObj(raw)
  return {
    caseId: asString(delivery.caseId),
    instructionId: asString(delivery.instructionId),
    handlerId: asString(delivery.handlerId),
    kind: asString(delivery.kind),
    status: asString(delivery.status),
    createdAt: optionalString(delivery.createdAt),
    acknowledgedAt: optionalString(delivery.acknowledgedAt),
  }
}

function normalizeCoverage(raw: unknown): InspectionCoverage {
  const c = asObj(raw)
  return {
    target: (asString(c.target) || 'INSPECTION_SURFACE_UNSPECIFIED') as InspectionSurface,
    status: (asString(c.status) || 'COVERAGE_STATUS_UNSPECIFIED') as CoverageStatus,
    inspectedBytes: asString(c.inspectedBytes) || String(asNumber(c.inspectedBytes)),
    totalBytesKnown: asString(c.totalBytesKnown) || String(asNumber(c.totalBytesKnown)),
  }
}

function normalizeHttp(raw: unknown): HttpPayload {
  const h = asObj(raw)
  return {
    method: asString(h.method),
    path: asString(h.path),
    queryRedacted: asString(h.queryRedacted),
    headersRedacted: asStringRecord(h.headersRedacted),
    bodyRedacted: asString(h.bodyRedacted),
    srcPseudonym: asString(h.srcPseudonym),
    dst: asString(h.dst),
    statusCode: asNumber(h.statusCode),
    latencyMicros: asString(h.latencyMicros) || '0',
  }
}

function normalizeAi(raw: unknown): AiPayload {
  const a = asObj(raw)
  return {
    provider: asString(a.provider),
    model: asString(a.model),
    roleCounts: asNumberRecord(a.roleCounts),
    toolCalls: asArray(a.toolCalls).map((item) => {
      const c = asObj(item)
      return { name: asString(c.name), argsDigest: asString(c.argsDigest) }
    }),
  }
}

function normalizeDetection(raw: unknown): Detection {
  const d = asObj(raw)
  return {
    detectorId: asString(d.detectorId),
    ruleId: asString(d.ruleId),
    confidence: asNumber(d.confidence),
    message: asString(d.message),
    tier: (asString(d.tier) || 'TIER_UNSPECIFIED') as Tier,
    attackClass: (asString(d.attackClass) || undefined) as AttackClass | undefined,
    anomalyScore: d.anomalyScore !== undefined ? asNumber(d.anomalyScore) : undefined,
    key: d.key !== undefined ? normalizeDetectionKey(d.key) : undefined,
	rawTags: asArray(d.rawTags).map(asString).filter((tag) => tag !== ''),
	taxonomyVersion: asString(d.taxonomyVersion),
	matchedVariable: asString(d.matchedVariable),
	evidenceSpan: asString(d.evidenceSpan),
	inspectionCoverageRef: asString(d.inspectionCoverageRef),
  }
}

function normalizeDetectionKey(raw: unknown): DetectionKey {
  const k = asObj(raw)
  return {
    detectorId: asString(k.detectorId) || undefined,
    detectorVersion: asString(k.detectorVersion) || undefined,
    detectorManifestDigest: asString(k.detectorManifestDigest) || undefined,
    ruleId: asString(k.ruleId),
    phase: asString(k.phase) || undefined,
    targetLocation: asString(k.targetLocation) || undefined,
    targetSelector: asString(k.targetSelector) || undefined,
    normalizationProfileDigest: asString(k.normalizationProfileDigest) || undefined,
  }
}

function normalizeTrace(raw: unknown): ReleaseTrace {
  const t = asObj(raw)
  return {
    releaseId: asString(t.releaseId),
    artifactId: asString(t.artifactId),
    mode: (asString(t.mode) || 'RELEASE_MODE_UNSPECIFIED') as ReleaseMode,
    canaryPercent: asNumber(t.canaryPercent),
    canarySelected: asBool(t.canarySelected),
    matched: asBool(t.matched),
  }
}

// normalizeAssetDetail 把 ListAssets/GetAsset 的 protojson 补成 AssetDetail 约定形状。
export function normalizeAssetDetail(raw: unknown): AssetDetail {
  const src = asObj(raw)
  return {
    asset: normalizeAsset(src.asset),
    unitIds: asArray(src.unitIds).map(asString).filter((id) => id !== ''),
    units: asArray(src.units).map(normalizeUnitProjection),
    edgeEnrollments: asArray(src.edgeEnrollments).map(normalizeEdgeEnrollment),
    health: asString(src.health),
    activeReleaseCount: asNumber(src.activeReleaseCount),
  }
}

/** normalizeEdgeEnrollment 补齐人工接入的配置坐标与逐项运行状态。 */
export function normalizeEdgeEnrollment(raw: unknown): EdgeEnrollment {
  const enrollment = asObj(raw)
  return {
    assetId: asString(enrollment.assetId),
    unitId: asString(enrollment.unitId),
    posture: (asString(enrollment.posture) || 'INGRESS_POSTURE_UNSPECIFIED') as IngressPosture,
    listenAddress: asString(enrollment.listenAddress),
    upstreamUrl: asString(enrollment.upstreamUrl),
    trafficKey: asString(enrollment.trafficKey),
    trustedProxyCidrs: asArray(enrollment.trustedProxyCidrs).map(asString),
    modelProfile: normalizeModelProfile(enrollment.modelProfile),
    modelIngressWindow: normalizeModelIngressWindow(enrollment.modelIngressWindow),
    modelsideId: asString(enrollment.modelsideId),
    specificationDigest: asString(enrollment.specificationDigest),
    expectedListenPlanVersion: asString(enrollment.expectedListenPlanVersion) || '0',
    expectedGenerationId: asString(enrollment.expectedGenerationId),
    expectedGenerationSeq: asString(enrollment.expectedGenerationSeq) || '0',
    status: (asString(enrollment.status) || 'EDGE_ENROLLMENT_STATUS_UNSPECIFIED') as EdgeEnrollmentStatus,
    lastHeartbeatAt: optionalString(enrollment.lastHeartbeatAt),
    currentListenPlanVersion: asString(enrollment.currentListenPlanVersion) || '0',
    currentGenerationId: asString(enrollment.currentGenerationId),
    currentGenerationSeq: asString(enrollment.currentGenerationSeq) || '0',
    modelsideStatus: (asString(enrollment.modelsideStatus) || 'EDGE_ENROLLMENT_STATUS_UNSPECIFIED') as EdgeEnrollmentStatus,
    modelsideLastResultAt: optionalString(enrollment.modelsideLastResultAt),
    modelProfileDigest: asString(enrollment.modelProfileDigest),
  }
}

function normalizeModelProfile(raw: unknown): ModelProfile {
  const profile = asObj(raw)
  return {
    profileId: asString(profile.profileId),
    modelGroup: asString(profile.modelGroup),
    modelType: asString(profile.modelType),
    modelVersion: asString(profile.modelVersion),
    alertThreshold: asNumber(profile.alertThreshold),
    reviewFloor: asNumber(profile.reviewFloor),
    reviewWindowSeconds: asNumber(profile.reviewWindowSeconds),
    maxReviewPerUnit: asNumber(profile.maxReviewPerUnit),
    maxReviewPerRoute: asNumber(profile.maxReviewPerRoute),
    dedupeRule: 'MODEL_DEDUPE_RULE_METHOD_ROUTE_HIGHEST_SCORE',
    allowedHeaders: asArray(profile.allowedHeaders).map(asString),
    maxBodyBytes: asNumber(profile.maxBodyBytes),
    reviewNewRoutes: asBool(profile.reviewNewRoutes),
    reviewInsufficientCoverage: asBool(profile.reviewInsufficientCoverage),
  }
}

function normalizeUnitProjection(raw: unknown): UnitProjection {
  const unit = asObj(raw)
  const capabilities = asObj(unit.capabilities)
  const health = asObj(unit.producerHealth)
  return {
    unitId: asString(unit.unitId),
    kind: asString(unit.kind),
    version: asString(unit.version),
    health: asString(unit.health),
    capabilities: {
      outputs: asArray(capabilities.outputs).map(asString) as UnitProjection['capabilities']['outputs'],
      projectionVersions: asArray(capabilities.projectionVersions).map(asString),
      postures: asArray(capabilities.postures).map(asString) as UnitProjection['capabilities']['postures'],
      sensors: asArray(capabilities.sensors).map(asString) as UnitProjection['capabilities']['sensors'],
      localEvidenceRing: asBool(capabilities.localEvidenceRing),
      localAsyncBypass: asBool(capabilities.localAsyncBypass),
      maxEventBatch: asNumber(capabilities.maxEventBatch),
      maxInFlightRequests: asNumber(capabilities.maxInFlightRequests),
      maxSpoolBytes: asString(capabilities.maxSpoolBytes) || String(asNumber(capabilities.maxSpoolBytes)),
      maxEvidenceEntries: asNumber(capabilities.maxEvidenceEntries),
      modelIngressHardLimit: capabilities.modelIngressHardLimit === undefined ? undefined : normalizeModelIngressWindow(capabilities.modelIngressHardLimit),
      maxModelIngressBatchItems: asNumber(capabilities.maxModelIngressBatchItems),
    },
    producerHealth: {
      bufferedCriticalEvents: asString(health.bufferedCriticalEvents) || String(asNumber(health.bufferedCriticalEvents)),
      bufferedOrdinarySamples: asString(health.bufferedOrdinarySamples) || String(asNumber(health.bufferedOrdinarySamples)),
      droppedCriticalEvents: asString(health.droppedCriticalEvents) || String(asNumber(health.droppedCriticalEvents)),
      droppedOrdinarySamples: asString(health.droppedOrdinarySamples) || String(asNumber(health.droppedOrdinarySamples)),
      droppedLocalBypassItems: asString(health.droppedLocalBypassItems) || String(asNumber(health.droppedLocalBypassItems)),
      projectionFailures: asString(health.projectionFailures) || String(asNumber(health.projectionFailures)),
      healthyProjectionVersions: asArray(health.healthyProjectionVersions).map(asString),
      effectiveModelIngressWindow: health.effectiveModelIngressWindow === undefined ? undefined : normalizeModelIngressWindow(health.effectiveModelIngressWindow),
      modelIngressWindowState: (asString(health.modelIngressWindowState) || 'MODEL_INGRESS_WINDOW_STATE_UNSPECIFIED') as UnitProjection['producerHealth']['modelIngressWindowState'],
      modelIngressDegradationReasons: asArray(health.modelIngressDegradationReasons).map(asString) as UnitProjection['producerHealth']['modelIngressDegradationReasons'],
      modelIngressQueuedItems: asString(health.modelIngressQueuedItems) || String(asNumber(health.modelIngressQueuedItems)),
      modelIngressQueuedBytes: asString(health.modelIngressQueuedBytes) || String(asNumber(health.modelIngressQueuedBytes)),
      modelIngressInFlightItems: asString(health.modelIngressInFlightItems) || String(asNumber(health.modelIngressInFlightItems)),
      modelIngressInFlightBytes: asString(health.modelIngressInFlightBytes) || String(asNumber(health.modelIngressInFlightBytes)),
      modelIngressOldestAgeMillis: asString(health.modelIngressOldestAgeMillis) || String(asNumber(health.modelIngressOldestAgeMillis)),
      modelIngressDrops: normalizeModelIngressDrops(health.modelIngressDrops),
    },
    posture: (asString(unit.posture) || 'INGRESS_POSTURE_UNSPECIFIED') as UnitProjection['posture'],
    trafficKey: asString(unit.trafficKey),
    lastHeartbeatAt: typeof unit.lastHeartbeatAt === 'string' ? unit.lastHeartbeatAt : undefined,
    currentGenerationId: optionalString(unit.currentGenerationId),
    currentGenerationSeq: asString(unit.currentGenerationSeq) || '0',
    currentListenPlanVersion: asString(unit.currentListenPlanVersion) || '0',
  }
}

function normalizeModelIngressWindow(raw: unknown): ModelIngressWindow {
  const src = asObj(raw)
  return {
    maxItems: asNumber(src.maxItems),
    maxRetainedBytes: asString(src.maxRetainedBytes) || String(asNumber(src.maxRetainedBytes)),
    maxQueueAge: asString(src.maxQueueAge),
  }
}

function normalizeModelIngressDrops(raw: unknown): UnitProjection['producerHealth']['modelIngressDrops'] {
  const src = asObj(raw)
  const counter = (value: unknown) => asString(value) || String(asNumber(value))
  return {
    evictedOldest: counter(src.evictedOldest),
    expired: counter(src.expired),
    itemTooLarge: counter(src.itemTooLarge),
    inFlightCapacity: counter(src.inFlightCapacity),
    transportFailed: counter(src.transportFailed),
    modelsideRejected: counter(src.modelsideRejected),
    admissionBudget: counter(src.admissionBudget),
  }
}

function normalizeAsset(raw: unknown): Asset {
  const src = asObj(raw)
  return {
    id: asString(src.id),
    displayName: asString(src.displayName),
    accessMode: (asString(src.accessMode) || 'ACCESS_MODE_UNSPECIFIED') as AccessMode,
    transports: asArray(src.transports).map(normalizeTransport),
    capabilities: src.capabilities !== undefined ? normalizeCaps(src.capabilities) : undefined,
    criticality: (asString(src.criticality) || 'CRITICALITY_UNSPECIFIED') as Criticality,
    maxAutoTier: (asString(src.maxAutoTier) || 'TIER_UNSPECIFIED') as Tier,
    labels: asStringRecord(src.labels),
    lastProbeAt: typeof src.lastProbeAt === 'string' ? src.lastProbeAt : undefined,
    updatedAt: typeof src.updatedAt === 'string' ? src.updatedAt : undefined,
  }
}

function normalizeTransport(raw: unknown): Transport {
  const t = asObj(raw)
  return {
    kind: (asString(t.kind) || 'KIND_UNSPECIFIED') as Transport['kind'],
    endpoint: asString(t.endpoint),
  }
}

function normalizeCaps(raw: unknown): CapabilityMatrix {
  const c = asObj(raw)
  return {
    kernelVersion: asString(c.kernelVersion),
    bpfLsm: asBool(c.bpfLsm),
    seccomp: asBool(c.seccomp),
    nftables: asBool(c.nftables),
    landlock: asBool(c.landlock),
    packageManagers: asArray(c.packageManagers).map(asString),
  }
}

// normalizeRelease 补齐省略的 retireReason 与制品 repeated 字段。
export function normalizeRelease(raw: unknown): Release {
  const src = asObj(raw)
  return {
    releaseId: asString(src.releaseId),
    state: (asString(src.state) || 'RELEASE_STATE_UNSPECIFIED') as ReleaseState,
    artifact: src.artifact !== undefined ? normalizeArtifact(src.artifact) : undefined,
    proposedAt: typeof src.proposedAt === 'string' ? src.proposedAt : undefined,
    signedAt: typeof src.signedAt === 'string' ? src.signedAt : undefined,
    shadowStartedAt: typeof src.shadowStartedAt === 'string' ? src.shadowStartedAt : undefined,
    canaryStartedAt: typeof src.canaryStartedAt === 'string' ? src.canaryStartedAt : undefined,
    enforcedAt: typeof src.enforcedAt === 'string' ? src.enforcedAt : undefined,
    retiredAt: typeof src.retiredAt === 'string' ? src.retiredAt : undefined,
    retireReason: (asString(src.retireReason) || 'RETIRE_REASON_UNSPECIFIED') as RetireReason,
    createdBy: asString(src.createdBy),
  }
}

function normalizeReleaseWindowStats(raw: unknown): ReleaseWindowStats {
  const src = asObj(raw)
  return {
    duration: asString(src.duration),
    requests: asString(src.requests) || String(asNumber(src.requests)),
    blocks: asString(src.blocks) || String(asNumber(src.blocks)),
    observes: asString(src.observes) || String(asNumber(src.observes)),
    canarySelected: asString(src.canarySelected) || String(asNumber(src.canarySelected)),
    denyFeedbackTotal: asString(src.denyFeedbackTotal) || String(asNumber(src.denyFeedbackTotal)),
    upstream5xx: asString(src.upstream5xx) || String(asNumber(src.upstream5xx)),
    p99Micros: asString(src.p99Micros) || String(asNumber(src.p99Micros)),
  }
}

/** normalizeReleaseStats 补齐统计窗口零值和守护窗口省略的原因数组。 */
export function normalizeReleaseStats(raw: unknown): ReleaseStats {
  const src = asObj(raw)
  const guard = asObj(src.guard)
  return {
    releaseId: asString(src.releaseId),
    state: (asString(src.state) || 'RELEASE_STATE_UNSPECIFIED') as ReleaseState,
    shadow: src.shadow !== undefined ? normalizeReleaseWindowStats(src.shadow) : undefined,
    canary: src.canary !== undefined ? normalizeReleaseWindowStats(src.canary) : undefined,
    enforce: src.enforce !== undefined ? normalizeReleaseWindowStats(src.enforce) : undefined,
    guard: src.guard !== undefined
      ? {
          consecutiveBadWindows: asNumber(guard.consecutiveBadWindows),
          lastBadWindowAt: optionalString(guard.lastBadWindowAt),
          lastBadReasons: asArray(guard.lastBadReasons).map(asString),
        }
      : undefined,
    computedAt: optionalString(src.computedAt),
  }
}

function normalizeArtifact(raw: unknown): Artifact {
  const a = asObj(raw)
  return {
    id: asString(a.id),
    kind: (asString(a.kind) || 'KIND_UNSPECIFIED') as ArtifactKind,
    payloadSchema: asString(a.payloadSchema),
    scope: a.scope !== undefined ? normalizeScope(a.scope) : undefined,
    ttl: asString(a.ttl),
    supersedes: asString(a.supersedes),
    evidenceRefs: asArray(a.evidenceRefs).map(asString),
    replayReport: isObj(a.replayReport) ? (a.replayReport as unknown as Artifact['replayReport']) : undefined,
    createdAt: typeof a.createdAt === 'string' ? a.createdAt : undefined,
    createdBy: asString(a.createdBy),
  }
}

function normalizeScope(raw: unknown): Scope {
  const s = asObj(raw)
  return {
    assetIds: asArray(s.assetIds).map(asString),
    routeSelector: asString(s.routeSelector),
  }
}

// normalizeDashboard 把省略的 int64 字符串与 map 补成 '0' / {}。
export function normalizeDashboard(raw: unknown): DashboardSummary {
  const src = asObj(raw)
  return {
    assetsTotal: asString(src.assetsTotal) || '0',
    degradedUnits: asString(src.degradedUnits) || '0',
    releasesByState: asStringRecord(src.releasesByState),
    events24hTotal: asString(src.events24hTotal) || '0',
    events24hBlocked: asString(src.events24hBlocked) || '0',
    pendingRetireSoon: asString(src.pendingRetireSoon) || '0',
    modelAlerts24h: asString(src.modelAlerts24h) || '0',
  }
}

/** normalizeTrafficReviewPolicyStatus 补齐策略上限和世代字段。 */
export function normalizeTrafficReviewPolicyStatus(raw: unknown): TrafficReviewPolicyStatus {
  const src = asObj(raw)
  const policy = asObj(src.policy)
  return {
    policy: {
      windowSeconds: asNumber(policy.windowSeconds),
      topRouteCells: asNumber(policy.topRouteCells),
      maxCandidatesPerWindow: asNumber(policy.maxCandidatesPerWindow),
      maxEvidenceBytes: asNumber(policy.maxEvidenceBytes),
      vaultMaxBytes: asString(policy.vaultMaxBytes) || '0',
      evidenceTtlSeconds: asString(policy.evidenceTtlSeconds) || '0',
      mode: (asString(policy.mode) || 'TRAFFIC_REVIEW_MODE_UNSPECIFIED') as TrafficReviewMode,
    },
    generationId: asString(src.generationId),
    generationSeq: asString(src.generationSeq) || '0',
    policyDigest: asString(src.policyDigest),
    edgeSupported: asBool(src.edgeSupported),
  }
}

/** normalizeModelIngressWindowStatus 补齐窗口、监听计划版本与降级原因。 */
export function normalizeModelIngressWindowStatus(raw: unknown): ModelIngressWindowStatus {
  const src = asObj(raw)
  return {
    assetId: asString(src.assetId),
    unitId: asString(src.unitId),
    desired: normalizeModelIngressWindow(src.desired),
    effective: src.effective === undefined ? undefined : normalizeModelIngressWindow(src.effective),
    desiredListenPlanVersion: asString(src.desiredListenPlanVersion) || String(asNumber(src.desiredListenPlanVersion)),
    appliedListenPlanVersion: asString(src.appliedListenPlanVersion) || String(asNumber(src.appliedListenPlanVersion)),
    state: (asString(src.state) || 'MODEL_INGRESS_WINDOW_STATE_UNSPECIFIED') as ModelIngressWindowStatus['state'],
    degradationReasons: asArray(src.degradationReasons).map(asString) as ModelIngressWindowStatus['degradationReasons'],
  }
}

function normalizeShapeSource(raw: unknown): ShapeSource {
  const src = asObj(raw)
  return {
    methods: asArray(src.methods).map(asString),
    routeTemplate: optionalString(src.routeTemplate),
    pathPrefix: optionalString(src.pathPrefix),
    constraints: asArray(src.constraints).map((rawConstraint) => {
      const constraint = asObj(rawConstraint)
      return {
        selector: asString(constraint.selector),
        minLen: asNumber(constraint.minLen),
        maxLen: asNumber(constraint.maxLen),
        charset: asString(constraint.charset) as NonNullable<ShapeSource['constraints']>[number]['charset'],
      }
    }),
  }
}

function normalizeReviewCandidate(raw: unknown): ReviewCandidate {
  const src = asObj(raw)
  return {
    candidateId: asString(src.candidateId),
    windowId: asString(src.windowId),
    unitId: asString(src.unitId),
    assetId: asString(src.assetId),
    occurredAt: optionalString(src.occurredAt),
    method: asString(src.method),
    routeTemplate: asString(src.routeTemplate),
    riskScore: asNumber(src.riskScore),
    riskReasons: asArray(src.riskReasons).map(asString),
    evidenceHandle: asString(src.evidenceHandle),
    evidenceDigest: asString(src.evidenceDigest),
    evidenceExpiresAt: optionalString(src.evidenceExpiresAt),
    baseline: asBool(src.baseline),
    reviewMode: optionalString(src.reviewMode),
  }
}

function normalizeTrafficFinding(raw: unknown): TrafficFinding {
  const src = asObj(raw)
  return {
    disposition: (asString(src.disposition) || 'TRAFFIC_FINDING_DISPOSITION_UNSPECIFIED') as TrafficFindingDisposition,
    confidence: asNumber(src.confidence),
    evidenceRefs: asArray(src.evidenceRefs).map(asString),
    attackClass: asString(src.attackClass),
    routeTemplate: asString(src.routeTemplate),
    selectors: asArray(src.selectors).map(asString),
    rationale: asString(src.rationale),
    optionalShapeDraft: src.optionalShapeDraft !== undefined ? normalizeShapeSource(src.optionalShapeDraft) : undefined,
  }
}

/** normalizeInvestigationCase 补齐案件卡片和工作区直接读取的 repeated 与标量字段。 */
export function normalizeInvestigationCase(raw: unknown): InvestigationCase {
  const src = asObj(raw)
  return {
    caseId: asString(src.caseId),
    moduleId: asString(src.moduleId),
    assetId: asString(src.assetId),
    clusterId: asString(src.clusterId),
    state: (asString(src.state) || 'INVESTIGATION_CASE_STATE_UNSPECIFIED') as InvestigationCaseState,
    priority: asNumber(src.priority),
    title: asString(src.title),
    summary: asString(src.summary),
    representatives: asArray(src.representatives).map(normalizeReviewCandidate),
    finding: src.finding !== undefined ? normalizeTrafficFinding(src.finding) : undefined,
    shadowReleaseId: asString(src.shadowReleaseId),
    assignedAgentId: asString(src.assignedAgentId),
    assignedAgentDisplayName: asString(src.assignedAgentDisplayName),
    resolution: (asString(src.resolution) || undefined) as CaseResolution | undefined,
    automationSuppressedReason: optionalString(src.automationSuppressedReason),
    assignedRunId: optionalString(src.assignedRunId),
    assignedAgentConfigDigest: optionalString(src.assignedAgentConfigDigest),
    resolvedAt: optionalString(src.resolvedAt),
    createdAt: optionalString(src.createdAt),
    updatedAt: optionalString(src.updatedAt),
  }
}

/** normalizeCaseActivity 把 protojson int64 游标稳定为字符串。 */
export function normalizeCaseActivity(raw: unknown): CaseActivity {
  const src = asObj(raw)
  return {
    sequence: asString(src.sequence) || String(asNumber(src.sequence)),
    caseId: asString(src.caseId),
    kind: (asString(src.kind) || 'CASE_ACTIVITY_KIND_UNSPECIFIED') as CaseActivityKind,
    refId: asString(src.refId),
    summary: asString(src.summary),
    occurredAt: optionalString(src.occurredAt),
  }
}

/** normalizeApprovalView 补齐审批卡片直接 join 的允许字段。 */
export function normalizeApprovalView(raw: unknown): ApprovalView {
  const src = asObj(raw)
  return {
    approvalId: asString(src.approvalId),
    kind: (asString(src.kind) || 'APPROVAL_KIND_UNSPECIFIED') as ApprovalView['kind'],
    state: asString(src.state),
    caseId: asString(src.caseId),
    assetId: asString(src.assetId),
    workerId: asString(src.workerId),
    modelHost: asString(src.modelHost),
    modelName: asString(src.modelName),
    modelConfigDigest: asString(src.modelConfigDigest),
    allowedFields: asArray(src.allowedFields).map(asString),
    maxBytes: asString(src.maxBytes) || String(asNumber(src.maxBytes)),
    previousCapacity: asNumber(src.previousCapacity),
    requestedCapacity: asNumber(src.requestedCapacity),
    expiresAt: optionalString(src.expiresAt),
    createdAt: optionalString(src.createdAt),
  }
}

/** normalizeDefenseModule 补齐模块目录的能力和表面数组。 */
export function normalizeDefenseModule(raw: unknown): DefenseModule {
  const src = asObj(raw)
  return {
    moduleId: asString(src.moduleId),
    displayName: asString(src.displayName),
    version: asString(src.version),
    requiredProducerCapabilities: asArray(src.requiredProducerCapabilities).map(asString),
    caseActivitySchemas: asArray(src.caseActivitySchemas).map(asString),
    surfaces: asArray(src.surfaces).map(asString),
    active: asBool(src.active),
  }
}

/** normalizeManagedAgentProfile 保留服务端 canManage 决策并补齐档案 repeated 字段。 */
export function normalizeManagedAgentProfile(raw: unknown): ManagedAgentProfile {
  const src = asObj(raw)
  return {
    agentId: asString(src.agentId),
    displayName: asString(src.displayName),
    kind: (asString(src.kind) || 'AGENT_PROFILE_KIND_UNSPECIFIED') as AgentProfileKind,
    state: (asString(src.state) || 'AGENT_PROFILE_STATE_UNSPECIFIED') as AgentProfileState,
    tools: asArray(src.tools).map(asString),
    bindings: asArray(src.bindings).map(normalizeBinding),
    executionMode: (asString(src.executionMode) || undefined) as AgentExecutionMode | undefined,
    configDigest: optionalString(src.configDigest),
    activeRunCount: asNumber(src.activeRunCount),
    lastRunAt: optionalString(src.lastRunAt),
    tombstonedAt: optionalString(src.tombstonedAt),
    lastWorkerId: optionalString(src.lastWorkerId),
    lastWorkerPlatform: optionalString(src.lastWorkerPlatform),
    canManage: asBool(src.canManage),
    createdBy: asString(src.createdBy),
    createdAt: optionalString(src.createdAt),
    updatedAt: optionalString(src.updatedAt),
  }
}

/** normalizeWorkerRecord 补齐 Worker 能力数组，避免空 repeated 令卡片崩溃。 */
export function normalizeWorkerRecord(raw: unknown): WorkerRecord {
  const src = asObj(raw)
  return {
    workerId: asString(src.workerId),
    workerKind: asString(src.workerKind),
    version: asString(src.version),
    operatingSystem: asString(src.operatingSystem),
    architecture: asString(src.architecture),
    sandboxCapabilities: asArray(src.sandboxCapabilities).map(asString),
    maxConcurrency: asNumber(src.maxConcurrency),
    lastSeenAt: optionalString(src.lastSeenAt),
    investigationEligible: asBool(src.investigationEligible),
    missingSandboxCapabilities: asArray(src.missingSandboxCapabilities).map(asString),
  }
}

/** normalizeWorkerEnrollmentRecord 补齐注册卡片直接读取的 repeated 与容量字段。 */
export function normalizeWorkerEnrollmentRecord(raw: unknown): WorkerEnrollmentRecord {
  const src = asObj(raw)
  return {
    enrollmentId: asString(src.enrollmentId),
    workerId: asString(src.workerId),
    workerKind: asString(src.workerKind),
    publicKeyFingerprint: asString(src.publicKeyFingerprint),
    hostname: asString(src.hostname),
    operatingSystem: asString(src.operatingSystem),
    architecture: asString(src.architecture),
    sandboxCapabilities: asArray(src.sandboxCapabilities).map(asString),
    state: asString(src.state),
    bindings: asArray(src.bindings).map(asString),
    maxConcurrency: asNumber(src.maxConcurrency),
    requestedAt: optionalString(src.requestedAt),
    decidedAt: optionalString(src.decidedAt),
    activationPublicKeyFingerprint: optionalString(src.activationPublicKeyFingerprint),
    approvedManifestDigest: optionalString(src.approvedManifestDigest),
    version: optionalString(src.version),
    memoryCapacityBytes: asString(src.memoryCapacityBytes) || String(asNumber(src.memoryCapacityBytes)),
    logicalCpuCapacity: asNumber(src.logicalCpuCapacity),
    sandboxChallengeId: optionalString(src.sandboxChallengeId),
  }
}

/** normalizeWorkerEnrollmentDecision 只保留控制台允许展示的激活引用与摘要。 */
export function normalizeWorkerEnrollmentDecision(raw: unknown): WorkerEnrollmentDecision {
  const src = asObj(raw)
  return {
    enrollmentId: asString(src.enrollmentId),
    state: asString(src.state),
    activationBundleRef: asString(src.activationBundleRef),
    approvedManifestDigest: asString(src.approvedManifestDigest),
  }
}
