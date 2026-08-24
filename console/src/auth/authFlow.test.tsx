// 登录页面测试：测试客户端返回会话后写入 sessionStorage（键 yufeng.session）。
// 脚手架 renderApp/seedStaleSession 见 src/test/renderApp.tsx；错误文案见 LoginPage 的 loginErrorText。

import { act, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, vi } from 'vitest'
import { ApiError } from '../api/errors'
import { ConsoleClientFixture } from '../test/fixtures/consoleClient'
import { loginAs, renderApp, seedStaleSession } from '../test/renderApp'
import { SESSION_KEY } from './session'

// 同一文件内的用例共享 jsdom，sessionStorage 不会自动隔离，统一在每个用例前清空
beforeEach(() => {
  sessionStorage.clear()
})

afterEach(() => {
  vi.useRealTimers()
})

/** 填写登录表单并点击“登录”提交。 */
async function submitLogin(username: string, password: string) {
  const user = userEvent.setup()
  await user.type(await screen.findByLabelText('用户名'), username)
  await user.type(screen.getByLabelText('密码'), password)
  await user.click(screen.getByRole('button', { name: '登录' }))
}

describe('登录流程', () => {
  it('登录成功进入仪表盘并写入会话', async () => {
    renderApp({ route: '/login' })
    await submitLogin('admin', 'admin123456')

    // 仪表盘锚点：关键指标卡“资产总数”
    expect(await screen.findByText('资产总数')).toBeInTheDocument()
    expect(sessionStorage.getItem(SESSION_KEY)).toContain('fixture-token-admin')
  })

  it('密码错误提示错误并停留在登录页', async () => {
    renderApp({ route: '/login' })
    await submitLogin('admin', 'wrong-password')

    expect(await screen.findByRole('alert')).toHaveTextContent('用户名或密码错误')
    // 仍在登录页：登录按钮还在，且本地没有会话
    expect(screen.getByRole('button', { name: '登录' })).toBeInTheDocument()
    expect(sessionStorage.getItem(SESSION_KEY)).toBeNull()
  })

  it('停用账户登录被拒并提示联系管理员', async () => {
    renderApp({ route: '/login' })
    await submitLogin('temp-ops', 'disabled123456')

    expect(await screen.findByRole('alert')).toHaveTextContent('账户已停用，请联系管理员')
    expect(screen.getByRole('button', { name: '登录' })).toBeInTheDocument()
    expect(sessionStorage.getItem(SESSION_KEY)).toBeNull()
  })

  it('会话过期（服务端不认可）被送回登录页并清除本地会话', async () => {
    seedStaleSession()
    renderApp({ route: '/dashboard', client: new ConsoleClientFixture() })

    // GetMe 抛 unauthenticated → AuthProvider 清会话置 anon → RequireAuth 守卫跳回登录页
    expect(await screen.findByRole('button', { name: '登录' })).toBeInTheDocument()
    expect(screen.getByText('御锋控制台')).toBeInTheDocument()
    expect(sessionStorage.getItem(SESSION_KEY)).toBeNull()
  })

  it('恢复会话遇到短暂不可用时保留令牌并重试，而不是误判为退出登录', async () => {
    class FlakyRestoreClient extends ConsoleClientFixture {
      private failures = 1

      override async getMe() {
        if (this.failures > 0) {
          this.failures--
          throw new ApiError({ code: 'unavailable', message: 'brain unavailable', httpStatus: 0 })
        }
        return super.getMe()
      }
    }

    vi.useFakeTimers()
    const client = new FlakyRestoreClient()
    await loginAs(client)
    renderApp({ route: '/dashboard', client })

    expect(screen.getByLabelText('正在恢复会话')).toBeInTheDocument()
    expect(sessionStorage.getItem(SESSION_KEY)).toContain('fixture-token-admin')
    await act(async () => {
      await Promise.resolve()
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000)
    })
    await act(async () => {
      await Promise.resolve()
    })
    expect(screen.getByText('资产总数')).toBeInTheDocument()
    expect(sessionStorage.getItem(SESSION_KEY)).toContain('fixture-token-admin')
  })
})
