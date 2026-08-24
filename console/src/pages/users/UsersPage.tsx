// 用户管理页（路由层按 user.admin 工具守卫，页内不重复判断）。
// 筛选 + 不透明游标分页；新建 / 编辑 / 重置密码 / 软删除四个写操作各自弹窗，
// 弹窗内错误统一为红色小字：ApiError 展示 message（code），其余给通用文案。

import { useMemo, useState } from 'react'
import {
  Button,
  Dropdown,
  DropdownItem,
  DropdownMenu,
  DropdownTrigger,
  Input,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
  Select,
  SelectItem,
  Table,
  TableBody,
  TableCell,
  TableColumn,
  TableHeader,
  TableRow,
} from '@heroui/react'
import { ChevronDown, Plus, Search } from 'lucide-react'
import type { ListUsersFilter } from '../../api/client'
import { hasCode, isApiError } from '../../api/errors'
import type { User, UserPatch, UserRole, UserState } from '../../api/types'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '../../auth/useAuth'
import { useAsyncData } from '../../components/useAsyncData'
import { formatTime } from '../../components/format'
import { Badge, ConfirmDialog, StateView, TokenPager } from '../../components/ui'

const PAGE_SIZE = 25

type BadgeTone = 'green' | 'amber' | 'red' | 'mute'

/** 角色徽章与筛选项用契约枚举短名，便于与 proto / 审计动作对照。 */
const ROLE_OPTIONS: { value: UserRole; label: string }[] = [
  { value: 'USER_ROLE_ADMIN', label: 'ADMIN' },
  { value: 'USER_ROLE_OPERATOR', label: 'OPERATOR' },
  { value: 'USER_ROLE_VIEWER', label: 'VIEWER' },
]

const ROLE_BADGE: Record<UserRole, { label: string; tone: BadgeTone }> = {
  USER_ROLE_UNSPECIFIED: { label: '未知', tone: 'mute' },
  USER_ROLE_ADMIN: { label: 'ADMIN', tone: 'amber' },
  USER_ROLE_OPERATOR: { label: 'OPERATOR', tone: 'green' },
  USER_ROLE_VIEWER: { label: 'VIEWER', tone: 'mute' },
}

const STATE_OPTIONS: { value: UserState; label: string }[] = [
  { value: 'USER_STATE_ACTIVE', label: '正常' },
  { value: 'USER_STATE_DISABLED', label: '禁用' },
  { value: 'USER_STATE_DELETED', label: '已删除' },
]

const STATE_BADGE: Record<UserState, { label: string; tone: BadgeTone }> = {
  USER_STATE_UNSPECIFIED: { label: '未知', tone: 'mute' },
  USER_STATE_ACTIVE: { label: '正常', tone: 'green' },
  USER_STATE_DISABLED: { label: '禁用', tone: 'amber' },
  USER_STATE_DELETED: { label: '已删除', tone: 'red' },
}

/** 弹窗内错误文案：ApiError 带 code，非 ApiError 给通用文案。 */
function apiErrorText(e: unknown, fallback: string): string {
  return isApiError(e) ? `${e.message}（${e.code}）` : fallback
}

/* ---------- 新建用户 ---------- */

function CreateUserDialog({
  open,
  onClose,
  onDone,
}: {
  open: boolean
  onClose: () => void
  onDone: (created: User) => void
}) {
  const { client } = useAuth()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [displayName, setDisplayName] = useState('')
  // 新建默认最小权限角色
  const [role, setRole] = useState<UserRole>('USER_ROLE_VIEWER')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const close = () => {
    setUsername('')
    setPassword('')
    setDisplayName('')
    setRole('USER_ROLE_VIEWER')
    setError(null)
    onClose()
  }

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      const created = await client.createUser({ username: username.trim(), password, displayName: displayName.trim(), role })
      onDone(created)
      close()
    } catch (e) {
      setError(hasCode(e, 'already_exists') ? '用户名已存在' : apiErrorText(e, '创建失败，请重试'))
    } finally {
      setBusy(false)
    }
  }

  const incomplete = username.trim() === '' || password === '' || displayName.trim() === ''

  return (
    <Modal isOpen={open} onClose={close} placement="center" radius="md">
      <ModalContent>
        <ModalHeader>新建用户</ModalHeader>
        <ModalBody className="gap-3">
          <Input label="用户名" radius="md" value={username} onValueChange={setUsername} isRequired />
          <Input label="初始密码" type="password" radius="md" value={password} onValueChange={setPassword} isRequired />
          <Input label="显示名" radius="md" value={displayName} onValueChange={setDisplayName} isRequired />
          <Select
            label="角色"
            radius="md"
            selectedKeys={[role]}
            onChange={(e) => setRole(e.target.value as UserRole)}
            isRequired
          >
            {ROLE_OPTIONS.map((o) => (
              <SelectItem key={o.value}>{o.label}</SelectItem>
            ))}
          </Select>
          {error !== null && <p className="text-xs text-[#ff746c]">{error}</p>}
        </ModalBody>
        <ModalFooter>
          <Button variant="light" radius="md" onPress={close} isDisabled={busy}>
            取消
          </Button>
          <Button color="primary" radius="md" isLoading={busy} isDisabled={incomplete} onPress={() => void submit()}>
            创建
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  )
}

/* ---------- 编辑用户 ---------- */

function EditUserDialog({ user, onClose, onDone }: { user: User; onClose: () => void; onDone: () => void }) {
  const { client } = useAuth()
  const [displayName, setDisplayName] = useState(user.displayName)
  const [role, setRole] = useState<UserRole>(user.role)
  const [state, setState] = useState<UserState>(user.state)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async () => {
    // patch 只传变化字段，避免覆盖未修改的属性（docs/api.md §6）
    const patch: UserPatch = {}
    if (displayName !== user.displayName) patch.displayName = displayName
    if (role !== user.role) patch.role = role
    if (state !== user.state) patch.state = state
    setBusy(true)
    setError(null)
    try {
      if (Object.keys(patch).length > 0) await client.updateUser(user.userId, patch)
      onDone()
      onClose()
    } catch (e) {
      setError(apiErrorText(e, '保存失败，请重试'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal isOpen onClose={onClose} placement="center" radius="md">
      <ModalContent>
        <ModalHeader>
          编辑用户 <span className="fs-mono text-sm font-normal text-[#8b98a1]">{user.username}</span>
        </ModalHeader>
        <ModalBody className="gap-3">
          <Input label="显示名" radius="md" value={displayName} onValueChange={setDisplayName} isRequired />
          <Select label="角色" radius="md" selectedKeys={[role]} onChange={(e) => setRole(e.target.value as UserRole)}>
            {ROLE_OPTIONS.map((o) => (
              <SelectItem key={o.value}>{o.label}</SelectItem>
            ))}
          </Select>
          <Select label="状态" radius="md" selectedKeys={[state]} onChange={(e) => setState(e.target.value as UserState)}>
            {STATE_OPTIONS.map((o) => (
              <SelectItem key={o.value}>{o.label}</SelectItem>
            ))}
          </Select>
          {error !== null && <p className="text-xs text-[#ff746c]">{error}</p>}
        </ModalBody>
        <ModalFooter>
          <Button variant="light" radius="md" onPress={onClose} isDisabled={busy}>
            取消
          </Button>
          <Button
            color="primary"
            radius="md"
            isLoading={busy}
            isDisabled={displayName.trim() === ''}
            onPress={() => void submit()}
          >
            保存
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  )
}

/* ---------- 重置密码 ---------- */

function ResetPasswordDialog({ user, onClose }: { user: User; onClose: () => void }) {
  const { client } = useAuth()
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState(false)

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      // revoke_sessions 固定 true：重置后吊销该用户全部会话
      await client.adminResetPassword(user.userId, password, true)
      setDone(true)
    } catch (e) {
      setError(apiErrorText(e, '重置失败，请重试'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal isOpen onClose={onClose} placement="center" radius="md">
      <ModalContent>
        <ModalHeader>
          重置密码 <span className="fs-mono text-sm font-normal text-[#8b98a1]">{user.username}</span>
        </ModalHeader>
        <ModalBody className="gap-3">
          {done ? (
            <p className="text-sm text-[#62e6a7]">已重置</p>
          ) : (
            <>
              <Input label="新密码" type="password" radius="md" value={password} onValueChange={setPassword} isRequired />
              <p className="text-xs text-[#8b98a1]">重置后该用户所有会话将被吊销（revoke_sessions=true）</p>
              {error !== null && <p className="text-xs text-[#ff746c]">{error}</p>}
            </>
          )}
        </ModalBody>
        <ModalFooter>
          <Button variant="light" radius="md" onPress={onClose} isDisabled={busy}>
            关闭
          </Button>
          {!done && (
            <Button color="primary" radius="md" isLoading={busy} isDisabled={password === ''} onPress={() => void submit()}>
              确认重置
            </Button>
          )}
        </ModalFooter>
      </ModalContent>
    </Modal>
  )
}

/* ---------- 删除用户 ---------- */

function DeleteUserDialog({ user, onClose, onDone }: { user: User; onClose: () => void; onDone: () => void }) {
  const { client } = useAuth()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.deleteUser(user.userId)
      onDone()
      onClose()
    } catch (e) {
      // 失败留在弹窗内展示（如 failed_precondition：不可删除最后一个 ACTIVE ADMIN）
      setError(apiErrorText(e, '删除失败，请重试'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <ConfirmDialog
      open
      title="删除用户"
      confirmLabel="确认删除"
      danger
      busy={busy}
      onConfirm={() => void submit()}
      onClose={onClose}
    >
      <p className="text-sm text-foreground-500">
        软删除：state=DELETED，保留审计与操作历史。用户 <span className="fs-mono">{user.username}</span> 将无法再登录。
      </p>
      {error !== null && <p className="mt-2 text-xs text-[#ff746c]">{error}</p>}
    </ConfirmDialog>
  )
}

/* ---------- 页面 ---------- */

export function UsersPage() {
  const { client } = useAuth()
  const navigate = useNavigate()

  const [query, setQuery] = useState('')
  const [roleFilter, setRoleFilter] = useState('all')
  const [stateFilter, setStateFilter] = useState('all')
  // 游标链：tokens[i] 是第 i 页的入参 pageToken，首页为空串；只回传不解析
  const [tokens, setTokens] = useState<string[]>([''])
  const [pageIndex, setPageIndex] = useState(0)

  const [createOpen, setCreateOpen] = useState(false)
  const [editTarget, setEditTarget] = useState<User | null>(null)
  const [resetTarget, setResetTarget] = useState<User | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<User | null>(null)

  const filter = useMemo<ListUsersFilter>(
    () => ({
      query: query.trim() === '' ? undefined : query.trim(),
      role: roleFilter === 'all' ? undefined : (roleFilter as UserRole),
      state: stateFilter === 'all' ? undefined : (stateFilter as UserState),
    }),
    [query, roleFilter, stateFilter],
  )
  const filterKey = JSON.stringify(filter)

  // 筛选变化回到第一页：渲染期间与上一次筛选比较后同步重置，避免 effect 级联渲染
  const [appliedKey, setAppliedKey] = useState(filterKey)
  if (appliedKey !== filterKey) {
    setAppliedKey(filterKey)
    setTokens([''])
    setPageIndex(0)
  }

  const { data, status, error, reload } = useAsyncData(
    () => client.listUsers(filter, { pageSize: PAGE_SIZE, pageToken: tokens[pageIndex] }),
    // filter 由 useMemo 稳定，依赖用其序列化结果 filterKey
    [filterKey, pageIndex],
  )

  const goPrev = () => setPageIndex((i) => Math.max(0, i - 1))
  const goNext = () => {
    if (data === null || data.nextPageToken === '') return
    setTokens((prev) => {
      const next = prev.slice(0, pageIndex + 1)
      next[pageIndex + 1] = data.nextPageToken
      return next
    })
    setPageIndex((i) => i + 1)
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <Input
          aria-label="搜索用户"
          placeholder="用户名 / 显示名"
          size="sm"
          radius="md"
          isClearable
          value={query}
          onValueChange={setQuery}
          startContent={<Search size={14} aria-hidden />}
          className="sm:w-64"
        />
        <Select
          aria-label="按角色筛选"
          size="sm"
          radius="md"
          selectedKeys={[roleFilter]}
          onChange={(e) => setRoleFilter(e.target.value)}
          className="sm:w-36"
        >
          {[
            <SelectItem key="all">全部角色</SelectItem>,
            ...ROLE_OPTIONS.map((o) => <SelectItem key={o.value}>{o.label}</SelectItem>),
          ]}
        </Select>
        <Select
          aria-label="按状态筛选"
          size="sm"
          radius="md"
          selectedKeys={[stateFilter]}
          onChange={(e) => setStateFilter(e.target.value)}
          className="sm:w-36"
        >
          {[
            <SelectItem key="all">全部状态</SelectItem>,
            ...STATE_OPTIONS.map((o) => <SelectItem key={o.value}>{o.label}</SelectItem>),
          ]}
        </Select>
        <Button
          color="primary"
          size="sm"
          radius="md"
          className="sm:ml-auto"
          startContent={<Plus size={14} aria-hidden />}
          onPress={() => setCreateOpen(true)}
        >
          新建用户
        </Button>
      </div>

      {status === 'loading' && <StateView kind="loading" />}
      {status === 'error' && error !== null && (
        <StateView
          kind={error.code === 'permission_denied' ? 'denied' : 'error'}
          message={error.message}
          onRetry={reload}
        />
      )}
      {status === 'ok' && data !== null && data.items.length === 0 && (
        <StateView kind="empty" title="没有符合条件的用户" message="调整筛选条件后重试" />
      )}
      {status === 'ok' && data !== null && data.items.length > 0 && (
        <section className="fs-panel" aria-label="用户列表">
          <Table aria-label="用户列表" removeWrapper radius="none" className="fs-table-tight">
            <TableHeader>
              <TableColumn>用户名</TableColumn>
              <TableColumn>显示名</TableColumn>
              <TableColumn>角色</TableColumn>
              <TableColumn>状态</TableColumn>
              <TableColumn>最近登录</TableColumn>
              <TableColumn>操作</TableColumn>
            </TableHeader>
            <TableBody emptyContent="没有符合条件的用户">
              {data.items.map((u) => (
                <TableRow key={u.userId}>
                  <TableCell className="fs-mono">{u.username}</TableCell>
                  <TableCell>{u.displayName}</TableCell>
                  <TableCell>
                    <Badge label={ROLE_BADGE[u.role].label} tone={ROLE_BADGE[u.role].tone} />
                  </TableCell>
                  <TableCell>
                    <Badge label={STATE_BADGE[u.state].label} tone={STATE_BADGE[u.state].tone} />
                  </TableCell>
                  <TableCell className="fs-mono text-[#8b98a1]">{formatTime(u.lastLoginAt)}</TableCell>
                  <TableCell>
                    <Dropdown>
                      <DropdownTrigger>
                        <Button size="sm" radius="md" variant="light" endContent={<ChevronDown size={12} aria-hidden />}>
                          操作
                        </Button>
                      </DropdownTrigger>
                      <DropdownMenu
                        aria-label={`用户 ${u.username} 的操作`}
                        onAction={(key) => {
                          if (key === 'edit') setEditTarget(u)
                          if (key === 'reset') setResetTarget(u)
                          if (key === 'delete') setDeleteTarget(u)
                        }}
                      >
                        <DropdownItem key="edit">编辑</DropdownItem>
                        <DropdownItem key="reset">重置密码</DropdownItem>
                        <DropdownItem key="delete" color="danger" className="text-danger">
                          删除
                        </DropdownItem>
                      </DropdownMenu>
                    </Dropdown>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <TokenPager
            page={pageIndex + 1}
            hasPrev={pageIndex > 0}
            hasNext={data.nextPageToken !== ''}
            onPrev={goPrev}
            onNext={goNext}
          />
        </section>
      )}

      <CreateUserDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onDone={(created) => {
          reload()
          navigate(`/grants?subject=${created.userId}`)
        }}
      />
      {/* key 强制按目标用户重置弹窗内部状态 */}
      {editTarget !== null && (
        <EditUserDialog key={editTarget.userId} user={editTarget} onClose={() => setEditTarget(null)} onDone={reload} />
      )}
      {resetTarget !== null && (
        <ResetPasswordDialog key={resetTarget.userId} user={resetTarget} onClose={() => setResetTarget(null)} />
      )}
      {deleteTarget !== null && (
        <DeleteUserDialog key={deleteTarget.userId} user={deleteTarget} onClose={() => setDeleteTarget(null)} onDone={reload} />
      )}
    </div>
  )
}
