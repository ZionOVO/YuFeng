// 应用路由：登录页公开；控制台页面在 RequireAuth + 圆融主题壳内；系统页对齐各服务真实门禁。

import { lazy, Suspense, useEffect, type ReactNode } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { AppShell } from './components/AppShell'
import { AdminRoleOnly, AdminToolOnly, RequireAuth, RequireOnboardingComplete, ToolOnly } from './components/guards'
import { JarvisSessionProvider } from './components/chat/JarvisSession'

const LoginPage = lazy(async () => ({ default: (await import('./pages/login/LoginPage')).LoginPage }))
const SetupPage = lazy(async () => ({ default: (await import('./pages/setup/SetupPage')).SetupPage }))
const DashboardPage = lazy(async () => ({ default: (await import('./pages/dashboard/DashboardPage')).DashboardPage }))
const AssetsPage = lazy(async () => ({ default: (await import('./pages/assets/AssetsPage')).AssetsPage }))
const AssetDetailPage = lazy(async () => ({ default: (await import('./pages/assets/AssetDetailPage')).AssetDetailPage }))
const EventsPage = lazy(async () => ({ default: (await import('./pages/events/EventsPage')).EventsPage }))
const EventDetailPage = lazy(async () => ({ default: (await import('./pages/events/EventDetailPage')).EventDetailPage }))
const ReleasesPage = lazy(async () => ({ default: (await import('./pages/releases/ReleasesPage')).ReleasesPage }))
const ReleaseDetailPage = lazy(async () => ({ default: (await import('./pages/releases/ReleaseDetailPage')).ReleaseDetailPage }))
const AuditPage = lazy(async () => ({ default: (await import('./pages/audit/AuditPage')).AuditPage }))
const AgentPage = lazy(async () => ({ default: (await import('./pages/agent/AgentPage')).AgentPage }))
const CasesPage = lazy(async () => ({ default: (await import('./pages/cases/CasesPage')).CasesPage }))
const GrantsPage = lazy(async () => ({ default: (await import('./pages/grants/GrantsPage')).GrantsPage }))
const UsersPage = lazy(async () => ({ default: (await import('./pages/users/UsersPage')).UsersPage }))
const ModelPage = lazy(async () => ({ default: (await import('./pages/model/ModelPage')).ModelPage }))
const WorkersPage = lazy(async () => ({ default: (await import('./pages/workers/WorkersPage')).WorkersPage }))
const NotFoundPage = lazy(async () => ({ default: (await import('./pages/NotFoundPage')).NotFoundPage }))

/** 把圆融主题类挂到根元素（HeroUI 主题变量与 fs-* 结构件按类生效；弹窗 portal 也受其覆盖）。 */
function ThemeScope({ children }: { children: ReactNode }) {
  useEffect(() => {
    const root = document.documentElement
    root.classList.add('fusionr', 'dark')
    return () => {
      root.classList.remove('fusionr', 'dark')
    }
  }, [])
  return children
}

export function App() {
  return (
    <Suspense fallback={<div className="min-h-screen bg-[#071013]" aria-label="页面加载中" />}>
      <Routes>
      <Route path="/login" element={<LoginPage />} />
      {/* 引导页在主壳外：未完成时整站只渲染本页（docs/api.md §17.9） */}
      <Route
        path="/setup"
        element={
          <RequireAuth>
            <ThemeScope>
              <SetupPage />
            </ThemeScope>
          </RequireAuth>
        }
      />
      <Route
        element={
          <RequireAuth>
            <RequireOnboardingComplete>
              <ThemeScope>
                <JarvisSessionProvider>
                  <AppShell />
                </JarvisSessionProvider>
              </ThemeScope>
            </RequireOnboardingComplete>
          </RequireAuth>
        }
      >
        <Route path="/dashboard" element={<DashboardPage />} />
        <Route path="/assets" element={<AssetsPage />} />
        <Route path="/assets/:assetId" element={<AssetDetailPage />} />
        <Route path="/events" element={<EventsPage />} />
        <Route path="/events/:eventId" element={<EventDetailPage />} />
        <Route path="/releases" element={<ReleasesPage />} />
        <Route path="/releases/:releaseId" element={<ReleaseDetailPage />} />
        <Route path="/audit" element={<AuditPage />} />
        <Route path="/agent" element={<AgentPage />} />
        <Route path="/cases" element={<CasesPage />} />
        <Route
          path="/grants"
          element={
            <ToolOnly tool="grant.write">
              <GrantsPage />
            </ToolOnly>
          }
        />
        <Route
          path="/users"
          element={
            <ToolOnly tool="user.admin">
              <UsersPage />
            </ToolOnly>
          }
        />
        <Route
          path="/model"
          element={
            <AdminRoleOnly>
              <ModelPage />
            </AdminRoleOnly>
          }
        />
        <Route
          path="/workers"
          element={
            <AdminToolOnly tool="worker.enroll">
              <WorkersPage />
            </AdminToolOnly>
          }
        />
      </Route>
      <Route path="/" element={<Navigate to="/dashboard" replace />} />
      <Route path="*" element={<NotFoundPage />} />
      </Routes>
    </Suspense>
  )
}
