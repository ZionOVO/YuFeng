// 同一次用户重试复用 Idempotency-Key，不得每次 randomUUID()。

import { describe, expect, it, vi } from 'vitest'
import { ConnectClient } from './connect'

function jsonRes(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

describe('Idempotency-Key 复用', () => {
  it('同一写请求失败后重试复用同一把键', async () => {
    const keys: string[] = []
    let calls = 0
    const fetchFn = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      calls += 1
      const h = new Headers(init?.headers)
      keys.push(h.get('Idempotency-Key') ?? '')
      if (calls === 1) {
        return jsonRes({ code: 'unavailable', message: 'temporary' }, 503)
      }
      return jsonRes({ grant: { grantId: 'g1', subjectUserId: 'u2', tools: ['console.read'], bindings: [], createdBy: 'a', createdAt: 't' } })
    })
    const client = new ConnectClient({
      getToken: () => 'tok',
      onUnauthenticated: () => undefined,
      fetchFn: fetchFn as unknown as typeof fetch,
    })
    const req = { subjectUserId: 'u2', tools: ['console.read'], bindings: [{ kind: 'asset' as const, id: 'asset-01' }] }
    await expect(client.putGrant(req)).rejects.toMatchObject({ code: 'unavailable' })
    await client.putGrant(req)
    expect(keys).toHaveLength(2)
    expect(keys[0]).not.toBe('')
    expect(keys[1]).toBe(keys[0])
  })

  it('请求体变化则换新键', async () => {
    const keys: string[] = []
    const fetchFn = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const h = new Headers(init?.headers)
      keys.push(h.get('Idempotency-Key') ?? '')
      return jsonRes({ grant: { grantId: 'g', subjectUserId: 'u', tools: [], bindings: [], createdBy: 'a', createdAt: 't' } })
    })
    const client = new ConnectClient({
      getToken: () => 'tok',
      onUnauthenticated: () => undefined,
      fetchFn: fetchFn as unknown as typeof fetch,
    })
    await client.putGrant({ subjectUserId: 'u2', tools: ['console.read'], bindings: [{ kind: 'asset', id: 'asset-01' }] })
    await client.putGrant({ subjectUserId: 'u3', tools: ['console.read'], bindings: [{ kind: 'asset', id: 'asset-01' }] })
    expect(keys[0]).not.toBe(keys[1])
  })
})
