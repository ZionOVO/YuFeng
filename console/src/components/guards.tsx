// 路由守卫：RequireAuth 校验会话；操作域页面按真实服务端工具与角色门禁裁剪可见性。

import { Navigate, Outlet, useLocation } from 'react-router-dom'
import { Spinner } from '@heroui/react'
import { useAuth } from '../auth/useAuth'
import { StateView } from './ui'

export function RequireAuth({ children }: { children?: React.ReactNode }) {
  const { status } = useAuth()
  const location = useLocation()

  if (status === 'loading') {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[#0a0d10]">
        <Spinner size="lg" aria-label="正在恢复会话" />
      </div>
    )
  }
  if (status === 'anon') {
    return <Navigate to="/login" state={{ from: location.pathname }} replace />
  }
  return children !== undefined ? <>{children}</> : <Outlet />
}

/** 引导未完成时不得进主壳（docs/api.md §17.9）。 */
export function RequireOnboardingComplete({ children }: { children?: React.ReactNode }) {
  const { status, onboarding } = useAuth()
  if (status === 'loading' || onboarding === null) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[#0a0d10]">
        <Spinner size="lg" aria-label="正在读取引导状态" />
      </div>
    )
  }
  if (onboarding.state !== 'ONBOARDING_STATE_COMPLETED') {
    return <Navigate to="/setup" replace />
  }
  return children !== undefined ? <>{children}</> : <Outlet />
}

function Denied({ message }: { message: string }) {
  return (
    <div className="yf-page">
      <StateView kind="denied" message={message} />
    </div>
  )
}

/** ToolOnly 按 EffectiveAccess 工具控制账户域兼容页面。 */
export function ToolOnly({ tool, children }: { tool: string; children?: React.ReactNode }) {
  const { hasTool } = useAuth()
  if (!hasTool(tool)) {
    return <Denied message={`此页需要 ${tool} 工具权限`} />
  }
  return children !== undefined ? <>{children}</> : <Outlet />
}

/** AdminRoleOnly 对齐模型网关服务的管理员角色硬门。 */
export function AdminRoleOnly({ children }: { children?: React.ReactNode }) {
  const { user } = useAuth()
  if (user?.role !== 'USER_ROLE_ADMIN') {
    return <Denied message="此页仅对管理员角色（USER_ROLE_ADMIN）开放" />
  }
  return children !== undefined ? <>{children}</> : <Outlet />
}

/** AdminToolOnly 对齐 Worker 服务的管理员角色与工具双重门禁。 */
export function AdminToolOnly({ tool, children }: { tool: string; children?: React.ReactNode }) {
  const { user, hasTool } = useAuth()
  if (user?.role !== 'USER_ROLE_ADMIN' || !hasTool(tool)) {
    return (
      <Denied message={`此页要求管理员角色（USER_ROLE_ADMIN）并持有 ${tool} 工具权限`} />
    )
  }
  return children !== undefined ? <>{children}</> : <Outlet />
}
