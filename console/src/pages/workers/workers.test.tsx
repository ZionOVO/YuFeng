import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '../../api/errors'
import type { ListAssetsFilter, Page, PageQuery } from '../../api/client'
import type { AssetDetail, WorkerEnrollmentRecord, WorkerRecord } from '../../api/types'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../../test/renderApp'

beforeEach(() => sessionStorage.clear())

describe('Worker 注册', () => {
  it('管理员只核对清单，浏览器只显示加密激活引用', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/workers', client })

    expect(await screen.findByText('branch-office-worker', { exact: false })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /批准时绑定的初始资产/ }))
    await user.click(await screen.findByRole('option', { name: 'core-payments' }))
    await user.click(screen.getByRole('button', { name: '批准登记' }))

    expect(await screen.findByText('已生成加密激活包')).toBeInTheDocument()
    expect(screen.getByText('激活包只能由持有本机 X25519 私钥的客户端取得和解密', { exact: false })).toBeInTheDocument()
    expect(document.body.textContent).not.toContain('bootstrapToken')
    expect(document.body.textContent).not.toContain('clientCertificatePem')
    await waitFor(() => expect(screen.queryByRole('button', { name: '批准登记' })).toBeNull())
  })

  it('初次读取失败显示可重试错误，不同时保留永不结束的加载态', async () => {
    class FailedWorkerClient extends ConsoleClientFixture {
      override listWorkers(): ReturnType<ConsoleClientFixture['listWorkers']> {
        return Promise.reject(new ApiError({ code: 'unavailable', message: 'worker pool unavailable', httpStatus: 503 }))
      }
    }

    const client = new FailedWorkerClient()
    await loginAs(client)
    renderApp({ route: '/workers', client })

    expect(await screen.findByText('worker pool unavailable')).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByLabelText('加载中')).toBeNull())
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument()
  })

  it('资产、Worker 与登记申请均沿服务端游标继续加载至末页', async () => {
    class PagedWorkerClient extends ConsoleClientFixture {
      override async listAssets(filter: ListAssetsFilter = {}, page: PageQuery = {}): Promise<Page<AssetDetail>> {
        const all = await super.listAssets(filter)
        return page.pageToken === 'assets-next'
          ? { items: [all.items[1]], nextPageToken: '' }
          : { items: [all.items[0]], nextPageToken: 'assets-next' }
      }

      override async listWorkers(page: PageQuery = {}): Promise<Page<WorkerRecord>> {
        const all = await super.listWorkers()
        const second = { ...all.items[0], workerId: 'agentd-remote', operatingSystem: 'darwin', architecture: 'arm64' }
        return page.pageToken === 'workers-next'
          ? { items: [second], nextPageToken: '' }
          : { items: [all.items[0]], nextPageToken: 'workers-next' }
      }

      override async listWorkerEnrollments(state = '', page: PageQuery = {}): Promise<Page<WorkerEnrollmentRecord>> {
        const all = await super.listWorkerEnrollments(state)
        const second = { ...all.items[0], enrollmentId: 'enroll-external-02', workerId: 'agentd-satellite', hostname: 'satellite-worker' }
        return page.pageToken === 'enrollments-next'
          ? { items: [second], nextPageToken: '' }
          : { items: [all.items[0]], nextPageToken: 'enrollments-next' }
      }
    }

    const user = userEvent.setup()
    const client = new PagedWorkerClient()
    await loginAs(client)
    renderApp({ route: '/workers', client })

    expect(await screen.findByText('core-payments')).toBeInTheDocument()
    expect(screen.queryByText('mall-gateway')).toBeNull()
    expect(screen.getByText('agentd-central', { exact: false })).toBeInTheDocument()
    expect(screen.queryByText('agentd-remote', { exact: false })).toBeNull()
    expect(screen.getByText('branch-office-worker', { exact: false })).toBeInTheDocument()
    expect(screen.queryByText('satellite-worker', { exact: false })).toBeNull()

    await user.click(screen.getByRole('button', { name: '继续加载资产' }))
    expect(await screen.findByText('mall-gateway')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '继续加载 Worker' }))
    expect(await screen.findByText('agentd-remote', { exact: false })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '继续加载登记申请' }))
    expect(await screen.findByText('satellite-worker', { exact: false })).toBeInTheDocument()

    expect(screen.queryByRole('button', { name: '继续加载资产' })).toBeNull()
    expect(screen.queryByRole('button', { name: '继续加载 Worker' })).toBeNull()
    expect(screen.queryByRole('button', { name: '继续加载登记申请' })).toBeNull()
  })
})
