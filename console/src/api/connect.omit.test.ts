// Connect JSON 省略空 repeated 时，客户端必须补齐，避免事件/资产列表整页抛错。

import { describe, expect, it, vi } from 'vitest'
import { ConnectClient } from './connect'

function jsonRes(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function clientWith(body: unknown): ConnectClient {
  const fetchFn = vi.fn(async () => jsonRes(body))
  return new ConnectClient({
    getToken: () => 'tok',
    onUnauthenticated: () => undefined,
    fetchFn: fetchFn as unknown as typeof fetch,
  })
}

describe('ConnectClient protojson 省略字段', () => {
  it('认证响应补齐空 access，AuthProvider 可安全读取工具和 Bindings', async () => {
    const session = await clientWith({ token: 'tok', user: { userId: 'user-1' }, access: {} }).login({ username: 'u', password: 'p' })
    expect(session.access).toEqual({ tools: [], bindings: [] })
    expect(session.user).toMatchObject({ role: 'USER_ROLE_UNSPECIFIED', state: 'USER_STATE_UNSPECIFIED' })

    const access = await clientWith({ user: { userId: 'user-1' }, access: {} }).getMyAccess()
    expect(access).toEqual({ tools: [], bindings: [] })
  })

  it('listEvents 补齐省略的 detections，页面可安全读 [0]', async () => {
    const page = await clientWith({
      events: [
        {
          id: 'e-omit',
          occurredAt: '2026-08-18T00:00:00Z',
          assetId: 'local-1',
          source: 'yufeng-edge',
          kind: 'KIND_TRAFFIC',
          verdict: 'VERDICT_ALLOW',
          http: { method: 'GET', path: '/api/items' },
        },
      ],
    }).listEvents()
    expect(page.items).toHaveLength(1)
    expect(page.items[0].detections).toEqual([])
    expect(page.items[0].detections[0]?.ruleId).toBeUndefined()
    expect(page.items[0].labels).toEqual({})
    expect(page.items[0].releaseTraces).toEqual([])
  })

  it('listAssets 补齐省略的 unitIds，页面可安全读 length', async () => {
    const page = await clientWith({
      assets: [
        {
          asset: {
            id: 'local-1',
            displayName: 'local-1',
            accessMode: 'ACCESS_MODE_NETWORK',
            capabilities: {},
            criticality: 'CRITICALITY_P2',
            maxAutoTier: 'TIER_L1_TRAFFIC',
          },
          activeReleaseCount: 4,
        },
      ],
    }).listAssets()
    expect(page.items[0].unitIds).toEqual([])
    expect(page.items[0].unitIds.length).toBe(0)
    expect(page.items[0].asset.labels).toEqual({})
    expect(page.items[0].asset.capabilities?.packageManagers).toEqual([])
  })

  it('getModelGateway 补齐省略的 providers 与计数', async () => {
    const gw = await clientWith({
      baseUrl: 'https://api.x.ai/v1',
      model: 'grok-4-1-fast-non-reasoning',
      hasSecret: true,
      status: 'MODEL_GATEWAY_STATUS_READY',
    }).getModelGateway()
    expect(gw.providers).toEqual([])
    expect(gw.callsTotal).toBe('0')
    expect(gw.callsOk).toBe('0')
    expect(gw.windowSeconds).toBe('0')
    expect(gw.lastError).toBe('')
    expect(gw.secretHint).toBe('')
  })

  it('getEvent 补齐省略的 detections 与 releaseTraces', async () => {
    const event = await clientWith({
      event: { id: 'e2', kind: 'KIND_TRAFFIC', verdict: 'VERDICT_BLOCK', assetId: 'local-1' },
    }).getEvent('e2')
    expect(event.event.detections).toEqual([])
    expect(event.event.releaseTraces).toEqual([])
    expect(event.event.labels).toEqual({})
    expect(event.modelInferences).toEqual([])
    expect(event.triageDeliveries).toEqual([])
  })

  it('updateAsset 省略空 expectedUpdatedAt，并以 lowerCamel 编码 FieldMask', async () => {
    const fetchFn = vi.fn(async (_url: string, init?: RequestInit) => {
      const body = JSON.parse(String(init?.body ?? '{}')) as { expectedUpdatedAt?: string; updateMask?: string }
      expect(body.expectedUpdatedAt).toBeUndefined()
      expect(body.updateMask).toBe('displayName,maxAutoTier,accessMode')
      return jsonRes({
        asset: {
          id: 'a1',
          displayName: '本机',
          accessMode: 'ACCESS_MODE_NETWORK',
          criticality: 'CRITICALITY_P2',
          maxAutoTier: 'TIER_L1_TRAFFIC',
        },
      })
    })
    const client = new ConnectClient({
      getToken: () => 'tok',
      onUnauthenticated: () => undefined,
      fetchFn: fetchFn as unknown as typeof fetch,
    })
    const detail = await client.updateAsset('a1', {
      displayName: '本机',
      maxAutoTier: 'TIER_L1_TRAFFIC',
      accessMode: 'ACCESS_MODE_NETWORK',
    }, '')
    expect(detail.asset.id).toBe('a1')
    expect(detail.asset.displayName).toBe('本机')
    expect(detail.unitIds).toEqual([])
  })

  it('putModelConfig 发送方言', async () => {
    const fetchFn = vi.fn(async (_url: string, init?: RequestInit) => {
      const body = JSON.parse(String(init?.body ?? '{}')) as { dialect?: string }
      expect(body.dialect).toBe('MODEL_DIALECT_CLAUDE_MESSAGES')
      return jsonRes({})
    })
    const client = new ConnectClient({
      getToken: () => 'tok',
      onUnauthenticated: () => undefined,
      fetchFn: fetchFn as unknown as typeof fetch,
    })
    await client.putModelConfig({
      baseUrl: 'https://api.x.ai/v1',
      secret: 'sk-x',
      dialect: 'MODEL_DIALECT_CLAUDE_MESSAGES',
    })
    expect(fetchFn).toHaveBeenCalled()
  })

  it('getModelGateway 补齐方言', async () => {
    const gw = await clientWith({
      baseUrl: 'https://api.x.ai/v1',
      model: 'grok',
      hasSecret: true,
      status: 'MODEL_GATEWAY_STATUS_READY',
    }).getModelGateway()
    expect(gw.dialect).toBe('MODEL_DIALECT_OPENAI_CHAT')
  })

  it('案件、审批与模块补齐 protojson 省略字段', async () => {
    const cases = await clientWith({ cases: [{ caseId: 'case-1', assetId: 'asset-1', finding: {} }] }).listCases()
    expect(cases.items[0]).toMatchObject({
      assignedAgentDisplayName: '',
      representatives: [],
      state: 'INVESTIGATION_CASE_STATE_UNSPECIFIED',
      finding: { evidenceRefs: [], selectors: [] },
    })

    const approval = await clientWith({ approval: { approvalId: 'approval-1' } }).getApproval('approval-1')
    expect(approval.allowedFields).toEqual([])
    expect(approval.maxBytes).toBe('0')

    const modules = await clientWith({ modules: [{ moduleId: 'traffic-interception' }] }).listModules()
    expect(modules[0]).toMatchObject({ requiredProducerCapabilities: [], caseActivitySchemas: [], surfaces: [], active: false })
  })

  it('Worker 与 Agent 稀疏响应可直接供卡片读取', async () => {
    const workers = await clientWith({ workers: [{ workerId: 'worker-1' }] }).listWorkers()
    expect(workers.items[0].sandboxCapabilities).toEqual([])
    expect(workers.items[0].missingSandboxCapabilities).toEqual([])

    const enrollments = await clientWith({ enrollments: [{ enrollmentId: 'enrollment-1' }] }).listWorkerEnrollments()
    expect(enrollments.items[0].sandboxCapabilities).toEqual([])
    expect(enrollments.items[0].bindings).toEqual([])
    expect(enrollments.items[0].memoryCapacityBytes).toBe('0')

    const profiles = await clientWith({ profiles: [{ agentId: 'profile-1', canManage: true }] }).listAgentProfiles()
    expect(profiles.items[0]).toMatchObject({ tools: [], bindings: [], canManage: true })
  })

  it('流量审查状态补齐策略上限和默认世代', async () => {
    const status = await clientWith({ status: { policy: { mode: 'TRAFFIC_REVIEW_MODE_OFF' } } }).getTrafficReviewPolicy('asset-1')

    expect(status).toMatchObject({
      generationSeq: '0',
      policyDigest: '',
      edgeSupported: false,
      policy: { vaultMaxBytes: '0', evidenceTtlSeconds: '0', maxEvidenceBytes: 0 },
    })
  })

  it('发布统计补齐空守护原因，详情页可安全读取 length', async () => {
    const stats = await clientWith({
      releaseId: 'release-1',
      state: 'RELEASE_STATE_SHADOW',
      shadow: { duration: '300s' },
      guard: {},
    }).getReleaseStats('release-1')

    expect(stats.shadow?.requests).toBe('0')
    expect(stats.guard?.lastBadReasons).toEqual([])
    expect(stats.guard?.lastBadReasons.length).toBe(0)
  })

  it('Worker 批准响应不保留历史敏感字段', async () => {
    const decision = await clientWith({
      enrollmentId: 'enrollment-1',
      state: 'approved',
      activationBundleRef: 'activation:encrypted',
      approvedManifestDigest: 'sha256:manifest',
      bootstrapToken: 'must-not-survive',
      clientCertificate: 'must-not-survive',
    }).decideWorkerEnrollment({ enrollmentId: 'enrollment-1', approved: true, bindings: ['asset-1'] })

    expect(decision).toEqual({
      enrollmentId: 'enrollment-1',
      state: 'approved',
      activationBundleRef: 'activation:encrypted',
      approvedManifestDigest: 'sha256:manifest',
    })
  })

  it('兼容授予列表补齐空 Tools 与 Bindings', async () => {
    const grants = await clientWith({ grants: [{ grantId: 'grant-1' }] }).listGrants()

    expect(grants[0]).toMatchObject({ tools: [], bindings: [], subjectUserId: '' })
  })
})
