// /agent 接 SessionService；纯文本 session.reply 不得画未登记仪器。

import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { encodeSessionTurn } from '../../api/sessionTurn'
import { YfChat } from '../../components/chat/YfChat'
import { loginAs, renderApp } from '../../test/renderApp'

beforeEach(() => {
  sessionStorage.clear()
})

function assertNoFabricatedInstruments() {
  expect(screen.queryByText('event.list')).toBeNull()
  expect(screen.queryByText('run-7c2a')).toBeNull()
  expect(screen.queryByText('待盖印')).toBeNull()
  expect(screen.queryByRole('link', { name: '去防护策略页盖印' })).toBeNull()
  expect(screen.queryByText('思考过程')).toBeNull()
}

describe('会话页', () => {
  it('移动端可在资产拓扑与贾维斯之间切换且会话不丢失', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/agent', client })

    const tabs = await screen.findByRole('tablist', { name: 'Agent 管理视图' })
    const mapTab = within(tabs).getByRole('tab', { name: '资产拓扑' })
    const chatTab = within(tabs).getByRole('tab', { name: '贾维斯' })
    expect(mapTab).toHaveAttribute('aria-selected', 'true')
    expect(chatTab).toHaveAttribute('aria-selected', 'false')

    await user.click(chatTab)
    expect(chatTab).toHaveAttribute('aria-selected', 'true')
    await user.type(screen.getByLabelText('向贾维斯发送消息'), '移动端保持这条会话')
    await user.click(screen.getByRole('button', { name: '发送' }))
    expect(await screen.findByText('移动端保持这条会话')).toBeInTheDocument()

    await user.click(mapTab)
    await user.click(chatTab)
    expect(screen.getByText('移动端保持这条会话')).toBeInTheDocument()
  })

  it('管理员在拓扑台新增并删除流量审查 Agent', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/agent', client })

    expect(await screen.findByText('2 个流量审查岗位 · Jarvis 固定')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '新增 Agent' }))
    const createDialog = await screen.findByRole('dialog')
    await user.type(within(createDialog).getByLabelText('Agent 名称'), '夜间流量审查员')
    await user.click(within(createDialog).getByRole('checkbox', { name: /core-payments/ }))
    await user.click(within(createDialog).getByRole('button', { name: '保存' }))

    expect(await screen.findByText('3 个流量审查岗位 · Jarvis 固定')).toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: '编辑 Agent 夜间流量审查员' }))
    const editDialog = await screen.findByRole('dialog')
    expect(within(editDialog).getByText('编辑 夜间流量审查员')).toBeInTheDocument()
    await user.click(within(editDialog).getByRole('button', { name: '删除 Agent' }))
    await user.click(within(editDialog).getByRole('button', { name: '确认删除' }))
    expect(await screen.findByText('2 个流量审查岗位 · Jarvis 固定')).toBeInTheDocument()
  })

  it('档案只部分落在当前资产范围时只能查看，不能从裁剪后的 bindings 推断可编辑', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    await client.createAgentProfile({
      displayName: '跨资产流量审查员',
      tools: ['case.get', 'case.request_evidence', 'run.create'],
      bindings: [{ kind: 'asset', id: 'asset-01' }, { kind: 'asset', id: 'asset-02' }],
    })
    await client.putGrant({
      subjectUserId: 'usr_03',
      tools: ['console.read', 'agent.manage'],
      bindings: [{ kind: 'asset', id: 'asset-01' }],
    })
    await loginAs(client, 'viewer-li', 'viewer123456')
    renderApp({ route: '/agent', client })

    const readOnlyProfile = await screen.findByRole('button', { name: '查看 Agent 跨资产流量审查员' })
    expect(screen.queryByRole('button', { name: '编辑 Agent 跨资产流量审查员' })).toBeNull()
    await user.click(readOnlyProfile)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('发送后可见贾维斯纯文本，不画思考折叠或仪器', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/agent', client })

    expect(await screen.findByRole('region', { name: '智能体会话' })).toBeInTheDocument()
    expect(screen.queryByText('能力开发中')).toBeNull()
    const box = await screen.findByLabelText('向贾维斯发送消息')
    await user.type(box, 'hello jarvis')
    await user.click(screen.getByRole('button', { name: '发送' }))

    expect(await screen.findByText('hello jarvis')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getByText(/不会把它当成命令/)).toBeInTheDocument()
    })
    assertNoFabricatedInstruments()
  })

  it('会话 RPC 不查授予：viewer 也能发消息', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client, 'viewer-li', 'viewer123456')
    renderApp({ route: '/agent', client })

    await screen.findByRole('region', { name: '智能体会话' })
    await user.type(await screen.findByLabelText('向贾维斯发送消息'), 'viewer ping')
    await user.click(screen.getByRole('button', { name: '发送' }))
    expect(await screen.findByText('viewer ping')).toBeInTheDocument()
  })

  it('纯文本 session.reply 不画 event.list、run-7c2a 或待盖印', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/agent', client })

    await user.type(await screen.findByLabelText('向贾维斯发送消息'), 'prod-web 的 22 还对公网开着')
    await user.click(screen.getByRole('button', { name: '发送' }))
    expect(await screen.findByText('prod-web 的 22 还对公网开着')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getByText(/不会把它当成命令/)).toBeInTheDocument()
    })
    assertNoFabricatedInstruments()
  })

  it('对话台不把未登记 YF/1 信封画成仪器或盖印卡', () => {
    const envelope = encodeSessionTurn({
      text: '调查回来了。',
      thinking: '先取形状。',
      tools: [{ name: 'event.list', state: 'done', assetId: 'asset-01' }],
      runs: [{ id: 'run-7c2a', role: '调查', state: '已焚' }],
      gate: { title: '关 22', status: 'open', releaseId: 'rel_01J8VN8P' },
    })
    render(
      <YfChat
        mode="stage"
        messages={[{ sequence: '1', sessionId: 'ses', sender: 'jarvis-1', content: envelope, occurredAt: 't' }]}
        selfId="usr_01"
        contextLabel="中台"
        thinking={false}
        busy={false}
        error={null}
        onSend={() => undefined}
      />,
    )
    expect(screen.getByText(/调查回来了/)).toBeInTheDocument()
    expect(screen.queryByText('思考过程')).toBeNull()
    expect(screen.queryByText('待盖印')).toBeNull()
    expect(screen.queryByRole('link', { name: '去防护策略页盖印' })).toBeNull()
  })

  it('引导未完成时 /agent 不可用，管理员只见引导', async () => {
    const client = new ConsoleClientFixture({ onboardingState: 'ONBOARDING_STATE_PENDING' })
    await loginAs(client)
    renderApp({ route: '/agent', client })

    expect(await screen.findByRole('region', { name: '初次配置引导' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '智能体会话' })).toBeNull()
  })
})
