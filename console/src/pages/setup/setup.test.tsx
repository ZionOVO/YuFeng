// /setup 只验证模型网关与贾维斯两项控制面谓词，数据面在主控制台按资产接入。

import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { vi } from 'vitest'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../../test/renderApp'

beforeEach(() => {
  sessionStorage.clear()
})

describe('初次配置页', () => {
  it('未完成时只展示模型网关、连通性、贾维斯和显式进入控制台四步', async () => {
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_PENDING' })
    await loginAs(client)
    renderApp({ route: '/dashboard', client })

    expect(await screen.findByRole('region', { name: '初次配置引导' })).toBeInTheDocument()
    for (const label of ['配置模型网关', '探测连通性', '确认贾维斯在线', '进入主控制台']) {
      expect(screen.getAllByText(label).length).toBeGreaterThan(0)
    }
    for (const retired of ['提交部署规格', '人工安装 Edge', '设置防御资产', '授权值守账户']) {
      expect(screen.queryByText(retired)).toBeNull()
    }
    expect(screen.queryByRole('link', { name: /仪表盘/ })).toBeNull()
  })

  it('非管理员只见等待页，不见密钥表单', async () => {
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_PENDING' })
    await loginAs(client, 'operator-chen', 'operator123456')
    renderApp({ route: '/setup', client })

    expect(await screen.findByText('等待管理员完成初次配置')).toBeInTheDocument()
    expect(screen.queryByLabelText('模型密钥')).toBeNull()
  })

  it('模型探测失败后保留配置并允许重试，不能跳过连通性', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_PENDING' })
    client.failNextModelTests(1)
    const probe = vi.spyOn(client, 'testModelConnectivity')
    await loginAs(client)
    renderApp({ route: '/setup', client })

    await user.type(await screen.findByLabelText('模型密钥（可选）'), 'sk-bad-key')
    await user.click(screen.getByRole('button', { name: '保存模型网关' }))
    await user.click(await screen.findByRole('button', { name: '探测模型网关' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('model endpoint unreachable')
    expect(probe).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('region', { name: '探测模型网关' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '确认贾维斯在线' })).toBeNull()
    expect(screen.queryByRole('button', { name: '进入主控制台' })).toBeNull()
  })

  it('允许 HTTP 无 Key 配置并进入真实连通性探测', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_PENDING' })
    await loginAs(client)
    renderApp({ route: '/setup', client })

    const endpoint = await screen.findByLabelText('模型端点')
    await user.clear(endpoint)
    await user.type(endpoint, 'http://model.internal:8000/v1')
    expect(screen.getByLabelText('模型密钥（可选）')).toHaveValue('')
    const save = screen.getByRole('button', { name: '保存模型网关' })
    expect(save).toBeEnabled()
    await user.click(save)

    const configured = await client.getOnboarding()
    expect(configured.baseUrl).toBe('http://model.internal:8000/v1')
    expect(configured.hasSecret).toBe(false)
    await user.click(await screen.findByRole('button', { name: '探测模型网关' }))
    expect(await screen.findByRole('region', { name: '确认贾维斯在线' })).toBeInTheDocument()
  })

  it('模型探测成功后等待贾维斯主动在线，不调用任何数据面接入接口', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_PENDING' })
    const putEdge = vi.spyOn(client, 'putEdgeEnrollment')
    await loginAs(client)
    renderApp({ route: '/setup', client })

    await user.type(await screen.findByLabelText('模型密钥（可选）'), 'sk-test-key')
    await user.click(screen.getByRole('button', { name: '保存模型网关' }))
    await user.click(await screen.findByRole('button', { name: '探测模型网关' }))

    const jarvis = await screen.findByRole('region', { name: '确认贾维斯在线' })
    expect(screen.getByRole('status', { name: '等待注册' })).toBeInTheDocument()
    expect(jarvis).toHaveTextContent('数据面不参与初次配置')
    expect(screen.getByRole('button', { name: '进入主控制台' })).toBeDisabled()
    expect(putEdge).not.toHaveBeenCalled()
  })

  it('两项谓词满足后可在零资产状态进入主控制台', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_MODEL_LIVE' })
    client.setJarvisOnline(true)
    const complete = vi.spyOn(client, 'completeOnboarding')
    await loginAs(client)
    renderApp({ route: '/setup', client })

    await user.click(await screen.findByRole('button', { name: '进入主控制台' }))

    await waitFor(() => expect(screen.getByText('资产总数')).toBeInTheDocument())
    expect(screen.getByText('资产总数').nextElementSibling).toHaveTextContent('0')
    expect(complete).toHaveBeenCalledTimes(1)
  })

  it('模型已在线但贾维斯稍后上线时，可刷新状态再进入控制台', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_MODEL_LIVE' })
    await loginAs(client)
    renderApp({ route: '/setup', client })

    expect(await screen.findByRole('button', { name: '刷新在线状态' })).toBeInTheDocument()
    client.setJarvisOnline(true)
    await user.click(screen.getByRole('button', { name: '刷新在线状态' }))

    expect(await screen.findByText('模型网关和贾维斯两项控制面条件均已满足，可以显式进入主控制台。')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '进入主控制台' })).toBeEnabled()
  })
})
