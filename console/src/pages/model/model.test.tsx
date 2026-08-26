// 模型网关管理页：管理员可见状态、接入主机与改槽；值守不得进。

import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { hasCode } from '../../api/errors'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../../test/renderApp'

beforeEach(() => {
  sessionStorage.clear()
})

describe('模型网关页', () => {
  it('管理员看到服务状态、接入主机数与近窗成功率', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client)
    const gw = await client.getModelGateway()
    renderApp({ route: '/model', client })

    const metrics = await screen.findByRole('region', { name: '模型网关状态' })
    expect(metrics).toHaveClass('model-gateway-metrics')
    expect(metrics.querySelectorAll('.model-gateway-metric')).toHaveLength(4)
    expect(within(metrics).getByText('接入主机')).toBeInTheDocument()
    expect(within(metrics).getByText(String(gw.providerCount))).toBeInTheDocument()
    expect(within(metrics).getByText('100.0%')).toBeInTheDocument()
    expect(within(metrics).getByText('正常')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: '接入主机' })).toBeInTheDocument()
    expect(screen.getByText('api.x.ai')).toBeInTheDocument()
    const providerScroll = screen.getByRole('region', { name: '接入主机表格，可横向滚动' })
    expect(providerScroll).toHaveAttribute('tabindex', '0')
    fireEvent.keyDown(providerScroll, { key: 'ArrowRight' })
    expect(providerScroll.scrollLeft).toBe(96)
    fireEvent.keyDown(providerScroll, { key: 'ArrowLeft' })
    expect(providerScroll.scrollLeft).toBe(0)
    expect(screen.getByLabelText('模型端点')).toHaveValue(gw.baseUrl)
    expect(screen.getAllByText('OpenAI Chat Completions').length).toBeGreaterThan(0)
    expect(screen.getByLabelText('模型密钥')).toHaveValue('')
    expect(screen.getByRole('group', { name: '模型网关操作' })).toBeInTheDocument()
    expect(screen.getByLabelText('模型端点').closest('.model-config-field')).toHaveClass('model-config-field--full')
    expect(screen.getByLabelText('模型名').closest('.model-config-field')).not.toHaveClass('model-config-field--full')
    expect(screen.getByLabelText('模型密钥').closest('.model-config-field')).toHaveClass('model-config-field--full')
    expect(metrics.querySelector('time')).toHaveAttribute('datetime', gw.lastCallAt)
  })

  it('空密钥保存保留旧钥；改端点后接入主机数计入当前槽', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    const before = await client.getModelGateway()
    renderApp({ route: '/model', client })

    await screen.findByRole('region', { name: '调整配置' })
    const url = screen.getByLabelText('模型端点')
    await user.clear(url)
    await user.type(url, 'https://api.openai.com/v1')
    await user.click(screen.getByRole('button', { name: '保存配置' }))

    await waitFor(async () => {
      const after = await client.getModelGateway()
      expect(after.baseUrl).toBe('https://api.openai.com/v1')
      expect(after.hasSecret).toBe(true)
      expect(after.secretHint).toBe(before.secretHint)
      expect(after.providerCount).toBe(2)
    })
    expect(await screen.findByText('api.openai.com')).toBeInTheDocument()
    expect(screen.getByLabelText('模型密钥')).toHaveValue('')
  })

  it('探测连通记入近窗且不回填密钥', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    const before = await client.getModelGateway()
    renderApp({ route: '/model', client })

    await screen.findByRole('button', { name: '探测连通' })
    await user.click(screen.getByRole('button', { name: '探测连通' }))
    expect(await screen.findByText('探测成功，已记入近窗调用')).toBeInTheDocument()
    const after = await client.getModelGateway()
    expect(Number(after.callsTotal)).toBe(Number(before.callsTotal) + 1)
    expect(screen.getByLabelText('模型密钥')).toHaveValue('')
  })

  it('可显式清除旧钥并保持 HTTP 无 Key 端点可探测', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/model', client })

    const endpoint = await screen.findByLabelText('模型端点')
    await user.clear(endpoint)
    await user.type(endpoint, 'http://model.internal:8000/v1')
    await user.click(screen.getByRole('checkbox', { name: '清除已保存密钥' }))
    await user.click(screen.getByRole('button', { name: '保存配置' }))

    await waitFor(async () => {
      const after = await client.getModelGateway()
      expect(after.baseUrl).toBe('http://model.internal:8000/v1')
      expect(after.hasSecret).toBe(false)
      expect(after.status).not.toBe('MODEL_GATEWAY_STATUS_UNCONFIGURED')
    })
    expect(screen.getByRole('button', { name: '探测连通' })).toBeEnabled()
  })
})

describe('模型网关 接口夹具契约', () => {
  it('非管理员不得读改槽；完成后 PutModelConfig 仍非法', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client, 'operator-chen', 'operator123456')
    await expect(client.getModelGateway()).rejects.toSatisfy((e) => hasCode(e, 'permission_denied'))
    await expect(client.updateModelGateway({ baseUrl: 'https://api.x.ai/v1' })).rejects.toSatisfy((e) =>
      hasCode(e, 'permission_denied'),
    )

    const admin = new ConsoleClientFixture()
    await loginAs(admin)
    await expect(admin.putModelConfig({ baseUrl: 'https://api.x.ai/v1', secret: 'sk-x' })).rejects.toSatisfy((e) =>
      hasCode(e, 'failed_precondition'),
    )
    const gw = await admin.updateModelGateway({
      baseUrl: 'https://api.x.ai/v1',
      model: 'grok-custom',
      dialect: 'MODEL_DIALECT_CLAUDE_MESSAGES',
    })
    expect(gw.model).toBe('grok-custom')
    expect(gw.dialect).toBe('MODEL_DIALECT_CLAUDE_MESSAGES')
    expect(gw.hasSecret).toBe(true)
  })
})
