// /setup 验证管理员提交规格、技术人员人工启动 Edge、再完成资产与授予的六步闭环。

import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { vi } from 'vitest'
import { ApiError } from '../../api/errors'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../../test/renderApp'

vi.mock('./hold', () => ({
  SetupOkHoldMs: 0,
  SetupModelProbeAttempts: 3,
  SetupModelProbeRetryMs: 5,
  holdAtLeast: async () => undefined,
  sleep: async () => new Promise<void>((resolve) => window.setTimeout(resolve, 5)),
}))

beforeEach(() => {
  sessionStorage.clear()
})

async function submitSpecification(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByRole('region', { name: '提交部署规格' })
  await user.type(screen.getByLabelText('流量键'), 'site-a')
  await user.type(screen.getByLabelText('真实上游地址'), 'http://app:8080')
  await user.click(screen.getByRole('button', { name: '确定并签发部署规格' }))
}

describe('引导页', () => {
  it('未完成时只显示新六步，不展示自动部署或贾维斯部署步骤', async () => {
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_PENDING' })
    await loginAs(client)
    renderApp({ route: '/dashboard', client })

    expect(await screen.findByRole('region', { name: '初次配置引导' })).toBeInTheDocument()
    for (const label of ['配置模型', '探测连通', '提交部署规格', '人工安装 Edge', '设置防御资产', '授权值守账户']) {
      expect(screen.getAllByText(label).length).toBeGreaterThan(0)
    }
    expect(screen.queryByText('等待贾维斯')).toBeNull()
    expect(screen.queryByText('部署数据面')).toBeNull()
    expect(screen.queryByRole('link', { name: /仪表盘/ })).toBeNull()
  })

  it('非管理员只见等待页，不见密钥表单', async () => {
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_PENDING' })
    await loginAs(client, 'operator-chen', 'operator123456')
    renderApp({ route: '/setup', client })

    expect(await screen.findByText('等待管理员完成初次配置')).toBeInTheDocument()
    expect(screen.queryByLabelText('模型密钥')).toBeNull()
  })

  it('模型探测失败后回到配置模型且不能跳过闭环', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_PENDING' })
    client.failNextModelTests(99)
    const probe = vi.spyOn(client, 'testModelConnectivity')
    await loginAs(client)
    renderApp({ route: '/setup', client })

    await user.type(await screen.findByLabelText('模型密钥'), 'sk-bad-key')
    await user.click(screen.getByRole('button', { name: '保存并探测连通' }))

    await waitFor(() => expect(probe).toHaveBeenCalledTimes(3))
    expect(await screen.findByRole('region', { name: '配置模型' })).toBeInTheDocument()
    expect(screen.queryByText(/跳过设置模型/)).toBeNull()
    expect(screen.queryByRole('region', { name: '提交部署规格' })).toBeNull()
  })

  it('提交完整签名输入后只展示两种人工安装方式并等待主动心跳', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_MODEL_LIVE' })
    const put = vi.spyOn(client, 'putDeploymentSpecification')
    await loginAs(client)
    renderApp({ route: '/setup', client })

    await submitSpecification(user)

    expect(await screen.findByRole('region', { name: '人工安装 Edge' })).toBeInTheDocument()
    expect(screen.getByText(/systemctl enable --now yufeng-edge/)).toBeInTheDocument()
    expect(screen.getByText(/compose\.edge-modelside\.yaml up -d/)).toBeInTheDocument()
    expect(screen.getByText(/正在等待 Edge 主动注册/)).toBeInTheDocument()
    expect(put).toHaveBeenCalledWith(
      expect.objectContaining({
        unitId: 'edge-1',
        assetId: 'asset-local',
        posture: 'INGRESS_POSTURE_REVERSE_PROXY',
        trafficKey: 'site-a',
        reverseProxy: { listenAddress: ':18080', upstreamUrl: 'http://app:8080' },
        modelIngressWindow: {
          maxItems: 4096,
          maxRetainedBytes: String(128 * 1024 * 1024),
          maxQueueAge: '2s',
        },
        modelProfile: expect.objectContaining({
          modelType: 'PVM',
          alertThreshold: 0.9,
          reviewFloor: 0.5,
          reviewWindowSeconds: 300,
          maxReviewPerUnit: 4,
          maxReviewPerRoute: 1,
        }),
      }),
    )
  })

  it('非法可信代理网段在浏览器侧阻止规格提交', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_MODEL_LIVE' })
    const put = vi.spyOn(client, 'putDeploymentSpecification')
    await loginAs(client)
    renderApp({ route: '/setup', client })

    await screen.findByRole('region', { name: '提交部署规格' })
    await user.type(screen.getByLabelText('流量键'), 'orders')
    await user.type(screen.getByLabelText('真实上游地址'), 'http://orders:8080')
    await user.type(screen.getByLabelText('可信代理网段'), '10.0.0.0')
    await user.click(screen.getByRole('button', { name: '确定并签发部署规格' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('有效的 IPv4 或 IPv6 CIDR')
    expect(put).not.toHaveBeenCalled()
  })

  it('Edge 主动回执制品坐标后才能进入资产与授权并完成引导', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_MODEL_LIVE' })
    await loginAs(client)
    renderApp({ route: '/setup', client })

    await submitSpecification(user)
    await screen.findByRole('region', { name: '人工安装 Edge' })
    client.setEdgeReady(true)
    expect(await screen.findByRole('region', { name: '设置防御资产' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '下一步，授权值守账户' }))
    await user.type(await screen.findByLabelText('值守用户名'), 'ops-other')
    await user.type(screen.getByLabelText('值守密码'), 'other-pass-123')
    await user.type(screen.getByLabelText('显示名'), '值守')
    await user.click(screen.getByRole('button', { name: '创建值守账户' }))

    await waitFor(() => expect(screen.getByText('资产总数')).toBeInTheDocument())
  })

  it('步骤六写授予瞬时失败后复用已创建用户安全重试', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_EDGE_LIVE' })
    const realPutGrant = client.putGrant.bind(client)
    const putGrant = vi
      .spyOn(client, 'putGrant')
      .mockRejectedValueOnce(new ApiError({ code: 'unavailable', message: 'temporary failure', httpStatus: 503 }))
      .mockImplementation(realPutGrant)
    await loginAs(client)
    renderApp({ route: '/setup', client })

    await user.click(await screen.findByRole('button', { name: '下一步，授权值守账户' }))
    await user.type(await screen.findByLabelText('值守用户名'), 'retry-ops')
    await user.type(screen.getByLabelText('值守密码'), 'retry-pass-123')
    await user.type(screen.getByLabelText('显示名'), '重试值守')
    await user.click(screen.getByRole('button', { name: '创建值守账户' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('temporary failure')

    await user.click(screen.getByRole('button', { name: '创建值守账户' }))
    await waitFor(() => expect(screen.getByText('资产总数')).toBeInTheDocument())
    expect(putGrant).toHaveBeenCalledTimes(2)
  })
})
