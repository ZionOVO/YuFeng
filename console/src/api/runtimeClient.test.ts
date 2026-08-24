import { describe, expect, it } from 'vitest'
import { createClient } from './index'
import { ConnectClient } from './connect'

describe('控制台运行时客户端', () => {
  it('始终创建真实 ConnectClient', () => {
    const client = createClient({
      getToken: () => 'session-token',
      onUnauthenticated: () => undefined,
    })
    expect(client).toBeInstanceOf(ConnectClient)
  })
})
