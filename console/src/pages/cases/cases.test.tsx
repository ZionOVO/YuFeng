import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ListCasesFilter, Page, PageQuery } from '../../api/client'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import type { DefenseModule, InvestigationCase } from '../../api/types'
import { loginAs, renderApp } from '../../test/renderApp'

beforeEach(() => sessionStorage.clear())

describe('案件工作台', () => {
  it('重新读取审批冻结投影，并且批准按钮只消费一次', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/cases?caseId=case_traffic_01', client })

    expect(await screen.findByRole('region', { name: '案件工作台' })).toBeInTheDocument()
    expect(await screen.findByText('api.x.ai / grok-4-1-fast-non-reasoning')).toBeInTheDocument()
    expect(screen.getByText('method、path、query、body')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '批准一次' }))
    await waitFor(() => expect(screen.queryByRole('button', { name: '批准一次' })).toBeNull())
    expect(await screen.findByText('approved')).toBeInTheDocument()
  })

  it('键盘可选择案件并进入共享工作台', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/cases', client })

    const card = await screen.findByRole('button', { name: /搜索接口疑似误报/ })
    card.focus()
    await user.keyboard('{Enter}')
    expect(await screen.findByText('请求形状与近期良性基线一致。')).toBeInTheDocument()
  })

  it('未知模块使用通用案件视图并提示升级客户端', async () => {
    class UnknownModuleClient extends ConsoleClientFixture {
      override async listModules(): Promise<DefenseModule[]> {
        return [{ moduleId: 'future-defense', displayName: '未来防御', version: '1', requiredProducerCapabilities: [], caseActivitySchemas: [], surfaces: [], active: true }]
      }
      override async getCase(caseId: string): Promise<InvestigationCase> {
        return { ...(await super.getCase(caseId)), moduleId: 'future-defense' }
      }
    }
    const client = new UnknownModuleClient()
    await loginAs(client)
    renderApp({ route: '/cases?caseId=case_traffic_01', client })
    expect(await screen.findByText('升级客户端可获得专用视图。', { exact: false })).toBeInTheDocument()
  })

  it('使用服务端游标翻阅全部真实案件而不是截断首批结果', async () => {
    class PagedCaseClient extends ConsoleClientFixture {
      override async listCases(filter: ListCasesFilter = {}, page: PageQuery = {}): Promise<Page<InvestigationCase>> {
        const all = await super.listCases(filter, { pageSize: 200 })
        if (page.pageToken === 'next') return { items: all.items.slice(1, 2), nextPageToken: '' }
        return { items: all.items.slice(0, 1), nextPageToken: all.items.length > 1 ? 'next' : '' }
      }
    }
    const user = userEvent.setup()
    const client = new PagedCaseClient()
    await loginAs(client)
    renderApp({ route: '/cases', client })

    expect(await screen.findByText('结算入口出现未映射请求形状')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '下一页' }))
    expect((await screen.findAllByText('搜索接口疑似误报')).length).toBeGreaterThan(0)
    expect(screen.getByText('第 2 页')).toBeInTheDocument()
  })

  it('人工反馈、解决和重新打开均调用真实案件服务并刷新终态', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/cases?caseId=case_traffic_01', client })

    await screen.findByRole('region', { name: '案件人工处置' })
    await user.type(screen.getByLabelText('案件处置说明'), '已与业务负责人核对')
    await user.click(screen.getByRole('button', { name: '记录反馈' }))
    expect(await screen.findByText('人工反馈已记录')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '解决案件' }))
    expect(await screen.findByRole('button', { name: '重新打开' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '重新打开' }))
    expect(await screen.findByRole('button', { name: '解决案件' })).toBeInTheDocument()
  })

  it('直达越出当前资产 Bindings 的案件显示无权限状态', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client, 'viewer-li', 'viewer123456')
    renderApp({ route: '/cases?caseId=case_traffic_02', client })

    expect(await screen.findByText('案件不在当前授权资产范围内')).toBeInTheDocument()
    expect(screen.getByText('没有权限')).toBeInTheDocument()
    expect(screen.queryByText('搜索接口疑似误报')).toBeNull()
  })
})
