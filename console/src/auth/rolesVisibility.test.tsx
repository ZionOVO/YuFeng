// 角色可见性测试：viewer / operator / admin 三档角色对侧边栏入口、用户管理页守卫
// 与防护策略详情写操作区的 UX 控制（工具、Bindings 与服务端角色硬门；鉴权本身以服务端为准）。

import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ConsoleClientFixture } from '../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../test/renderApp'

// 同一文件内的用例共享 jsdom，sessionStorage 不会自动隔离，统一在每个用例前清空
beforeEach(() => {
  sessionStorage.clear()
})

/** 用测试夹具账户登录（写入 sessionStorage）后，以同一 client 直刷目标路由。 */
async function renderLoggedIn(route: string, username: string, password: string) {
  const client = new ConsoleClientFixture()
  await loginAs(client, username, password)
  return renderApp({ route, client })
}

const USER_TOOL_DENIED = '此页需要 user.admin 工具权限'
const ADMIN_ROLE_DENIED = '此页仅对管理员角色（USER_ROLE_ADMIN）开放'

describe('viewer（USER_ROLE_VIEWER）', () => {
  it('侧边栏无“用户管理”入口', async () => {
    await renderLoggedIn('/dashboard', 'viewer-li', 'viewer123456')

    expect(await screen.findByText('资产总数')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /用户管理/ })).toBeNull()
    expect(screen.queryByRole('link', { name: /模型网关/ })).toBeNull()
  })

  it('直刷 /users 看到无权限状态', async () => {
    await renderLoggedIn('/users', 'viewer-li', 'viewer123456')

    expect(await screen.findByText(USER_TOOL_DENIED)).toBeInTheDocument()
    expect(screen.getByText('没有权限')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /用户管理/ })).toBeNull()
  })

  it('直刷 /model 看到无权限状态', async () => {
    await renderLoggedIn('/model', 'viewer-li', 'viewer123456')

    expect(await screen.findByText(ADMIN_ROLE_DENIED)).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '调整配置' })).toBeNull()
  })

  it('防护策略详情页不渲染写操作按钮', async () => {
    await renderLoggedIn('/releases/rel_01J8VN8P', 'viewer-li', 'viewer123456')

    // 等待防护策略概要加载完成；rel_01J8VN8P 处于小比例态，可写角色可见“推进全量 / 回滚 / 退休”
    // “防护策略概要”面板是 div[aria-label] 而非 section，无 region 角色，按标签文本查
    expect(await screen.findByLabelText('防护策略概要')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '防护策略操作' })).toBeNull()
    for (const name of ['推进全量', '回滚', '退休']) {
      expect(screen.queryByRole('button', { name })).toBeNull()
    }
  })
})

describe('operator（USER_ROLE_OPERATOR）', () => {
  it('提案人没有 promote 工具时不渲染推进/回滚', async () => {
    await renderLoggedIn('/releases/rel_01J8VN8P', 'operator-chen', 'operator123456')

    expect(await screen.findByLabelText('防护策略概要')).toBeInTheDocument()
    for (const name of ['推进全量', '回滚', '退休']) {
      expect(screen.queryByRole('button', { name })).toBeNull()
    }
  })

  it('持 promote 授予的用户看到推进与回滚', async () => {
    await renderLoggedIn('/releases/rel_01J8VN8P', 'promoter-wu', 'promoter123456')

    expect(await screen.findByRole('button', { name: '推进全量' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '回滚' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '退休' })).toBeInTheDocument()
  })

  it('侧边栏仍无“用户管理”入口', async () => {
    await renderLoggedIn('/dashboard', 'operator-chen', 'operator123456')

    expect(await screen.findByText('资产总数')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /用户管理/ })).toBeNull()
    expect(screen.queryByRole('link', { name: /模型网关/ })).toBeNull()
  })

  it('直刷 /users 看到无权限状态', async () => {
    await renderLoggedIn('/users', 'operator-chen', 'operator123456')

    expect(await screen.findByText(USER_TOOL_DENIED)).toBeInTheDocument()
    expect(screen.getByText('没有权限')).toBeInTheDocument()
  })
})

describe('admin（USER_ROLE_ADMIN）', () => {
  it('系统设置折叠用户管理和模型网关', async () => {
    const user = userEvent.setup()
    await renderLoggedIn('/dashboard', 'admin', 'admin123456')

    expect(await screen.findByText('资产总数')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /用户管理/ })).toBeNull()
    expect(screen.queryByRole('link', { name: /模型网关/ })).toBeNull()
    await user.click(screen.getByRole('button', { name: '系统设置' }))
    expect(screen.getByRole('link', { name: /用户管理/ })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /模型网关/ })).toBeInTheDocument()
  })

	 it('安全运营和记录追溯分别折叠同类入口', async () => {
    const user = userEvent.setup()
    await renderLoggedIn('/dashboard', 'admin', 'admin123456')

    expect(await screen.findByText('资产总数')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Agent 管理' })).toBeNull()
	await user.click(screen.getByRole('button', { name: '安全运营' }))
    expect(screen.getByRole('link', { name: '资产台账' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Agent 管理' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '防护策略' })).toBeInTheDocument()
	expect(screen.getByRole('link', { name: '案件' })).toBeInTheDocument()

    expect(screen.queryByRole('link', { name: '安全事件' })).toBeNull()
    await user.click(screen.getByRole('button', { name: '记录追溯' }))
    expect(screen.getByRole('link', { name: '安全事件' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '操作审计' })).toBeInTheDocument()
  })

  it('/users 正常渲染用户列表', async () => {
    await renderLoggedIn('/users', 'admin', 'admin123456')

    expect(await screen.findByRole('region', { name: '用户列表' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '新建用户' })).toBeInTheDocument()
  })

  it('/model 正常渲染模型网关', async () => {
    await renderLoggedIn('/model', 'admin', 'admin123456')

    expect(await screen.findByRole('region', { name: '模型网关状态' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '保存配置' })).toBeInTheDocument()
  })

  it('角色为管理员但无 user.admin 时仍可看模型和 Worker，不展示用户管理', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    const scopedAdmin = await client.createUser({
      username: 'worker-admin',
      password: 'worker-admin-123456',
      displayName: 'Worker 管理员',
      role: 'USER_ROLE_ADMIN',
    })
    await client.putGrant({
      subjectUserId: scopedAdmin.userId,
      tools: ['console.read', 'worker.enroll'],
      bindings: [{ kind: 'asset', id: 'asset-01' }],
    })
    await loginAs(client, 'worker-admin', 'worker-admin-123456')
    renderApp({ route: '/dashboard', client })

    expect(await screen.findByText('资产总数')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '系统设置' }))
    expect(screen.getByRole('link', { name: '模型网关' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Worker' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: '用户管理' })).toBeNull()

    await user.click(screen.getByRole('link', { name: 'Worker' }))
    expect(await screen.findByRole('region', { name: 'Worker 注册说明' })).toBeInTheDocument()
  })
})

describe('工具授权与角色模板分离', () => {
  it('operator 获授 user.admin 后可管理用户，但不能进入管理员角色专属模型页', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    await client.putGrant({
      subjectUserId: 'usr_02',
      tools: ['console.read', 'user.admin'],
      bindings: [{ kind: 'asset', id: 'asset-01' }],
    })
    await loginAs(client, 'operator-chen', 'operator123456')
    const app = renderApp({ route: '/users', client })

    expect(await screen.findByRole('region', { name: '用户列表' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '用户管理' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: '模型网关' })).toBeNull()

    await user.click(screen.getByRole('link', { name: '用户管理' }))
    expect(screen.queryByText(ADMIN_ROLE_DENIED)).toBeNull()

    app.unmount()
    renderApp({ route: '/model', client })
    expect(await screen.findByText(ADMIN_ROLE_DENIED)).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '调整配置' })).toBeNull()
  })
})
