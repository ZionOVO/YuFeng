// 通用异步数据加载 hook：统一 loading / error / ok 三态与手动刷新。
// unauthenticated 由 AuthProvider 的全局回调接管（清会话回登录页），这里只暴露 error。

import { useCallback, useEffect, useState } from 'react'
import { ApiError, isApiError } from '../api/errors'

export type DataStatus = 'loading' | 'error' | 'ok'

export interface AsyncData<T> {
  data: T | null
  status: DataStatus
  error: ApiError | null
  reload: () => void
}

interface Settled<T> {
  depsKey: string
  reqKey: string
  data: T | null
  error: ApiError | null
}

export function useAsyncData<T>(loader: () => Promise<T>, deps: readonly unknown[], retainAcrossDependencies = true): AsyncData<T> {
  const [nonce, setNonce] = useState(0)
  // reqKey 标识一次请求：deps 内容变化或手动刷新（nonce 递增）都会得到新 key
  const depsKey = JSON.stringify(deps)
  const reqKey = `${depsKey}#${nonce}`
  const [settled, setSettled] = useState<Settled<T> | null>(null)

  useEffect(() => {
    let cancelled = false
    loader().then(
      (d) => {
        if (!cancelled) setSettled({ depsKey, reqKey, data: d, error: null })
      },
      (e: unknown) => {
        if (cancelled) return
        setSettled({
          depsKey,
          reqKey,
          data: null,
          error: isApiError(e) ? e : new ApiError({ code: 'unknown', message: String(e), httpStatus: 0 }),
        })
      },
    )
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- loader 随当次渲染捕获，请求时机由 reqKey 表达
  }, [reqKey])

  const reload = useCallback(() => setNonce((n) => n + 1), [])

  // 状态派生而非存储：error 只认当前 reqKey；列表默认跨依赖保留上一份成功结果，
  // 筛选/翻页触发新请求时控件不会随整页 loading 卸载（丢焦点），新数据到达后整体替换。
  const current = settled !== null && settled.reqKey === reqKey ? settled : null
  // 对象详情切换标识时不得把旧对象带进新对象的写入口；同一对象手动刷新仍保留旧投影。
  const data = settled !== null && (retainAcrossDependencies || settled.depsKey === depsKey) ? settled.data : null
  const error = current?.error ?? null
  const status: DataStatus = error !== null ? 'error' : data !== null ? 'ok' : 'loading'
  return { data, status, error, reload }
}
