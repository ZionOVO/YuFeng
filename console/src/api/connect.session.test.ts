// ConnectClient 会话方法必须直接调用真实 SessionService 路由。

import { afterEach, describe, expect, it, vi } from 'vitest'
import { ConnectClient } from './connect'

function jsonRes(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

describe('ConnectClient SessionService', () => {
	afterEach(() => {
	  vi.useRealTimers()
	})

  it('createSession / sendMessage / pollMessages / listMessages 都 POST 到 yufeng.session.v1.SessionService', async () => {
    const paths: string[] = []
    const fetchFn = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      paths.push(url)
      if (url.endsWith('/CreateSession')) {
        return jsonRes({ sessionId: 'ses_live' })
      }
      if (url.endsWith('/SendMessage')) {
        return jsonRes({ messageSequence: '1' })
      }
      if (url.endsWith('/PollMessages')) {
        return jsonRes({
          messages: [
            {
              sequence: '2',
              sessionId: 'ses_live',
              sender: 'jarvis-1',
              content: 'pong',
              occurredAt: '2026-08-16T00:00:01.000Z',
            },
          ],
          nextCursor: '2',
        })
      }
      if (url.endsWith('/ListMessages')) {
        return jsonRes({
          messages: [
            {
              sequence: '2',
              sessionId: 'ses_live',
              sender: 'jarvis-1',
              content: 'pong',
              occurredAt: '2026-08-16T00:00:01.000Z',
            },
          ],
          nextPageToken: '',
        })
      }
      return jsonRes({ code: 'unimplemented', message: 'unexpected' }, 501)
    })
    const client = new ConnectClient({
      getToken: () => 'tok',
      onUnauthenticated: () => undefined,
      fetchFn: fetchFn as unknown as typeof fetch,
    })

    await expect(client.createSession({ title: 'console' })).resolves.toEqual({ sessionId: 'ses_live' })
    await expect(client.sendMessage({ sessionId: 'ses_live', content: 'hello' })).resolves.toEqual({ messageSequence: '1' })
    const polled = await client.pollMessages({ sessionId: 'ses_live', cursor: '0', longPollSeconds: 30 })
    expect(polled.messages).toHaveLength(1)
    expect(polled.messages[0]?.sender).toBe('jarvis-1')
    expect(polled.messages[0]?.content).toBe('pong')
    expect(polled.messages[0]?.content.includes('YF/1')).toBe(false)
    const listed = await client.listMessages({ sessionId: 'ses_live' }, { pageSize: 50 })
    expect(listed.items[0]?.content).toBe('pong')

    expect(paths).toEqual([
      '/yufeng.session.v1.SessionService/CreateSession',
      '/yufeng.session.v1.SessionService/SendMessage',
      '/yufeng.session.v1.SessionService/PollMessages',
      '/yufeng.session.v1.SessionService/ListMessages',
    ])
  })

  it('pollMessages 保留 0 让服务端按契约使用默认长轮询窗口', async () => {
    const fetchFn = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const body = JSON.parse(String(init?.body ?? '{}')) as { longPollSeconds?: number }
      expect(body.longPollSeconds).toBe(0)
      return jsonRes({})
    })
    const client = new ConnectClient({
      getToken: () => 'tok',
      onUnauthenticated: () => undefined,
      fetchFn: fetchFn as unknown as typeof fetch,
    })

    await client.pollMessages({ sessionId: 'ses_live', longPollSeconds: 0 })
    expect(fetchFn).toHaveBeenCalledOnce()
  })

	it('pollMessages 在服务端长轮询窗口后主动中止悬挂请求', async () => {
	  vi.useFakeTimers()
	  const fetchFn = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
		return new Promise<Response>((_resolve, reject) => {
		  init?.signal?.addEventListener('abort', () => reject(init.signal?.reason ?? new DOMException('aborted', 'AbortError')))
		})
	  })
	  const client = new ConnectClient({
		getToken: () => 'tok',
		onUnauthenticated: () => undefined,
		fetchFn: fetchFn as unknown as typeof fetch,
	  })
	  const pending = client.pollMessages({ sessionId: 'ses_hung', longPollSeconds: 1 })
	  const rejected = expect(pending).rejects.toMatchObject({ code: 'unavailable' })
	  await vi.advanceTimersByTimeAsync(6000)
	  await rejected
	  expect(fetchFn.mock.calls[0]?.[1]?.signal?.aborted).toBe(true)
	})
})
