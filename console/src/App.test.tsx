// 关键路由直刷测试：已登录（admin）直接以 initialEntries 进入各页面，断言页面锚点渲染。
// 另覆盖 404 兜底路由与未登录直刷受保护页的重定向。

import { screen } from '@testing-library/react'
import { FIXTURE_BLOCK_EVENT_ID } from './test/fixtures/data'
import { ConsoleClientFixture } from './test/fixtures/consoleClient'
import { loginAs, renderApp } from './test/renderApp'

// 同一文件内的用例共享 jsdom，sessionStorage 不会自动隔离，统一在每个用例前清空
beforeEach(() => {
  sessionStorage.clear()
})

/** admin 登录（写入 sessionStorage）后直刷目标路由。 */
async function renderAsAdmin(route: string) {
  const client = new ConsoleClientFixture()
  await loginAs(client)
  return renderApp({ route, client })
}

describe('关键路由直刷', () => {
  it('/dashboard 仪表盘', async () => {
    await renderAsAdmin('/dashboard')
    expect(await screen.findByText('资产总数')).toBeInTheDocument()
    expect(await screen.findByRole('region', { name: '资产拓扑' })).toBeInTheDocument()
  })

  it('/assets 资产台账', async () => {
    await renderAsAdmin('/assets')
    expect(await screen.findByRole('region', { name: '资产台账' })).toBeInTheDocument()
  })

  it('/assets/asset-01 资产详情', async () => {
    await renderAsAdmin('/assets/asset-01')
    expect(await screen.findByRole('region', { name: '资产元数据' })).toBeInTheDocument()
  })

  it('/events 事件流', async () => {
    await renderAsAdmin('/events')
    // 事件列表的 section 无 aria-label，表格有：aria-label="事件列表"
    expect(await screen.findByLabelText('事件列表')).toBeInTheDocument()
  })

  it('/events/:eventId 事件详情', async () => {
    await renderAsAdmin(`/events/${FIXTURE_BLOCK_EVENT_ID}`)
    expect(await screen.findByRole('region', { name: '事件概要' })).toBeInTheDocument()
  })

  it('/releases 发布治理', async () => {
    await renderAsAdmin('/releases')
    expect(await screen.findByRole('region', { name: '防护策略列表' })).toBeInTheDocument()
  })

  it('/releases/rel_01J8VN8P 防护策略详情', async () => {
    await renderAsAdmin('/releases/rel_01J8VN8P')
    // “防护策略概要”面板是 div[aria-label] 而非 section，无 region 角色，按标签文本查
    expect(await screen.findByLabelText('防护策略概要')).toBeInTheDocument()
  })

  it('/audit 审计链', async () => {
    await renderAsAdmin('/audit')
    expect(await screen.findByRole('region', { name: '链段校验' })).toBeInTheDocument()
  })

  it('/agent 编排场会话', async () => {
    await renderAsAdmin('/agent')
    expect(await screen.findByRole('region', { name: '智能体会话' })).toBeInTheDocument()
    expect(await screen.findByRole('region', { name: '资产拓扑' })).toBeInTheDocument()
  })

  it('/cases 案件工作台', async () => {
    await renderAsAdmin('/cases')
    expect(await screen.findByRole('region', { name: '案件工作台' })).toBeInTheDocument()
  })

  it('/tools 不再作为独立产品页面', async () => {
    await renderAsAdmin('/tools')
    expect(await screen.findByText('页面不存在')).toBeInTheDocument()
  })

  it('/users 用户管理（admin）', async () => {
    await renderAsAdmin('/users')
    expect(await screen.findByRole('region', { name: '用户列表' })).toBeInTheDocument()
  })

  it('/model 模型网关（admin）', async () => {
    await renderAsAdmin('/model')
    expect(await screen.findByRole('region', { name: '模型网关状态' })).toBeInTheDocument()
    expect(await screen.findByRole('region', { name: '调整配置' })).toBeInTheDocument()
  })

  it('/no-such-route 落到 404 页', async () => {
    await renderAsAdmin('/no-such-route')
    expect(await screen.findByText('页面不存在')).toBeInTheDocument()
  })

  it('/gallery 不进入交付路由', async () => {
    await renderAsAdmin('/gallery')
    expect(await screen.findByText('页面不存在')).toBeInTheDocument()
  })

  it('未登录直刷 /dashboard 重定向到登录页', async () => {
    renderApp({ route: '/dashboard' })
    expect(await screen.findByRole('button', { name: '登录' })).toBeInTheDocument()
    expect(screen.getByText('御锋控制台')).toBeInTheDocument()
  })
})
