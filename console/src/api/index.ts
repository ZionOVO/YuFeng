// 客户端工厂只装配 ConnectClient，直连 brain 已登记的 yufeng.* 服务。
// 测试替身只能通过 AuthProvider 的 client 参数注入，不得进入运行时依赖图。

import type { ConsoleClient } from './client'
import { ConnectClient } from './connect'

export interface ClientFactoryOptions {
  /** 读取当前会话令牌（auth/session.ts 的 loadSession）。 */
  getToken?: () => string | null
  /** 收到 unauthenticated 时回调（清会话并回登录页）。 */
  onUnauthenticated?: () => void
}

export function createClient(opts: ClientFactoryOptions = {}): ConsoleClient {
  return new ConnectClient({
    getToken: opts.getToken ?? (() => null),
    onUnauthenticated: opts.onUnauthenticated ?? (() => undefined),
  })
}

export type { ConsoleClient } from './client'
export { binds, canOnAsset, canOnRelease, containsBindings, emptyAccess, hasTool } from './access'
export { TOOL } from './types'
export type { BindingRef, EffectiveAccess, Grant, ToolName } from './types'
export { ApiError, hasCode, isApiError } from './errors'
export type { ConnectErrorCode } from './errors'
