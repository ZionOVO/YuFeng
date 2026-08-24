// 登录页：整页居中卡片，走 AuthService.Login；错误码映射为中文提示（docs/api.md §17.2）。
// 本页在 ThemeScope 之外渲染，根元素自带 fusionr dark 主题类（yf-login 等样式收在 .fusionr 作用域）。

import { useState, type FormEvent } from 'react'
import { Button, Input, Spinner } from '@heroui/react'
import { Navigate, useLocation, useNavigate } from 'react-router-dom'
import { hasCode, isApiError } from '../../api/errors'
import { useAuth } from '../../auth/useAuth'
import { BrandMark } from '../../components/BrandMark'

/** 登录失败提示：优先结构化原因键，再按错误码映射，其余兜底 message + code。 */
function loginErrorText(e: unknown): string {
  if (isApiError(e)) {
    if (e.reasonKey === 'user_disabled') return '账户已停用，请联系管理员'
    if (hasCode(e, 'unauthenticated')) return '用户名或密码错误'
    if (hasCode(e, 'resource_exhausted')) return '尝试次数过多，请稍后再试'
    return `登录失败：${e.message}（${e.code}）`
  }
  return '登录失败，请重试'
}

export function LoginPage() {
  const { status, login, isOnboardingComplete, onboarding, client } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (status === 'loading') {
    return (
      <div className="fusionr dark flex min-h-screen items-center justify-center bg-[#0a0d10]">
        <Spinner size="lg" aria-label="正在恢复会话" />
      </div>
    )
  }
  if (status === 'authed') {
    if (onboarding === null) {
      return (
        <div className="fusionr dark flex min-h-screen items-center justify-center bg-[#0a0d10]">
          <Spinner size="lg" aria-label="正在读取引导状态" />
        </div>
      )
    }
    return <Navigate to={isOnboardingComplete ? '/dashboard' : '/setup'} replace />
  }

  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await login(username.trim(), password)
      const ob = await client.getOnboarding()
      const from = (location.state as { from?: string } | null)?.from ?? '/dashboard'
      if (ob.state !== 'ONBOARDING_STATE_COMPLETED') {
        navigate('/setup', { replace: true })
        return
      }
      const dest = from === '/login' || from === '/setup' ? '/dashboard' : from
      navigate(dest, { replace: true })
    } catch (err) {
      setError(loginErrorText(err))
      setBusy(false)
    }
  }

  return (
    <div className="fusionr dark">
      <div className="yf-login">
        <div className="flex w-full max-w-[400px] flex-col gap-4">
          <div className="yf-login-card">
            <div className="mb-6 flex flex-col items-center gap-3 text-center">
              <BrandMark size={56} decorative={false} />
              <div>
                <h1 className="fs-title mt-0">御锋控制台</h1>
                <p className="fs-eyebrow mt-2">YUFENG / CONTROL PLANE</p>
              </div>
            </div>
            <form
              className="flex flex-col gap-3"
              onSubmit={(e) => {
                void submit(e)
              }}
            >
              <Input
                label="用户名"
                size="sm"
                radius="md"
                value={username}
                onValueChange={setUsername}
                autoComplete="username"
                isRequired
              />
              <Input
                label="密码"
                type="password"
                size="sm"
                radius="md"
                value={password}
                onValueChange={setPassword}
                autoComplete="current-password"
                isRequired
              />
              {error !== null && (
                <p role="alert" className="rounded-md border border-[#653531] bg-[#261513] px-3 py-2 text-xs text-[#ff746c]">
                  {error}
                </p>
              )}
              <Button type="submit" color="primary" radius="md" isLoading={busy} isDisabled={username === '' || password === ''}>
                登录
              </Button>
            </form>
          </div>
        </div>
      </div>
    </div>
  )
}
