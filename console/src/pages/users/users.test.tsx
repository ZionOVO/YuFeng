import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '../../api/errors'
import type { ListUsersFilter, Page, PageQuery } from '../../api/client'
import type { User } from '../../api/types'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../../test/renderApp'

beforeEach(() => sessionStorage.clear())

async function chooseUserAction(user: ReturnType<typeof userEvent.setup>, username: string, action: '编辑' | '重置密码' | '删除') {
  const row = (await screen.findByText(username)).closest('tr')
  expect(row).not.toBeNull()
  await user.click(within(row!).getByRole('button', { name: '操作' }))
  await user.click(await screen.findByRole('menuitem', { name: action }))
}

describe('用户管理写操作与分页', () => {
  it('创建重复用户名时保留弹窗并展示稳定的领域错误', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/users', client })

    await screen.findByRole('region', { name: '用户列表' })
    await user.click(screen.getByRole('button', { name: '新建用户' }))
    expect(screen.getByRole('button', { name: '创建' })).toBeDisabled()
    await user.type(screen.getByLabelText('用户名'), 'admin')
    await user.type(screen.getByLabelText('初始密码'), 'different-secret')
    await user.type(screen.getByLabelText('显示名'), '重复管理员')
    await user.click(screen.getByRole('button', { name: '创建' }))

    expect(await screen.findByText('用户名已存在')).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '取消' }))
    await user.click(screen.getByRole('button', { name: '新建用户' }))
    expect(screen.getByLabelText('用户名')).toHaveValue('')
    expect(screen.getByLabelText('初始密码')).toHaveValue('')
    expect(screen.getByLabelText('显示名')).toHaveValue('')
  })

  it('编辑只提交变化字段，重置密码固定吊销全部旧会话', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    const update = vi.spyOn(client, 'updateUser')
    const reset = vi.spyOn(client, 'adminResetPassword')
    await loginAs(client)
    renderApp({ route: '/users', client })

    await chooseUserAction(user, 'operator-chen', '编辑')
    const editDialog = screen.getByRole('dialog')
    const displayName = within(editDialog).getByRole('textbox', { name: '显示名' })
    fireEvent.change(displayName, { target: { value: '陈值班' } })
    await user.click(within(editDialog).getByRole('button', { name: '保存' }))
    await waitFor(() => expect(update).toHaveBeenCalledWith('usr_02', { displayName: '陈值班' }))
    expect(await screen.findByText('陈值班')).toBeInTheDocument()

    await chooseUserAction(user, 'viewer-li', '重置密码')
    const resetDialog = screen.getByRole('dialog')
    expect(within(resetDialog).getByRole('button', { name: '确认重置' })).toBeDisabled()
    fireEvent.change(within(resetDialog).getByLabelText('新密码'), { target: { value: 'viewer-new-secret' } })
    await user.click(within(resetDialog).getByRole('button', { name: '确认重置' }))
    expect(await screen.findByText('已重置')).toBeInTheDocument()
    expect(reset).toHaveBeenCalledWith('usr_03', 'viewer-new-secret', true)
  })

  it('编辑提交发生非接口异常时显示通用错误且不关闭弹窗', async () => {
    class FailedUpdateClient extends ConsoleClientFixture {
      override async updateUser(): Promise<User> {
        throw new Error('socket closed')
      }
    }

    const user = userEvent.setup()
    const client = new FailedUpdateClient()
    await loginAs(client)
    renderApp({ route: '/users', client })

    await chooseUserAction(user, 'operator-chen', '编辑')
    const dialog = screen.getByRole('dialog')
    const displayName = within(dialog).getByRole('textbox', { name: '显示名' })
    fireEvent.change(displayName, { target: { value: '不会保存' } })
    await user.click(within(dialog).getByRole('button', { name: '保存' }))

    expect(await screen.findByText('保存失败，请重试')).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('服务端拒绝删除最后一个在用管理员，弹窗保留并展示失败前置条件', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/users', client })

    await chooseUserAction(user, 'admin', '删除')
    await user.click(screen.getByRole('button', { name: '确认删除' }))

    expect(await screen.findByText('cannot delete the last active admin（failed_precondition）')).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect((await client.getUser('usr_01')).state).toBe('USER_STATE_ACTIVE')
  })

  it('非管理员用户软删除成功后保留记录并展示已删除状态', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/users', client })

    await chooseUserAction(user, 'temp-ops', '删除')
    await user.click(screen.getByRole('button', { name: '确认删除' }))

    await waitFor(async () => expect((await client.getUser('usr_04')).state).toBe('USER_STATE_DELETED'))
    const row = (await screen.findByText('temp-ops')).closest('tr')
    expect(row).not.toBeNull()
    expect(within(row!).getByText('已删除')).toBeInTheDocument()
  })

  it('只回传服务端游标，前后翻页及筛选变化都回到首批', async () => {
    class PagedUsersClient extends ConsoleClientFixture {
      readonly requests: Array<{ filter: ListUsersFilter; page: PageQuery }> = []

      override async listUsers(filter: ListUsersFilter = {}, page: PageQuery = {}): Promise<Page<User>> {
        this.requests.push({ filter: { ...filter }, page: { ...page } })
        return super.listUsers(filter, { ...page, pageSize: 2 })
      }
    }

    const user = userEvent.setup()
    const client = new PagedUsersClient()
    await loginAs(client)
    renderApp({ route: '/users', client })

    expect(await screen.findByText('operator-chen')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByText('viewer-li')).toBeInTheDocument()
    expect(client.requests.some(({ page }) => page.pageToken !== undefined && page.pageToken !== '')).toBe(true)

    await user.click(screen.getByRole('button', { name: '上一页' }))
    expect(await screen.findByText('admin')).toBeInTheDocument()
    await user.type(screen.getByLabelText('搜索用户'), 'viewer-li')
    await waitFor(() => {
      expect(client.requests.at(-1)).toMatchObject({ filter: { query: 'viewer-li' }, page: { pageToken: '' } })
    })
    expect(await screen.findByText('viewer-li')).toBeInTheDocument()
    expect(screen.queryByText('admin')).toBeNull()
  })

  it.each([
    ['permission_denied', '没有权限'],
    ['unavailable', 'user service unavailable'],
  ] as const)('列表读取 %s 时呈现对应状态', async (code, expected) => {
    class FailedListClient extends ConsoleClientFixture {
      override async listUsers(): Promise<Page<User>> {
        throw new ApiError({ code, message: 'user service unavailable', httpStatus: code === 'permission_denied' ? 403 : 503 })
      }
    }

    const client = new FailedListClient()
    await loginAs(client)
    renderApp({ route: '/users', client })
    expect(await screen.findByText(expected)).toBeInTheDocument()
  })
})
