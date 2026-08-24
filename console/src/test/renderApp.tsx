// 页面测试脚手架：用 MemoryRouter + HeroUIProvider + AuthProvider 组装完整 App，
// 默认注入测试专用 ConsoleClientFixture；控制台运行时不会导入本文件。
//
// 用法：
//   const { client } = renderApp({ route: '/login' })          // 匿名入口
//   await loginAs(client)                                       // 走通登录并预置 sessionStorage
//   renderApp({ route: '/releases/rel_01J8VN8P', client })     // 直刷已登录页面

import { render } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { HeroUIProvider } from '@heroui/react'
import { App } from '../App'
import { AuthProvider } from '../auth/AuthContext'
import type { ConsoleClient } from '../api'
import type { Session } from '../api/types'
import { emptyAccess } from '../api/access'
import { ConsoleClientFixture } from './fixtures/consoleClient'
import { saveSession } from '../auth/session'

export function renderApp(opts: { route?: string; client?: ConsoleClient } = {}) {
  const client = opts.client ?? new ConsoleClientFixture()
  const utils = render(
    <HeroUIProvider>
      <MemoryRouter initialEntries={[opts.route ?? '/login']}>
        <AuthProvider client={client}>
          <App />
        </AuthProvider>
      </MemoryRouter>
    </HeroUIProvider>,
  )
  return { client, ...utils }
}

/** 通过测试客户端登录并写入 sessionStorage，建立“已登录后刷新页面”的初始状态。 */
export async function loginAs(
  client: ConsoleClientFixture,
  username = 'admin',
  password = 'admin123456',
): Promise<Session> {
  const session = await client.login({ username, password })
  saveSession(session)
  return session
}

/** 预置一个服务端已不认可的会话（令牌有效格式但用户不存在），用于会话过期场景。 */
export function seedStaleSession(): void {
  saveSession({
    token: 'fixture-token-ghost',
    expiresAt: '2026-08-17T00:00:00.000Z',
    user: {
      userId: 'user-ghost',
      username: 'ghost',
      displayName: '幽灵',
      role: 'USER_ROLE_VIEWER',
      state: 'USER_STATE_ACTIVE',
    },
    access: emptyAccess(),
  })
}
