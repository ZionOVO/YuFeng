// 认证上下文：会话恢复、登录/登出、角色判断与全局 API 客户端持有。
// 角色控制仅用于 UX（隐藏写按钮/菜单），不构成安全边界——鉴权以服务端为准（docs/api.md §0.5、§17.2）。

import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { createClient } from '../api'
import type { ConsoleClient } from '../api'
import type { EffectiveAccess, Onboarding, User } from '../api/types'
import { binds as bindsObject, canOnAsset as assetGranted, emptyAccess, hasTool as toolGranted } from '../api/access'
import { isApiError } from '../api/errors'
import { clearSession, loadSession, saveSession } from './session'
import { AuthContext, type AuthContextValue, type AuthStatus } from './useAuth'

export function AuthProvider({ children, client: injected }: { children: ReactNode; client?: ConsoleClient }) {
  const [user, setUser] = useState<User | null>(null)
  const [access, setAccess] = useState<EffectiveAccess>(emptyAccess())
  const [onboarding, setOnboarding] = useState<Onboarding | null>(null)
  // 无本地会话则直接 anon，避免在 effect 里同步 setState；有会话则先 loading 等 GetMe 校验
  const [status, setStatus] = useState<AuthStatus>(() => (loadSession() === null ? 'anon' : 'loading'))

  // injected 仅供测试注入替代客户端；操作员路径走 createClient（默认 ConnectClient）
  const [client] = useState<ConsoleClient>(
    () =>
      injected ??
      createClient({
        getToken: () => loadSession()?.token ?? null,
        // 令牌失效：清会话并置 anon，跳转由 RequireAuth 守卫统一完成（docs/api.md §17.2）
        onUnauthenticated: () => {
          clearSession()
          setUser(null)
          setAccess(emptyAccess())
          setOnboarding(null)
          setStatus('anon')
        },
      }),
  )

  // 启动恢复登录态：sessionStorage 有会话则 GetMe 校验（docs/api.md §17.2）
  useEffect(() => {
    if (loadSession() === null) return
    let cancelled = false
    let retry: number | undefined
    const restore = () => {
      void Promise.all([client.getMe(), client.getMyAccess(), client.getOnboarding()])
        .then(([me, acc, ob]) => {
          if (cancelled) return
          setUser(me)
          setAccess(acc)
          setOnboarding(ob)
          setStatus('authed')
        })
        .catch((cause: unknown) => {
          if (cancelled) return
          if (isApiError(cause) && cause.code === 'unauthenticated') {
            clearSession()
            setUser(null)
            setAccess(emptyAccess())
            setOnboarding(null)
            setStatus('anon')
            return
          }
          // 短暂网络或中台不可用不等同于会话失效；保留令牌并重新读取最新权限。
          retry = window.setTimeout(restore, 1000)
        })
    }
    restore()
    return () => {
      cancelled = true
      if (retry !== undefined) window.clearTimeout(retry)
    }
  }, [client])

  const login = useCallback(
    async (username: string, password: string): Promise<User> => {
      const session = await client.login({ username, password })
      saveSession(session)
      setUser(session.user)
      setAccess(session.access ?? emptyAccess())
      const ob = await client.getOnboarding()
      setOnboarding(ob)
      setStatus('authed')
      return session.user
    },
    [client],
  )

  const refreshOnboarding = useCallback(async (): Promise<Onboarding> => {
    const ob = await client.getOnboarding()
    setOnboarding(ob)
    return ob
  }, [client])

  const refreshAccess = useCallback(async (): Promise<EffectiveAccess> => {
    const acc = await client.getMyAccess()
    setAccess(acc)
    const sess = loadSession()
    if (sess !== null) saveSession({ ...sess, access: acc })
    return acc
  }, [client])

  const logout = useCallback(async () => {
    try {
      await client.logout()
    } catch {
      // 登出失败（网络/令牌已失效）不阻塞本地清理
    }
    clearSession()
    setUser(null)
    setAccess(emptyAccess())
    setOnboarding(null)
    // 置 anon 后由 RequireAuth 守卫跳回登录页
    setStatus('anon')
  }, [client])

  const value = useMemo<AuthContextValue>(
    () => ({
      client,
      user,
      access,
      onboarding,
      status,
      canWrite: access.tools.some((t) => t !== 'console.read' && t !== 'user.admin'),
      isAdmin: toolGranted(access, 'user.admin'),
      isOnboardingComplete: onboarding?.state === 'ONBOARDING_STATE_COMPLETED',
      hasTool: (tool: string) => toolGranted(access, tool),
      binds: (kind, id) => bindsObject(access, kind, id),
      canOnAsset: (tool, assetId) => assetGranted(access, tool, assetId),
      refreshOnboarding,
      refreshAccess,
      login,
      logout,
    }),
    [client, user, access, onboarding, status, login, logout, refreshOnboarding, refreshAccess],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
