// 初次配置只建立模型网关与贾维斯控制面；数据面在主控制台按资产人工接入。

import { useEffect, useState } from 'react'
import { Button, Checkbox, Input, Select, SelectItem } from '@heroui/react'
import { Navigate, useNavigate } from 'react-router-dom'
import { DefaultChatModel, DefaultModelBaseURL } from '../../api/limits'
import { isApiError } from '../../api/errors'
import type { ModelDialect } from '../../api/types'
import { MODEL_DIALECTS, normalizeDialect } from '../../api/modelDialect'
import { useAuth } from '../../auth/useAuth'
import { BrandMark } from '../../components/BrandMark'
import { SetupMark } from './SetupMark'

const STEPS = [
  { key: 'model', label: '配置模型网关' },
  { key: 'probe', label: '探测连通性' },
  { key: 'jarvis', label: '确认贾维斯在线' },
  { key: 'complete', label: '进入主控制台' },
] as const

type StepKey = (typeof STEPS)[number]['key']

function currentStep(state: string, configured: boolean, jarvisOnline: boolean): StepKey {
  if (!configured || state === 'ONBOARDING_STATE_PENDING') return 'model'
  if (state === 'ONBOARDING_STATE_MODEL_CONFIGURED' || state === 'ONBOARDING_STATE_FAILED') return 'probe'
  if (!jarvisOnline) return 'jarvis'
  return 'complete'
}

function stepStatus(key: StepKey, current: StepKey): 'done' | 'current' | 'pending' {
  const index = STEPS.findIndex((step) => step.key === key)
  const currentIndex = STEPS.findIndex((step) => step.key === current)
  if (index < currentIndex) return 'done'
  if (index === currentIndex) return 'current'
  return 'pending'
}

export function SetupPage() {
  const { user, onboarding, client, refreshOnboarding, refreshAccess, logout } = useAuth()
  const navigate = useNavigate()
  const [baseUrl, setBaseUrl] = useState(DefaultModelBaseURL)
  const [model, setModel] = useState(DefaultChatModel)
  const [dialect, setDialect] = useState<ModelDialect>('MODEL_DIALECT_OPENAI_CHAT')
  const [secret, setSecret] = useState('')
  const [clearSecret, setClearSecret] = useState(false)
  const [busy, setBusy] = useState<'save' | 'probe' | 'complete' | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (onboarding === null) return
    // 引导快照只回填非敏感坐标；密钥始终保持空白。
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (onboarding.baseUrl !== '') setBaseUrl(onboarding.baseUrl)
    if (onboarding.model !== '') setModel(onboarding.model)
    setDialect(normalizeDialect(onboarding.dialect))
  }, [onboarding])

  const showError = (cause: unknown) => {
    if (isApiError(cause)) {
      setError(`${cause.message}（${cause.code}）`)
      return
    }
    setError('操作失败，请重试')
  }

  const saveModelGateway = async () => {
    setBusy('save')
    setError(null)
    try {
      await client.putModelConfig({ baseUrl: baseUrl.trim(), model: model.trim(), dialect, secret, clearSecret })
      setSecret('')
      setClearSecret(false)
      await refreshOnboarding()
    } catch (cause) {
      showError(cause)
    } finally {
      setBusy(null)
    }
  }

  const probeModelGateway = async () => {
    setBusy('probe')
    setError(null)
    try {
      await client.testModelConnectivity()
      await refreshOnboarding()
    } catch (cause) {
      showError(cause)
      await refreshOnboarding()
    } finally {
      setBusy(null)
    }
  }

  const enterConsole = async () => {
    setBusy('complete')
    setError(null)
    try {
      await client.completeOnboarding()
      await refreshOnboarding()
      await refreshAccess()
      navigate('/dashboard', { replace: true })
    } catch (cause) {
      showError(cause)
      await refreshOnboarding()
    } finally {
      setBusy(null)
    }
  }

  if (onboarding === null || user === null) {
    return <p className="p-8 text-sm text-[#8b98a1]">正在读取引导状态</p>
  }
  if (onboarding.state === 'ONBOARDING_STATE_COMPLETED') {
    return <Navigate to="/dashboard" replace />
  }
  if (user.role !== 'USER_ROLE_ADMIN') {
    return (
      <div className="flex min-h-screen flex-col items-center justify-center gap-4 bg-[#0a0d10] px-6">
        <p className="text-sm">等待管理员完成初次配置</p>
        <Button size="sm" radius="md" variant="bordered" onPress={() => void logout()}>
          退出登录
        </Button>
      </div>
    )
  }

  const configured = onboarding.baseUrl !== ''
  const current = currentStep(onboarding.state, configured, onboarding.jarvisOnline)
  const savedCoordinatesChanged =
    onboarding.baseUrl !== '' &&
    (baseUrl.trim() !== onboarding.baseUrl || model.trim() !== onboarding.model || dialect !== normalizeDialect(onboarding.dialect))
  const modelLive = onboarding.state === 'ONBOARDING_STATE_MODEL_LIVE'

  return (
    <div className="min-h-screen bg-[#0a0d10] px-6 py-10 text-[#e8eef2]">
      <div className="mx-auto flex w-full max-w-xl flex-col gap-6">
        <header className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <BrandMark size={40} />
            <div>
              <p className="fs-eyebrow">YUFENG / SETUP</p>
              <h1 className="fs-title mt-1">初次配置</h1>
            </div>
          </div>
          <Button size="sm" radius="md" variant="light" onPress={() => void logout()}>
            退出登录
          </Button>
        </header>

        <section className="fs-panel" aria-label="初次配置引导">
          <ol className="yf-setup-steps">
            {STEPS.map((step, index) => {
              const status = stepStatus(step.key, current)
              return (
                <li key={step.key} className={`yf-setup-step yf-setup-step--${status}`}>
                  <span className="yf-setup-step-n">{index + 1}</span>
                  <span>{step.label}</span>
                </li>
              )
            })}
          </ol>
        </section>

        <section className="fs-panel" aria-label="配置模型网关">
          <div className="fs-panel-head">
            <div>
              <p className="fs-panel-title">配置模型网关</p>
              <p className="fs-panel-sub">只配置 Brain 的智能出口；不在这里部署 Edge、ModelSide 或防御资产</p>
            </div>
            <SetupMark ok={configured && !savedCoordinatesChanged} label={configured ? '已保存' : '待配置'} />
          </div>
          <div className="flex flex-col gap-3 px-4 py-4">
            <Input label="模型端点" radius="md" value={baseUrl} onValueChange={setBaseUrl} />
            <Input label="模型名" radius="md" value={model} onValueChange={setModel} />
            <Select
              label="模型方言"
              radius="md"
              selectedKeys={[dialect]}
              onChange={(event) => setDialect(normalizeDialect(event.target.value))}
            >
              {MODEL_DIALECTS.map((item) => (
                <SelectItem key={item.value}>{item.label}</SelectItem>
              ))}
            </Select>
            <Input
              label="模型密钥（可选）"
              type="password"
              radius="md"
              value={secret}
              onValueChange={(value) => {
                setSecret(value)
                if (value !== '') setClearSecret(false)
              }}
              isDisabled={clearSecret}
              autoComplete="off"
              description={onboarding.hasSecret ? `已保存 ${onboarding.secretHint}；留空则保留，或显式清除` : '可留空；无 Key 时 Brain 不发送供应商认证头'}
            />
            {onboarding.hasSecret && (
              <Checkbox
                isSelected={clearSecret}
                onValueChange={(selected) => {
                  setClearSecret(selected)
                  if (selected) setSecret('')
                }}
              >
                清除已保存密钥
              </Checkbox>
            )}
            <p className="text-xs leading-5 text-[#8b98a1]">允许受控网络使用 HTTP；公网和任何敏感证据生成应使用 HTTPS。</p>
            <Button
              color="primary"
              radius="md"
              isLoading={busy === 'save'}
              isDisabled={busy !== null || baseUrl.trim() === '' || model.trim() === ''}
              onPress={() => void saveModelGateway()}
            >
              {configured ? '更新模型网关' : '保存模型网关'}
            </Button>
          </div>
        </section>

        {configured && !savedCoordinatesChanged && (
          <section className="fs-panel" aria-label="探测模型网关">
            <div className="fs-panel-head">
              <div>
                <p className="fs-panel-title">探测连通性</p>
                <p className="fs-panel-sub">由 Brain 直接请求当前真实模型端点；无 Key 时不发送认证头</p>
              </div>
              <SetupMark ok={modelLive} label={modelLive ? '探测成功' : '尚未通过'} />
            </div>
            <div className="flex flex-col gap-3 px-4 py-4">
              <p className="text-sm leading-6 text-[#8b98a1]">
                探测失败不会清除已保存的模型坐标；修正配置后可再次探测，不能跳过。
              </p>
              <Button
                radius="md"
                variant={modelLive ? 'bordered' : 'solid'}
                color={modelLive ? 'default' : 'primary'}
                isLoading={busy === 'probe'}
                isDisabled={busy !== null}
                onPress={() => void probeModelGateway()}
              >
                {modelLive ? '重新探测模型网关' : '探测模型网关'}
              </Button>
            </div>
          </section>
        )}

        {modelLive && (
          <section className="fs-panel" aria-label="确认贾维斯在线">
            <div className="fs-panel-head">
              <div>
                <p className="fs-panel-title">确认贾维斯在线</p>
                <p className="fs-panel-sub">贾维斯主动注册并保持心跳；Brain 不负责安装或启动它</p>
              </div>
              <SetupMark ok={onboarding.jarvisOnline} label={onboarding.jarvisOnline ? '在线' : '等待注册'} />
            </div>
            <div className="flex flex-col gap-3 px-4 py-4">
              <p className="text-sm leading-6 text-[#8b98a1]" aria-live="polite">
                {onboarding.jarvisOnline
                  ? '模型网关和贾维斯两项控制面条件均已满足，可以显式进入主控制台。'
                  : '请由技术人员启动 yufeng-jarvis；它在线后刷新本页。数据面不参与初次配置。'}
              </p>
              {!onboarding.jarvisOnline && (
                <Button radius="md" variant="bordered" isDisabled={busy !== null} onPress={() => void refreshOnboarding()}>
                  刷新在线状态
                </Button>
              )}
              <Button
                color="primary"
                radius="md"
                isLoading={busy === 'complete'}
                isDisabled={busy !== null || !onboarding.jarvisOnline}
                onPress={() => void enterConsole()}
              >
                进入主控制台
              </Button>
            </div>
          </section>
        )}

        {(error !== null || onboarding.lastError !== '') && (
          <p role="alert" className="text-xs text-[#ff746c]">
            {error ?? onboarding.lastError}
          </p>
        )}
      </div>
    </div>
  )
}
