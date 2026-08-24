import { useCallback, useEffect, useRef, useState } from 'react'
import type { Page } from '../api/client'

export type CursorCollectionStatus = 'loading' | 'error' | 'ok'

export interface CursorCollection<T> {
  items: T[]
  nextPageToken: string
  status: CursorCollectionStatus
  error: Error | null
  loadingMore: boolean
  loadMoreError: Error | null
  loadMore: () => void
  reload: () => void
}

interface CursorCollectionState<T> {
  items: T[]
  nextPageToken: string
  status: CursorCollectionStatus
  error: Error | null
  loadingMore: boolean
  loadMoreError: Error | null
}

function initialState<T>(): CursorCollectionState<T> {
  return {
    items: [],
    nextPageToken: '',
    status: 'loading',
    error: null,
    loadingMore: false,
    loadMoreError: null,
  }
}

function asError(cause: unknown): Error {
  return cause instanceof Error ? cause : new Error(String(cause))
}

// useCursorCollection 按服务端不透明游标累积列表，并丢弃数据源切换后的旧响应。
// 同一游标在途或已经成功请求时不会再次发起；失败后可用同一游标显式重试。
export function useCursorCollection<T>(loadPage: (pageToken: string) => Promise<Page<T>>): CursorCollection<T> {
  const [revision, setRevision] = useState(0)
  const [state, setState] = useState<CursorCollectionState<T>>(initialState)
  const generation = useRef(0)
  const requestedTokens = useRef(new Set<string>())

  useEffect(() => {
    const currentGeneration = generation.current + 1
    generation.current = currentGeneration
    requestedTokens.current = new Set([''])
    queueMicrotask(() => {
      if (generation.current !== currentGeneration) return
      setState(initialState())

      void loadPage('').then(
        (page) => {
          if (generation.current !== currentGeneration) return
          const repeatedCursor = page.nextPageToken !== '' && requestedTokens.current.has(page.nextPageToken)
          setState({
            items: page.items,
            nextPageToken: repeatedCursor ? '' : page.nextPageToken,
            status: 'ok',
            error: null,
            loadingMore: false,
            loadMoreError: repeatedCursor ? new Error('服务端返回了重复分页游标') : null,
          })
        },
        (cause: unknown) => {
          if (generation.current !== currentGeneration) return
          requestedTokens.current.delete('')
          setState({ ...initialState<T>(), status: 'error', error: asError(cause) })
        },
      )
    })

    return () => {
      if (generation.current === currentGeneration) generation.current++
    }
  }, [loadPage, revision])

  const loadMore = useCallback(() => {
    const pageToken = state.nextPageToken
    if (pageToken === '' || requestedTokens.current.has(pageToken)) return
    const currentGeneration = generation.current
    requestedTokens.current.add(pageToken)
    setState((current) => ({ ...current, loadingMore: true, loadMoreError: null }))

    void loadPage(pageToken).then(
      (page) => {
        if (generation.current !== currentGeneration) return
        const repeatedCursor = page.nextPageToken !== '' && requestedTokens.current.has(page.nextPageToken)
        setState((current) => {
          if (current.nextPageToken !== pageToken) return current
          return {
            ...current,
            items: [...current.items, ...page.items],
            nextPageToken: repeatedCursor ? '' : page.nextPageToken,
            loadingMore: false,
            loadMoreError: repeatedCursor ? new Error('服务端返回了重复分页游标') : null,
          }
        })
      },
      (cause: unknown) => {
        if (generation.current !== currentGeneration) return
        requestedTokens.current.delete(pageToken)
        setState((current) => {
          if (current.nextPageToken !== pageToken) return current
          return { ...current, loadingMore: false, loadMoreError: asError(cause) }
        })
      },
    )
  }, [loadPage, state.nextPageToken])

  const reload = useCallback(() => {
    // 同一事件循环内先使在途响应失效，再安排新的首批请求。
    generation.current++
    requestedTokens.current.clear()
    setRevision((current) => current + 1)
  }, [])

  return { ...state, loadMore, reload }
}
