import { render, screen } from '@testing-library/react'
import { HeroUIProvider } from '@heroui/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { ApiError } from '../../api/errors'
import { emptyAccess } from '../../api/access'
import type { DashboardSummary } from '../../api/types'
import { AuthContext, type AuthContextValue } from '../../auth/useAuth'
import { JarvisSessionContext, type JarvisSessionValue } from '../../components/chat/useJarvisSession'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../../test/renderApp'
import { DashboardPage } from './DashboardPage'

beforeEach(() => {
  sessionStorage.clear()
})

function renderDashboard(client: ConsoleClientFixture) {
  const auth: AuthContextValue = {
    client,
    user: null,
    access: emptyAccess(),
    onboarding: null,
    status: 'authed',
    canWrite: false,
    isAdmin: false,
    isOnboardingComplete: true,
    hasTool: () => false,
    binds: () => false,
    canOnAsset: () => false,
    refreshOnboarding: () => client.getOnboarding(),
    refreshAccess: () => client.getMyAccess(),
    login: async () => client.getMe(),
    logout: async () => {},
  }
  const jarvis: JarvisSessionValue = {
    sessionId: null,
    messages: [],
    busy: false,
    thinking: false,
    error: null,
    contextLabel: '中台',
    setContextLabel: () => {},
    focusAssetIds: [],
    dockOpen: false,
    setDockOpen: () => {},
    pendingGate: false,
    jarvisOnline: null,
    refreshSignals: () => {},
    ensureSession: async () => null,
    send: async () => {},
  }
  return render(
    <HeroUIProvider>
      <MemoryRouter>
        <AuthContext.Provider value={auth}>
          <JarvisSessionContext.Provider value={jarvis}>
            <DashboardPage />
          </JarvisSessionContext.Provider>
        </AuthContext.Provider>
      </MemoryRouter>
    </HeroUIProvider>,
  )
}

describe('仪表盘', () => {
  it('展示业务指标、最近事件，并可从状态管道进入发布治理', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/dashboard', client })

    expect(await screen.findByRole('region', { name: '关键指标' })).toBeInTheDocument()
    expect(screen.getByText('资产总数').parentElement).toHaveTextContent('3')
    expect(screen.getByText('24H 拦截').parentElement).toHaveTextContent('阻断率')
    expect(screen.getByLabelText('最近事件')).toBeInTheDocument()

    await user.click(screen.getAllByRole('button', { name: /小比例/ })[0])
    expect(await screen.findByRole('region', { name: '防护策略列表' })).toBeInTheDocument()
  })

  it('从紧凑资产拓扑打开所选资产详情', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/dashboard', client })

    await user.click(await screen.findByRole('button', { name: '选中资产 core-payments' }))
    expect(await screen.findByRole('region', { name: '资产元数据' })).toBeInTheDocument()
  })

  it('仪表盘读取失败时提供重试并恢复数据', async () => {
    let attempts = 0
    class RetryDashboardFixture extends ConsoleClientFixture {
      override async dashboard() {
        attempts += 1
        if (attempts === 1) {
          throw new ApiError({ code: 'unavailable', message: 'dashboard service unavailable', httpStatus: 0 })
        }
        return super.dashboard()
      }
    }

    const user = userEvent.setup()
    const client = new RetryDashboardFixture()
    await loginAs(client)
    renderDashboard(client)

    expect(await screen.findByRole('alert')).toHaveTextContent('dashboard service unavailable')
    await user.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByText('资产总数')).toBeInTheDocument()
    expect(attempts).toBe(2)
  })

  it('仪表盘的权限拒绝采用无权限状态，而不是泄露后端错误', async () => {
    class DeniedDashboardFixture extends ConsoleClientFixture {
      override async dashboard(): Promise<DashboardSummary> {
        throw new ApiError({ code: 'permission_denied', message: 'object out of bindings', httpStatus: 403 })
      }
    }

    const client = new DeniedDashboardFixture()
    await loginAs(client)
    renderDashboard(client)

    expect(await screen.findByRole('alert')).toHaveTextContent('没有权限')
    expect(screen.queryByText('object out of bindings')).toBeNull()
  })
})
