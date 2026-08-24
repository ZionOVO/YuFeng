import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '../../api/errors'
import type { ListAuditFilter, Page, PageQuery } from '../../api/client'
import type { AuditEntry, ChainVerification } from '../../api/types'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../../test/renderApp'

beforeEach(() => sessionStorage.clear())

describe('审计链校验与游标', () => {
  it('展示链段校验回执，非法区间保留服务端错误码', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/audit', client })

    const verify = await screen.findByRole('region', { name: '链段校验' })
    const start = within(verify).getByRole('textbox', { name: '起始序号' })
    const end = within(verify).getByRole('textbox', { name: '结束序号' })
    const submit = within(verify).getByRole('button', { name: '校验链段' })
    expect(submit).toBeDisabled()

    fireEvent.change(start, { target: { value: '1' } })
    fireEvent.change(end, { target: { value: '3' } })
    await user.click(submit)
    expect(await within(verify).findByText('校验通过')).toBeInTheDocument()
    expect(within(verify).getByText('已校验 3 条')).toBeInTheDocument()
    expect(within(verify).getByText(/^\u8d77\u70b9 /)).toHaveAttribute('title')
    expect(within(verify).getByText(/^\u7ec8\u70b9 /)).toHaveAttribute('title')

    fireEvent.change(start, { target: { value: '3' } })
    fireEvent.change(end, { target: { value: '2' } })
    await user.click(submit)
    expect(await within(verify).findByText('校验请求失败：invalid sequence range（invalid_argument）')).toBeInTheDocument()
    expect(within(verify).queryByText('校验通过')).toBeNull()
  })

  it('链断裂时显式呈现失败结果', async () => {
    class BrokenChainClient extends ConsoleClientFixture {
      override async verifyChain(): Promise<ChainVerification> {
        return { valid: false, startHash: 'start', endHash: 'end', entriesChecked: 2 }
      }
    }

    const user = userEvent.setup()
    const client = new BrokenChainClient()
    await loginAs(client)
    renderApp({ route: '/audit', client })

    const verify = await screen.findByRole('region', { name: '链段校验' })
    fireEvent.change(within(verify).getByRole('textbox', { name: '起始序号' }), { target: { value: '1' } })
    fireEvent.change(within(verify).getByRole('textbox', { name: '结束序号' }), { target: { value: '2' } })
    await user.click(within(verify).getByRole('button', { name: '校验链段' }))
    expect(await within(verify).findByText('校验失败')).toBeInTheDocument()
  })

  it('翻页只回传不透明游标，筛选变化清空旧游标链', async () => {
    class PagedAuditClient extends ConsoleClientFixture {
      readonly requests: Array<{ filter: ListAuditFilter; page: PageQuery }> = []

      override async listAuditEntries(filter: ListAuditFilter = {}, page: PageQuery = {}): Promise<Page<AuditEntry>> {
        this.requests.push({ filter: { ...filter }, page: { ...page } })
        return super.listAuditEntries(filter, { ...page, pageSize: 4 })
      }
    }

    const user = userEvent.setup()
    const client = new PagedAuditClient()
    await loginAs(client)
    renderApp({ route: '/audit', client })

    await screen.findByLabelText('审计条目')
    await user.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByText('第 2 页')).toBeInTheDocument()
    expect(client.requests.some(({ page }) => page.pageToken !== undefined && page.pageToken !== '')).toBe(true)
    await user.click(screen.getByRole('button', { name: '上一页' }))
    expect(await screen.findByText('第 1 页')).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('按对象标识筛选'), { target: { value: 'asset-01' } })
    fireEvent.change(screen.getByLabelText('按操作者筛选'), { target: { value: 'operator-chen' } })
    await waitFor(() => {
      expect(client.requests.at(-1)).toMatchObject({
        filter: { objectId: 'asset-01', actor: 'operator-chen' },
        page: { pageToken: '' },
      })
    })
  })

  it.each([
    ['permission_denied', '没有权限'],
    ['unavailable', 'audit service unavailable'],
  ] as const)('审计列表 %s 时呈现对应状态', async (code, expected) => {
    class FailedAuditClient extends ConsoleClientFixture {
      override async listAuditEntries(): Promise<Page<AuditEntry>> {
        throw new ApiError({ code, message: 'audit service unavailable', httpStatus: code === 'permission_denied' ? 403 : 503 })
      }
    }

    const client = new FailedAuditClient()
    await loginAs(client)
    renderApp({ route: '/audit', client })
    expect(await screen.findByText(expected)).toBeInTheDocument()
  })
})
