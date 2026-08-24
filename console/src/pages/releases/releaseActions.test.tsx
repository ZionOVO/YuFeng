// 防护策略详情页状态机操作区测试：各状态只渲染合法操作按钮（非法状态的操作编译期不渲染），
// 推进成功/门槛失败/viewer 只读的页面反馈。数据来自 ConsoleClientFixture 确定性数据集（test/fixtures/data.ts）。

import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../../test/renderApp'

/** 指定账户登录并渲染防护策略详情页。门禁/影子用 admin；推进/回滚用 promoter-wu（docs/api.md §17.4.1）。 */
async function renderReleaseAs(
  releaseId: string,
  username = 'admin',
  password = 'admin123456',
): Promise<HTMLElement> {
  const client = new ConsoleClientFixture()
  await loginAs(client, username, password)
  renderApp({ route: `/releases/${releaseId}`, client })
  return screen.findByLabelText('防护策略概要')
}

async function renderReleaseAsAdmin(releaseId: string): Promise<HTMLElement> {
  return renderReleaseAs(releaseId)
}

async function renderReleaseAsPromoter(releaseId: string): Promise<HTMLElement> {
  return renderReleaseAs(releaseId, 'promoter-wu', 'promoter123456')
}

/** 操作区按钮集合断言：present 全部存在、absent 全部不存在。 */
async function expectActionButtons(present: string[], absent: string[]): Promise<void> {
  const actions = await screen.findByLabelText('防护策略操作')
  for (const name of present) {
    expect(within(actions).getByRole('button', { name })).toBeInTheDocument()
  }
  for (const name of absent) {
    expect(within(actions).queryByRole('button', { name })).toBeNull()
  }
}

beforeEach(() => {
  sessionStorage.clear()
})

describe('发布状态机操作区（按状态渲染合法操作）', () => {
  it('draft：只有执行门禁，无推进/回滚/退休', async () => {
    await renderReleaseAsAdmin('rel_01J8ZTB4')
    await expectActionButtons(['执行门禁'], ['启动影子', '推进小比例', '推进全量', '回滚', '退休'])
  })

  it('signed：只有启动影子', async () => {
    await renderReleaseAsAdmin('rel_01J8YPC9')
    await expectActionButtons(['启动影子'], ['执行门禁', '推进小比例', '推进全量', '回滚', '退休'])
  })

  it('shadow：推进小比例 + 推进全量（单机直达）+ 回滚 + 退休', async () => {
    await renderReleaseAsPromoter('rel_01J90N2Q')
    await expectActionButtons(['推进小比例', '推进全量', '回滚', '退休'], ['执行门禁', '启动影子'])
  })

  it('canary：推进全量 + 回滚 + 退休', async () => {
    await renderReleaseAsPromoter('rel_01J8VN8P')
    await expectActionButtons(['推进全量', '回滚', '退休'], ['执行门禁', '启动影子', '推进小比例'])
  })

  it('enforce：回滚 + 退休，无任何推进', async () => {
    await renderReleaseAsPromoter('rel_01J8SQGT')
    await expectActionButtons(['回滚', '退休'], ['执行门禁', '启动影子', '推进小比例', '推进全量'])
  })

  it('retired：无任何操作按钮', async () => {
    await renderReleaseAsAdmin('rel_01J90K6F')
    const actions = await screen.findByLabelText('防护策略操作')
    expect(within(actions).getByText('防护策略已退休，无可用操作')).toBeInTheDocument()
    expect(within(actions).queryByRole('button')).toBeNull()
  })
})

describe('发布推进交互', () => {
  it('shadow 推进小比例：确认弹窗确认后状态变为 canary', async () => {
    const user = userEvent.setup()
    const summary = await renderReleaseAsPromoter('rel_01J90N2Q')
    expect(within(summary).getByText('影子')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '推进小比例' }))
    // 确认弹窗在 portal 中；缺省灰度 5% 合法，直接点确认「推进」
    await user.click(await screen.findByRole('button', { name: '推进' }))

    // 概要状态徽章变为小比例（影子徽章消失），操作区切换为 canary 的合法操作
    await waitFor(() => {
      expect(within(screen.getByLabelText('防护策略概要')).getByText('小比例')).toBeInTheDocument()
    })
    expect(within(screen.getByLabelText('防护策略概要')).queryByText('影子')).toBeNull()
    const actions = screen.getByLabelText('防护策略操作')
    expect(within(actions).getByRole('button', { name: '推进全量' })).toBeInTheDocument()
    expect(within(actions).queryByRole('button', { name: '推进小比例' })).toBeNull()
  })

  it('shadow 推进门槛失败：提示 + gateChecks 逐项可见，状态留在影子', async () => {
    const user = userEvent.setup()
    await renderReleaseAsPromoter('rel_01J8XH7D')

    await user.click(screen.getByRole('button', { name: '推进小比例' }))
    await user.click(await screen.findByRole('button', { name: '推进' }))

    // rel_01J8XH7D 影子请求数不足：failed_precondition + GateChecksView 逐项展示
    expect(await screen.findByText(/推进门槛未满足/)).toBeInTheDocument()
    expect(screen.getByText('shadow_min_requests')).toBeInTheDocument()
    expect(screen.getByText(/影子阶段请求数不足/)).toBeInTheDocument()
    expect(within(screen.getByLabelText('防护策略概要')).getByText('影子')).toBeInTheDocument()
  })

  it('canary 推进全量门槛失败：误报举报检查项可见，状态留在小比例', async () => {
    const user = userEvent.setup()
    await renderReleaseAsPromoter('rel_01J8W33K')

    await user.click(screen.getByRole('button', { name: '推进全量' }))
    // ConfirmDialog 的确认按钮文案同为「推进」
    await user.click(await screen.findByRole('button', { name: '推进' }))

    // rel_01J8W33K 有 1 条拒绝反馈：deny_feedback_total 检查项不通过
    expect(await screen.findByText(/推进门槛未满足/)).toBeInTheDocument()
    expect(screen.getByText('deny_feedback_total')).toBeInTheDocument()
    expect(screen.getByText(/存在未处理的误报举报/)).toBeInTheDocument()
    expect(within(screen.getByLabelText('防护策略概要')).getByText('小比例')).toBeInTheDocument()
  })
})

describe('写按钮 canOnAsset 与提案人', () => {
  it('持 promote 工具但 Bindings 不含该资产时不渲染推进按钮', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client)
    await client.putGrant({
      subjectUserId: 'usr_05',
      tools: ['console.read', 'govern.promote_canary', 'govern.promote_enforce', 'govern.rollback', 'govern.retire'],
      bindings: [{ kind: 'asset', id: 'asset-02' }],
    })
    await loginAs(client, 'promoter-wu', 'promoter123456')
    renderApp({ route: '/releases/rel_01J90N2Q', client })

    // Bindings 不含该发布资产：读路径 permission_denied，写按钮同样不可见
    expect(await screen.findByText('没有权限')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '推进小比例' })).toBeNull()
    expect(screen.queryByRole('button', { name: '推进全量' })).toBeNull()
  })

  it('提案人看到推进按钮禁用并旁注须由其他人推进', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client)
    await client.putGrant({
      subjectUserId: 'usr_02',
      tools: ['console.read', 'govern.promote_canary', 'govern.promote_enforce', 'govern.rollback', 'govern.retire'],
      bindings: [
        { kind: 'asset', id: 'asset-01' },
        { kind: 'asset', id: 'asset-02' },
      ],
    })
    await loginAs(client, 'operator-chen', 'operator123456')
    renderApp({ route: '/releases/rel_01J90N2Q', client })

    const actions = await screen.findByLabelText('防护策略操作')
    expect(within(actions).getByRole('button', { name: '推进小比例' })).toBeDisabled()
    expect(within(actions).getByText(/须由其他持权用户推进/)).toBeInTheDocument()
  })
})

describe('角色控制（viewer 只读）', () => {
  it('viewer 访问影子防护策略详情：不渲染操作区', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client, 'viewer-li', 'viewer123456')
    renderApp({ route: '/releases/rel_01J90N2Q', client })

    await screen.findByLabelText('防护策略概要')
    expect(screen.queryByLabelText('防护策略操作')).toBeNull()
    expect(screen.queryByRole('button', { name: '推进小比例' })).toBeNull()
    expect(screen.queryByRole('button', { name: '回滚' })).toBeNull()
    expect(screen.queryByRole('button', { name: '退休' })).toBeNull()
  })
})
