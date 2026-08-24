// 交付构建走 connect，且用相对路径调 API（docs/api.md §17.1）。

import { describe, expect, it, vi } from 'vitest'
import { ConnectClient } from './connect'

describe('Connect 相对路径', () => {
  it('ConnectClient 默认 baseUrl 为空，请求打到同源 /yufeng.*', async () => {
    const fetchFn = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString()
      expect(url.startsWith('http://')).toBe(false)
      expect(url.startsWith('https://')).toBe(false)
      expect(url).toBe('/yufeng.auth.v1.AuthService/Login')
      return new Response(JSON.stringify({ token: 't', user: { userId: 'u', username: 'a' } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    })
    const client = new ConnectClient({
      getToken: () => null,
      onUnauthenticated: () => undefined,
      fetchFn: fetchFn as unknown as typeof fetch,
    })
    await client.login({ username: 'a', password: 'b' })
    expect(fetchFn).toHaveBeenCalled()
  })
})