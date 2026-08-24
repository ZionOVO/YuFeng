import { describe, expect, it, vi } from 'vitest'
import { ConnectClient } from './connect'

describe('Edge 人工部署规格契约', () => {
  it('按入口姿态发送完整签名输入而不调用部署动作', async () => {
    const bodies: unknown[] = []
    const fetchFn = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      bodies.push(JSON.parse(String(init?.body ?? '{}')))
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    })
    const client = new ConnectClient({
      getToken: () => 'tok',
      onUnauthenticated: () => undefined,
      fetchFn: fetchFn as unknown as typeof fetch,
    })

    const common = {
      unitId: 'edge-1',
      assetId: 'asset-1',
      modelProfile: {
        profileId: 'http/default',
        modelGroup: 'http',
        modelType: 'PVM',
        modelVersion: 'v1',
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
      },
    }
    await client.putDeploymentSpecification({
      ...common,
      posture: 'INGRESS_POSTURE_REVERSE_PROXY',
      trafficKey: 'site-a',
      trustedProxyCidrs: ['10.0.0.0/8'],
      reverseProxy: { listenAddress: ':18080', upstreamUrl: 'http://app:8080' },
    })
    await client.putDeploymentSpecification({
      ...common,
      posture: 'INGRESS_POSTURE_EXT_AUTHZ',
      trafficKey: 'gateway-a',
      trustedProxyCidrs: [],
      extAuthz: { listenAddress: ':18081' },
    })

    expect(bodies).toEqual([
      {
        ...common,
        posture: 'INGRESS_POSTURE_REVERSE_PROXY',
        trafficKey: 'site-a',
        trustedProxyCidrs: ['10.0.0.0/8'],
        reverseProxy: { listenAddress: ':18080', upstreamUrl: 'http://app:8080' },
      },
      {
        ...common,
        posture: 'INGRESS_POSTURE_EXT_AUTHZ',
        trafficKey: 'gateway-a',
        trustedProxyCidrs: [],
        extAuthz: { listenAddress: ':18081' },
      },
    ])
  })

  it('读取 Edge 主动回执坐标并只调用显式引导操作', async () => {
    const methods: string[] = []
    const fetchFn = vi.fn(async (input: RequestInfo | URL) => {
      const method = String(input).split('/').at(-1) ?? ''
      methods.push(method)
      if (method === 'GetOnboarding') {
        return new Response(
          JSON.stringify({
            state: 'ONBOARDING_STATE_EDGE_LIVE',
            edgeReady: true,
            localUnitId: 'edge-1',
            localAssetId: 'asset-1',
            deploymentSpecDigest: 'sha256:spec',
            expectedGenerationId: 'generation-1',
            expectedGenerationSeq: '7',
            expectedListenPlanVersion: '3',
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    })
    const client = new ConnectClient({
      getToken: () => 'tok',
      onUnauthenticated: () => undefined,
      fetchFn: fetchFn as unknown as typeof fetch,
    })

    await expect(client.getOnboarding()).resolves.toEqual(
      expect.objectContaining({
        state: 'ONBOARDING_STATE_EDGE_LIVE',
        edgeReady: true,
        localUnitId: 'edge-1',
        localAssetId: 'asset-1',
        deploymentSpecDigest: 'sha256:spec',
        expectedGenerationId: 'generation-1',
        expectedGenerationSeq: '7',
        expectedListenPlanVersion: '3',
      }),
    )
    await client.testModelConnectivity()
    await client.completeOnboarding()
    await client.probeModelGateway()

    expect(methods).toEqual(['GetOnboarding', 'TestModelConnectivity', 'CompleteOnboarding', 'ProbeModelGateway'])
  })
})
