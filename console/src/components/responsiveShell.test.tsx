// 移动壳层行为测试：顶部栏打开完整导航；全局贾维斯使用可关闭对话层并恢复触发点焦点。

import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ConsoleClientFixture } from '../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../test/renderApp'

beforeEach(() => {
  sessionStorage.clear()
  document.body.classList.remove('yf-mobile-menu-open', 'yf-jarvis-open')
})

describe('响应式应用壳', () => {
  it('顶部栏用全屏抽屉承载全部导航和账户入口', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/dashboard', client })

    await screen.findByText('资产总数')
    const topbar = screen.getByRole('banner', { name: '移动顶栏' })
    await user.click(within(topbar).getByRole('button', { name: '打开移动导航' }))
    const navigation = screen.getByRole('dialog', { name: '移动导航' })
    expect(within(navigation).getByRole('link', { name: '仪表盘' })).toBeInTheDocument()
    expect(within(navigation).getByRole('link', { name: '资产台账' })).toBeInTheDocument()
    expect(within(navigation).getByRole('link', { name: 'Agent 管理' })).toBeInTheDocument()
    expect(within(navigation).getByRole('link', { name: '操作审计' })).toBeInTheDocument()
    expect(within(navigation).getByRole('link', { name: '用户管理' })).toBeInTheDocument()
    expect(within(navigation).getByRole('link', { name: 'Worker' })).toBeInTheDocument()
    expect(within(navigation).getByRole('button', { name: '修改密码' })).toBeInTheDocument()
    expect(document.body).toHaveClass('yf-mobile-menu-open')

    const close = within(navigation).getByRole('button', { name: '关闭导航' })
    const logout = within(navigation).getByRole('button', { name: '退出登录' })
    await waitFor(() => expect(close).toHaveFocus())
    await user.tab({ shift: true })
    expect(logout).toHaveFocus()
    await user.tab()
    expect(close).toHaveFocus()

    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog', { name: '移动导航' })).toBeNull()
    expect(document.body).not.toHaveClass('yf-mobile-menu-open')
  })

  it('全局贾维斯以对话层打开并在关闭后恢复触发点焦点', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/dashboard', client })

    await screen.findByText('资产总数')
    const trigger = screen.getByRole('button', { name: '打开贾维斯' })
    await user.click(trigger)

    expect(await screen.findByRole('dialog', { name: '智能体会话' })).toBeInTheDocument()
    expect(document.body).toHaveClass('yf-jarvis-open')
    const dialog = screen.getByRole('dialog', { name: '智能体会话' })
    const input = screen.getByLabelText('向贾维斯发送消息')
    const close = within(dialog).getByRole('button', { name: '收起' })
    expect(input).toHaveFocus()
    await user.tab()
    expect(close).toHaveFocus()
    await user.tab({ shift: true })
    expect(input).toHaveFocus()

    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '智能体会话' })).toBeNull())
    expect(document.body).not.toHaveClass('yf-jarvis-open')
    expect(trigger).toHaveFocus()
  })
})
