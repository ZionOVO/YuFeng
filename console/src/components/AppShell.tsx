// 应用壳：圆融主题（fusionr）的侧栏布局——品牌印章、导航、健康指示、用户菜单、顶栏与页面出口。
// 侧栏与面板样式复用 theme/fusion.css 的 fs-* 结构件。

import { useEffect, useRef, useState } from 'react'
import {
  Avatar,
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
  useDisclosure,
} from '@heroui/react'
import {
  Bot,
  BriefcaseBusiness,
  ChevronDown,
  ClipboardList,
  FileClock,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Menu,
  Settings,
  ScrollText,
  Server,
  ShieldAlert,
  ShieldCheck,
  Cpu,
  UserRound,
  Users,
  X,
} from 'lucide-react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { useAuth } from '../auth/useAuth'
import { isApiError } from '../api/errors'
import { BrandMark } from './BrandMark'
import { JarvisDock } from './chat/JarvisDock'
import { useDialogFocusTrap } from './useDialogFocusTrap'

const PRIMARY_NAV_ITEMS = [
  { to: '/dashboard', label: '仪表盘', icon: LayoutDashboard },
] as const

const PROTECTION_ITEMS = [
  { to: '/assets', label: '资产台账', icon: Server },
  { to: '/agent', label: 'Agent 管理', icon: Bot },
  { to: '/cases', label: '案件', icon: BriefcaseBusiness },
  { to: '/releases', label: '防护策略', icon: ShieldCheck },
] as const

const RECORD_ITEMS = [
  { to: '/events', label: '安全事件', icon: ShieldAlert },
  { to: '/audit', label: '操作审计', icon: ScrollText },
] as const

const SYSTEM_ITEMS = [
  { to: '/model', label: '模型网关', icon: Cpu },
  { to: '/users', label: '用户管理', icon: Users },
  { to: '/workers', label: 'Worker', icon: Server },
] as const

type MobilePanel = 'menu'

const PAGE_META: [RegExp, { eyebrow: string; title: string }][] = [
  [/^\/dashboard/, { eyebrow: 'YUFENG / CONTROL PLANE', title: '态势总览' }],
  [/^\/assets\/[^/]+/, { eyebrow: 'ASSETS / DETAIL', title: '资产详情' }],
  [/^\/assets/, { eyebrow: 'YUFENG / ASSETS', title: '资产台账' }],
  [/^\/events\/[^/]+/, { eyebrow: 'EVENTS / DETAIL', title: '事件详情' }],
  [/^\/events/, { eyebrow: 'YUFENG / EVENTS', title: '事件流' }],
  [/^\/releases\/[^/]+/, { eyebrow: 'PROTECTION / POLICY', title: '防护策略详情' }],
  [/^\/releases/, { eyebrow: 'YUFENG / PROTECTION', title: '防护策略' }],
  [/^\/audit/, { eyebrow: 'YUFENG / AUDIT', title: '操作审计' }],
  [/^\/users/, { eyebrow: 'YUFENG / IAM', title: '用户管理' }],
  [/^\/model/, { eyebrow: 'YUFENG / MODEL', title: '模型网关' }],
  [/^\/grants/, { eyebrow: 'YUFENG / IAM', title: '人员授权' }],
  [/^\/agent/, { eyebrow: 'ASSETS / AGENTS', title: 'Agent 管理' }],
  [/^\/cases/, { eyebrow: 'YUFENG / CASES', title: '案件工作台' }],
  [/^\/workers/, { eyebrow: 'YUFENG / WORKERS', title: '调查执行进程' }],
]

function pageMeta(pathname: string): { eyebrow: string; title: string } {
  for (const [re, meta] of PAGE_META) {
    if (re.test(pathname)) return meta
  }
  return { eyebrow: 'YUFENG', title: '控制台' }
}

/** 修改密码弹窗（AuthService.ChangePassword，docs/api.md §5.4）。 */
function ChangePasswordDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { client } = useAuth()
  const [oldPassword, setOldPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState(false)

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.changePassword({ oldPassword, newPassword })
      setDone(true)
    } catch (e) {
      setError(isApiError(e) ? `修改失败：${e.message}（${e.code}）` : '修改失败，请重试')
    } finally {
      setBusy(false)
    }
  }

  const close = () => {
    setOldPassword('')
    setNewPassword('')
    setConfirm('')
    setError(null)
    setDone(false)
    onClose()
  }

  return (
    <Modal isOpen={open} onClose={close} placement="center" radius="lg">
      <ModalContent>
        <ModalHeader>修改密码</ModalHeader>
        <ModalBody className="gap-3">
          {done ? (
            <p className="text-sm text-[#62e6a7]">密码已更新，下次登录生效。</p>
          ) : (
            <>
              <Input label="当前密码" type="password" radius="md" value={oldPassword} onValueChange={setOldPassword} isRequired />
              <Input label="新密码" type="password" radius="md" value={newPassword} onValueChange={setNewPassword} isRequired />
              <Input
                label="确认新密码"
                type="password"
                radius="md"
                value={confirm}
                onValueChange={setConfirm}
                isInvalid={confirm !== '' && confirm !== newPassword}
                errorMessage={confirm !== '' && confirm !== newPassword ? '两次输入不一致' : undefined}
                isRequired
              />
              {error !== null && <p className="text-xs text-[#ff746c]">{error}</p>}
            </>
          )}
        </ModalBody>
        <ModalFooter>
          <Button variant="light" radius="md" onPress={close}>
            关闭
          </Button>
          {!done && (
            <Button
              color="primary"
              radius="md"
              isLoading={busy}
              isDisabled={oldPassword === '' || newPassword === '' || confirm !== newPassword}
              onPress={submit}
            >
              确认修改
            </Button>
          )}
        </ModalFooter>
      </ModalContent>
    </Modal>
  )
}

export function AppShell() {
  const { user, hasTool, logout } = useAuth()
  const location = useLocation()
  const meta = pageMeta(location.pathname)
  const passwordModal = useDisclosure()
  const orchestra = location.pathname === '/agent'
  const activeGroup = PROTECTION_ITEMS.some((item) => location.pathname.startsWith(item.to))
    ? 'protection'
    : RECORD_ITEMS.some((item) => location.pathname.startsWith(item.to))
      ? 'records'
      : SYSTEM_ITEMS.some((item) => location.pathname.startsWith(item.to))
        ? 'system'
        : null
  const [openGroups, setOpenGroups] = useState<Record<'protection' | 'records' | 'system', boolean>>(() => ({
    protection: PROTECTION_ITEMS.some((item) => location.pathname.startsWith(item.to)),
    records: RECORD_ITEMS.some((item) => location.pathname.startsWith(item.to)),
    system: SYSTEM_ITEMS.some((item) => location.pathname.startsWith(item.to)),
  }))
  const [collapsedActivePath, setCollapsedActivePath] = useState<string | null>(null)
  const [mobilePanelState, setMobilePanelState] = useState<{ panel: MobilePanel; pathname: string } | null>(null)
  const mobilePanel = mobilePanelState?.pathname === location.pathname ? mobilePanelState.panel : null
  const systemItems = SYSTEM_ITEMS.filter((item) => {
    if (item.to === '/model') return user?.role === 'USER_ROLE_ADMIN'
    if (item.to === '/users') return hasTool('user.admin')
    return user?.role === 'USER_ROLE_ADMIN' && hasTool('worker.enroll')
  })
  const mobileSheetRef = useRef<HTMLElement | null>(null)
  useDialogFocusTrap(mobilePanel !== null, mobileSheetRef)
  const closeMobilePanel = () => setMobilePanelState(null)
  const toggleMobilePanel = (panel: MobilePanel) => {
    setMobilePanelState((current) => current?.panel === panel && current.pathname === location.pathname
      ? null
      : { panel, pathname: location.pathname })
  }

  useEffect(() => {
    if (mobilePanel === null) return
    const close = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMobilePanelState(null)
    }
    document.body.classList.add('yf-mobile-menu-open')
    window.addEventListener('keydown', close)
    return () => {
      document.body.classList.remove('yf-mobile-menu-open')
      window.removeEventListener('keydown', close)
    }
  }, [mobilePanel])
  const groupIsOpen = (group: keyof typeof openGroups) =>
    openGroups[group] || (activeGroup === group && collapsedActivePath !== location.pathname)
  const toggleGroup = (group: keyof typeof openGroups) => {
    const open = groupIsOpen(group)
    setOpenGroups((current) => ({ ...current, [group]: !open }))
    if (activeGroup === group) setCollapsedActivePath(open ? location.pathname : null)
  }
  const navLink = (item: { to: string; label: string; icon: typeof LayoutDashboard }, child = false) => (
    <NavLink key={item.to} to={item.to} className={({ isActive }) => `fs-nav-item${child ? ' fs-nav-child' : ''}${isActive ? ' is-active' : ''}`}>
      <item.icon size={14} aria-hidden />
      {item.label}
    </NavLink>
  )
  const mobileNavLink = (item: { to: string; label: string; icon: typeof LayoutDashboard }, onSelect?: () => void) => (
    <NavLink key={item.to} to={item.to} className={({ isActive }) => `fs-mobile-nav-item${isActive ? ' is-active' : ''}`} onClick={onSelect}>
      <item.icon size={20} aria-hidden />
      <span>{item.label}</span>
    </NavLink>
  )
  const navGroup = (
    key: keyof typeof openGroups,
    label: string,
    icon: typeof LayoutDashboard,
    items: ReadonlyArray<{ to: string; label: string; icon: typeof LayoutDashboard }>,
  ) => {
    const active = items.some((item) => location.pathname.startsWith(item.to))
    const open = groupIsOpen(key)
    const Icon = icon
    return (
      <div className="fs-nav-group" key={key}>
        <button type="button" className={`fs-nav-item fs-nav-group-trigger${active ? ' is-active-parent' : ''}`} aria-expanded={open} onClick={() => toggleGroup(key)}>
          <Icon size={14} aria-hidden />
          <span>{label}</span>
          <ChevronDown size={14} className="fs-nav-chevron" aria-hidden />
        </button>
        {open && <div className="fs-nav-children">{items.map((item) => navLink(item, true))}</div>}
      </div>
    )
  }

  return (
    <div className="yf-shell">
      <aside className="fs-sidebar" aria-label="主导航">
        <div className="fs-brand">
          <BrandMark size={38} />
          <div>
            <p className="fs-wordmark">御锋</p>
            <p className="fs-wordmark-sub">YUFENG 2.0</p>
          </div>
        </div>
        <nav className="fs-nav">
          {PRIMARY_NAV_ITEMS.map((item) => navLink(item))}
		  {navGroup('protection', '安全运营', ShieldCheck, PROTECTION_ITEMS)}
          {navGroup('records', '记录追溯', FileClock, RECORD_ITEMS)}
          {systemItems.length > 0 && navGroup('system', '系统设置', Settings, systemItems)}
        </nav>
        <div className="fs-sidebar-foot">
          <p className="fs-health">
            <span className="fs-dot" aria-hidden />
            会话 · 已认证
          </p>
          <Dropdown placement="top-start">
            <DropdownTrigger>
              <button type="button" className="fs-nav-item w-full" aria-label="当前用户菜单">
                <Avatar size="sm" fallback={<UserRound size={14} aria-hidden />} className="shrink-0" />
                <span className="min-w-0 flex-1 truncate text-left">{user?.displayName ?? user?.username ?? '—'}</span>
                <ClipboardList size={12} className="shrink-0 opacity-60" aria-hidden />
              </button>
            </DropdownTrigger>
            <DropdownMenu aria-label="用户菜单">
              <DropdownItem key="who" isReadOnly description={user?.role ?? ''} className="opacity-70">
                {user?.username ?? ''}
              </DropdownItem>
              <DropdownItem key="password" onPress={passwordModal.onOpen}>
                修改密码
              </DropdownItem>
              <DropdownItem key="logout" color="danger" className="text-danger" onPress={() => void logout()}>
                退出登录
              </DropdownItem>
            </DropdownMenu>
          </Dropdown>
        </div>
      </aside>

      <div className="yf-content">
        {!orchestra && (
          <header className="fs-topbar">
            <div>
              <p className="fs-eyebrow">{meta.eyebrow}</p>
              <h1 className="fs-title">{meta.title}</h1>
            </div>
          </header>
        )}
        <main className={orchestra ? 'yf-page yf-page--flush' : 'yf-page'}>
          <Outlet />
        </main>
      </div>

      {!orchestra && <JarvisDock />}
      <header className="fs-mobile-nav" aria-label="移动顶栏">
        <div className="fs-mobile-nav-brand">
          <BrandMark size={32} />
          <div className="min-w-0">
            <p className="fs-mobile-nav-title">{meta.title}</p>
            <p className="fs-eyebrow">YUFENG</p>
          </div>
        </div>
        <button
          type="button"
          className={`fs-mobile-menu${mobilePanel === 'menu' ? ' is-active' : ''}`}
          aria-label="打开移动导航"
          aria-haspopup="dialog"
          aria-expanded={mobilePanel === 'menu'}
          onClick={() => toggleMobilePanel('menu')}
        >
          <Menu size={22} aria-hidden />
        </button>
      </header>
      {mobilePanel !== null && (
        <>
          <button type="button" className="fs-mobile-backdrop" aria-label="关闭移动导航" onClick={closeMobilePanel} />
          <section ref={mobileSheetRef} tabIndex={-1} className="fs-mobile-sheet" role="dialog" aria-modal="true" aria-label="移动导航">
            <header className="fs-mobile-sheet-head">
              <div>
                <p className="fs-eyebrow">YUFENG / NAVIGATION</p>
                <h2>导航</h2>
              </div>
              <button type="button" className="fs-mobile-close" aria-label="关闭导航" autoFocus onClick={closeMobilePanel}>
                <X size={20} aria-hidden />
              </button>
            </header>
            <nav className="fs-mobile-sheet-links">
              <p className="fs-mobile-group-label">总览</p>
              {PRIMARY_NAV_ITEMS.map((item) => mobileNavLink(item, closeMobilePanel))}
              <p className="fs-mobile-group-label">安全运营</p>
              {PROTECTION_ITEMS.map((item) => mobileNavLink(item, closeMobilePanel))}
              <p className="fs-mobile-group-label">记录</p>
              {RECORD_ITEMS.map((item) => mobileNavLink(item, closeMobilePanel))}
              {systemItems.length > 0 && <p className="fs-mobile-group-label">系统设置</p>}
              {systemItems.map((item) => mobileNavLink(item, closeMobilePanel))}
            </nav>
            <div className="fs-mobile-account">
              <div className="fs-mobile-account-user">
                <Avatar size="sm" fallback={<UserRound size={14} aria-hidden />} />
                <div className="min-w-0">
                  <p>{user?.displayName ?? user?.username ?? '—'}</p>
                  <p>{user?.username ?? ''} · {user?.role ?? ''}</p>
                </div>
              </div>
              <button type="button" onClick={() => {
                closeMobilePanel()
                passwordModal.onOpen()
              }}>
                <KeyRound size={18} aria-hidden />
                修改密码
              </button>
              <button type="button" className="is-danger" onClick={() => void logout()}>
                <LogOut size={18} aria-hidden />
                退出登录
              </button>
            </div>
          </section>
        </>
      )}
      <ChangePasswordDialog open={passwordModal.isOpen} onClose={passwordModal.onClose} />
    </div>
  )
}
