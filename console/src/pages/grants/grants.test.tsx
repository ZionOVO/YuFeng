// 授予页主体不能是自己；Tools 多选；Bindings 从 ListAssets 勾选；禁止 *；创建用户后留下补授。

import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../../test/renderApp'

beforeEach(() => {
  sessionStorage.clear()
})

describe('授予页', () => {
  it('主体选项不含自己，Bindings 来自 ListAssets，没有 * 输入', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/grants', client })

    expect(await screen.findByRole('region', { name: '授予' })).toBeInTheDocument()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /主体/ }))
    expect(screen.queryByRole('option', { name: /^admin$/ })).toBeNull()
    expect(await screen.findByRole('option', { name: /operator-chen/ })).toBeInTheDocument()
    expect(screen.getAllByLabelText('资产 asset-01').length).toBeGreaterThan(0)
    expect(screen.getAllByLabelText('资产 asset-02').length).toBeGreaterThan(0)
    expect(screen.queryByLabelText('通配绑定')).toBeNull()
    expect(screen.queryByDisplayValue('*')).toBeNull()
    expect(screen.getAllByLabelText('工具 govern.promote_enforce').length).toBeGreaterThan(0)
    expect(screen.getAllByLabelText('工具 govern.propose').length).toBeGreaterThan(0)
  })

  it('不能把自己写成授予主体', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client)
    await expect(
      client.putGrant({
        subjectUserId: 'usr_01',
        tools: ['console.read'],
        bindings: [{ kind: 'asset', id: 'asset-01' }],
      }),
    ).rejects.toMatchObject({ reasonKey: 'grant_self' })
  })

  it('创建用户后停留在授予页并预选该用户', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/users', client })

    await screen.findByRole('region', { name: '用户列表' })
    await user.click(screen.getByRole('button', { name: '新建用户' }))
    await user.type(screen.getByLabelText('用户名'), 'new-ops')
    await user.type(screen.getByLabelText('初始密码'), 'new-ops-123456')
    await user.type(screen.getByLabelText('显示名'), '新运维')
    await user.click(screen.getByRole('button', { name: '创建' }))

    expect(await screen.findByRole('region', { name: '授予' })).toBeInTheDocument()
    expect((await screen.findAllByText(/new-ops/)).length).toBeGreaterThan(0)
    expect((await screen.findAllByText(/补授/)).length).toBeGreaterThan(0)
  })

  it('保存授予后 ListGrants 可见', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/grants?subject=usr_03', client })

    await screen.findByRole('region', { name: '授予' })
    await user.click(screen.getAllByLabelText('工具 govern.promote_enforce')[0])
    await user.click(screen.getAllByLabelText('资产 asset-01')[0])
    await user.click(screen.getByRole('button', { name: '保存授予' }))

    await waitFor(async () => {
      const grants = await client.listGrants('usr_03')
      expect(grants.some((g) => g.tools.includes('govern.promote_enforce'))).toBe(true)
    })
  })

  it('账户级 worker 权限不要求虚构资产 Binding', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/grants?subject=usr_03', client })

    await screen.findByRole('region', { name: '授予' })
    await user.click(screen.getAllByLabelText('工具 worker.enroll')[0])
    const save = screen.getByRole('button', { name: '保存授予' })
    expect(save).toBeEnabled()
    await user.click(save)

    await waitFor(async () => {
      const grants = await client.listGrants('usr_03')
      expect(grants.some((grant) => grant.tools.includes('worker.enroll') && grant.bindings.length === 0)).toBe(true)
    })
  })
})
