import { describe, expect, it, vi } from 'vitest'
import { ConnectClient } from './connect'

const modelProfile = {
  profileId: 'http-threat/PVM/gpvm-e9eceef3',
  modelGroup: 'http-threat',
  modelType: 'PVM',
  modelVersion: 'gpvm-e9eceef3',
  alertThreshold: 0.9,
  reviewFloor: 0.5,
  reviewWindowSeconds: 300,
  maxReviewPerUnit: 4,
  maxReviewPerRoute: 1,
  dedupeRule: 'MODEL_DEDUPE_RULE_METHOD_ROUTE_HIGHEST_SCORE' as const,
  allowedHeaders: ['content-type'],
  maxBodyBytes: 65536,
  reviewNewRoutes: true,
  reviewInsufficientCoverage: true,
}

const modelIngressWindow = {
  maxItems: 4096,
  maxRetainedBytes: String(128 * 1024 * 1024),
  maxQueueAge: '2s',
}

describe('人工 Edge 接入契约', () => {
  it('按资产发送反向代理与外部授权接入配置，不调用旧引导部署方法', async () => {
    const calls: { method: string; body: unknown }[] = []
    const fetchFn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = String(input).split('/').at(-1) ?? ''
      calls.push({ method, body: JSON.parse(String(init?.body ?? '{}')) })
      return new Response(JSON.stringify({ enrollment: { assetId: 'asset-1', unitId: 'edge-1' } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    })
    const client = new ConnectClient({
      getToken: () => 'tok',
      onUnauthenticated: () => undefined,
      fetchFn: fetchFn as unknown as typeof fetch,
    })

    await client.putEdgeEnrollment({
      assetId: 'asset-1',
      unitId: 'edge-1',
      posture: 'INGRESS_POSTURE_REVERSE_PROXY',
      listenAddress: ':18080',
      upstreamUrl: 'http://app:8080',
      trafficKey: 'site-a',
      trustedProxyCidrs: ['10.0.0.0/8'],
      modelProfile,
      modelIngressWindow,
    })
    await client.putEdgeEnrollment({
      assetId: 'asset-1',
      unitId: 'edge-2',
      posture: 'INGRESS_POSTURE_EXT_AUTHZ',
      listenAddress: ':18081',
      upstreamUrl: '',
      trafficKey: 'gateway-a',
      trustedProxyCidrs: [],
      modelProfile,
      modelIngressWindow,
    })

    expect(calls).toEqual([
      {
        method: 'PutEdgeEnrollment',
        body: {
          assetId: 'asset-1',
          unitId: 'edge-1',
          posture: 'INGRESS_POSTURE_REVERSE_PROXY',
          listenAddress: ':18080',
          upstreamUrl: 'http://app:8080',
          trafficKey: 'site-a',
          trustedProxyCidrs: ['10.0.0.0/8'],
          modelProfile,
          modelIngressWindow,
        },
      },
      {
        method: 'PutEdgeEnrollment',
        body: {
          assetId: 'asset-1',
          unitId: 'edge-2',
          posture: 'INGRESS_POSTURE_EXT_AUTHZ',
          listenAddress: ':18081',
          upstreamUrl: '',
          trafficKey: 'gateway-a',
          trustedProxyCidrs: [],
          modelProfile,
          modelIngressWindow,
        },
      },
    ])
    expect('putDeploymentSpecification' in client).toBe(false)
  })

  it('读取逐项接入状态，并让初次配置客户端只保留两项完成谓词', async () => {
    const methods: string[] = []
    const fetchFn = vi.fn(async (input: RequestInfo | URL) => {
      const method = String(input).split('/').at(-1) ?? ''
      methods.push(method)
      if (method === 'GetEdgeEnrollment') {
        return new Response(JSON.stringify({
          enrollment: {
            assetId: 'asset-1',
            unitId: 'edge-1',
            status: 'EDGE_ENROLLMENT_STATUS_ONLINE',
            modelsideStatus: 'EDGE_ENROLLMENT_STATUS_OUT_OF_SYNC',
            expectedListenPlanVersion: '3',
            currentListenPlanVersion: '3',
            expectedGenerationSeq: '7',
            currentGenerationSeq: '7',
          },
        }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      if (method === 'GetOnboarding') {
        return new Response(JSON.stringify({
          state: 'ONBOARDING_STATE_MODEL_LIVE',
          jarvisOnline: true,
          edgeReady: true,
          localAssetId: 'legacy-asset',
        }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    })
    const client = new ConnectClient({
      getToken: () => 'tok',
      onUnauthenticated: () => undefined,
      fetchFn: fetchFn as unknown as typeof fetch,
    })

    await expect(client.getEdgeEnrollment('asset-1', 'edge-1')).resolves.toMatchObject({
      assetId: 'asset-1',
      unitId: 'edge-1',
      status: 'EDGE_ENROLLMENT_STATUS_ONLINE',
      modelsideStatus: 'EDGE_ENROLLMENT_STATUS_OUT_OF_SYNC',
      expectedListenPlanVersion: '3',
      currentListenPlanVersion: '3',
    })
    await expect(client.getOnboarding()).resolves.toEqual(expect.objectContaining({
      state: 'ONBOARDING_STATE_MODEL_LIVE',
      jarvisOnline: true,
    }))
    await client.testModelConnectivity()
    await client.completeOnboarding()

    expect(methods).toEqual(['GetEdgeEnrollment', 'GetOnboarding', 'TestModelConnectivity', 'CompleteOnboarding'])
  })
})
