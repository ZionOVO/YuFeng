// 用户会话的 sessionStorage 存取（docs/api.md §17.2：键 yufeng.session）。
// 令牌不落 localStorage、不进日志；收到 unauthenticated 时由 AuthProvider 调 clearSession。

import { emptyAccess } from '../api/access'
import type { Session } from '../api/types'

export const SESSION_KEY = 'yufeng.session'

export function loadSession(): Session | null {
  const raw = sessionStorage.getItem(SESSION_KEY)
  if (raw === null) return null
  try {
    const parsed = JSON.parse(raw) as Partial<Session>
    if (typeof parsed.token !== 'string' || parsed.token === '' || typeof parsed.user?.userId !== 'string') {
      return null
    }
    return { ...parsed, access: parsed.access ?? emptyAccess() } as Session
  } catch {
    return null
  }
}

export function saveSession(session: Session): void {
  sessionStorage.setItem(SESSION_KEY, JSON.stringify(session))
}

export function clearSession(): void {
  sessionStorage.removeItem(SESSION_KEY)
}
