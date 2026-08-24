import { act, renderHook, waitFor } from '@testing-library/react'
import { useCallback } from 'react'
import type { Page } from '../api/client'
import { useCursorCollection } from './useCursorCollection'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((done, fail) => {
    resolve = done
    reject = fail
  })
  return { promise, resolve, reject }
}

describe('useCursorCollection', () => {
  it('空页仍可沿游标继续，末页停止加载', async () => {
    const pages = new Map<string, Page<string>>([
      ['', { items: [], nextPageToken: 'empty-next' }],
      ['empty-next', { items: [], nextPageToken: '' }],
    ])
    const loadPage = (token: string) => Promise.resolve(pages.get(token)!)
    const { result } = renderHook(() => useCursorCollection(loadPage))

    await waitFor(() => expect(result.current.nextPageToken).toBe('empty-next'))
    act(() => result.current.loadMore())
    await waitFor(() => expect(result.current.nextPageToken).toBe(''))

    expect(result.current.items).toEqual([])
    expect(result.current.nextPageToken).toBe('')
  })

  it('同一下一页在途时只请求一次', async () => {
    const next = deferred<Page<string>>()
    let nextCalls = 0
    const loadPage = (token: string) => {
      if (token === '') return Promise.resolve({ items: ['first'], nextPageToken: 'next' })
      nextCalls++
      return next.promise
    }
    const { result } = renderHook(() => useCursorCollection(loadPage))

    await waitFor(() => expect(result.current.nextPageToken).toBe('next'))
    act(() => {
      result.current.loadMore()
      result.current.loadMore()
    })
    expect(nextCalls).toBe(1)

    act(() => next.resolve({ items: ['second'], nextPageToken: '' }))
    await waitFor(() => expect(result.current.items).toEqual(['first', 'second']))
  })

  it('数据源变化后丢弃旧请求结果', async () => {
    const oldPage = deferred<Page<string>>()
    const { result, rerender } = renderHook(
      ({ source }) => {
        const load = useCallback(
          () => source === 'old' ? oldPage.promise : Promise.resolve({ items: ['new'], nextPageToken: '' }),
          [source],
        )
        return useCursorCollection(load)
      },
      { initialProps: { source: 'old' } },
    )

    rerender({ source: 'new' })
    await waitFor(() => expect(result.current.items).toEqual(['new']))
    act(() => oldPage.resolve({ items: ['stale'], nextPageToken: '' }))
    await act(async () => Promise.resolve())

    expect(result.current.items).toEqual(['new'])
  })

  it('首批读取失败后可显式重试并清除错误', async () => {
    let first = true
    const loadPage = () => {
      if (first) {
        first = false
        return Promise.reject(new Error('brain unavailable'))
      }
      return Promise.resolve({ items: ['recovered'], nextPageToken: '' })
    }
    const { result } = renderHook(() => useCursorCollection(loadPage))

    await waitFor(() => expect(result.current.error?.message).toBe('brain unavailable'))
    expect(result.current.status).toBe('error')
    act(() => result.current.reload())
    await waitFor(() => expect(result.current.items).toEqual(['recovered']))
    expect(result.current.error).toBeNull()
  })

  it('下一页失败保留游标，并允许同一游标再次请求', async () => {
    let nextAttempts = 0
    const loadPage = (token: string): Promise<Page<string>> => {
      if (token === '') return Promise.resolve({ items: ['first'], nextPageToken: 'next' })
      nextAttempts += 1
      if (nextAttempts === 1) return Promise.reject(new Error('temporary failure'))
      return Promise.resolve({ items: ['second'], nextPageToken: '' })
    }
    const { result } = renderHook(() => useCursorCollection(loadPage))

    await waitFor(() => expect(result.current.nextPageToken).toBe('next'))
    act(() => result.current.loadMore())
    await waitFor(() => expect(result.current.loadMoreError?.message).toBe('temporary failure'))
    expect(result.current.nextPageToken).toBe('next')
    act(() => result.current.loadMore())
    await waitFor(() => expect(result.current.items).toEqual(['first', 'second']))
    expect(nextAttempts).toBe(2)
  })

  it('服务端重复返回已请求游标时停止无限翻页并报告原因', async () => {
    const loadPage = (token: string): Promise<Page<string>> =>
      Promise.resolve(token === '' ? { items: ['first'], nextPageToken: 'next' } : { items: ['second'], nextPageToken: 'next' })
    const { result } = renderHook(() => useCursorCollection(loadPage))

    await waitFor(() => expect(result.current.nextPageToken).toBe('next'))
    act(() => result.current.loadMore())
    await waitFor(() => expect(result.current.items).toEqual(['first', 'second']))
    expect(result.current.nextPageToken).toBe('')
    expect(result.current.loadMoreError?.message).toBe('服务端返回了重复分页游标')
  })
})
