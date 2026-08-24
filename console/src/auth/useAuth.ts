import { createContext, useContext } from 'react'
import type { ConsoleClient } from '../api'
import type { EffectiveAccess, Onboarding, User } from '../api/types'

export type AuthStatus = 'loading' | 'authed' | 'anon'

export interface AuthContextValue {
  client: ConsoleClient
  user: User | null
  access: EffectiveAccess
  onboarding: Onboarding | null
  status: AuthStatus
  /** 有任一写工具（用户体验提示）。 */
  canWrite: boolean
  /** 持 user.admin：用户管理入口。 */
  isAdmin: boolean
  isOnboardingComplete: boolean
  hasTool: (tool: string) => boolean
  binds: (kind: 'asset' | 'unit' | 'release', id: string) => boolean
  canOnAsset: (tool: string, assetId: string) => boolean
  refreshOnboarding: () => Promise<Onboarding>
  refreshAccess: () => Promise<EffectiveAccess>
  login: (username: string, password: string) => Promise<User>
  logout: () => Promise<void>
}

export const AuthContext = createContext<AuthContextValue | null>(null)

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (ctx === null) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
