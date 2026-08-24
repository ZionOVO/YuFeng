// 初次配置引导：管理员先提交部署规格，技术人员再独立安装 Edge。
// 页面只轮询 Edge 主动心跳，不调用容器、进程管理或 Edge 探针。

import { useEffect, useRef, useState } from 'react'
import { Button, Input, Select, SelectItem } from '@heroui/react'
import { Navigate, useNavigate } from 'react-router-dom'
import { DefaultChatModel, DefaultModelBaseURL } from '../../api/limits'
import { hasCode, isApiError } from '../../api/errors'
import type { EdgeDeploymentSpecification } from '../../api/client'
import type { AccessMode, AssetDetail, Criticality, ModelDialect, Onboarding } from '../../api/types'
import { MODEL_DIALECTS, normalizeDialect } from '../../api/modelDialect'
import { useAuth } from '../../auth/useAuth'
import { BrandMark } from '../../components/BrandMark'
import { ACCESS_MODE_LABEL, CRITICALITY_BADGE, EDITABLE_ACCESS_MODES, EDITABLE_CRITICALITIES } from '../assets/assetMeta'
import { SetupMark } from './SetupMark'
import { holdAtLeast, SetupOkHoldMs, sleep } from './hold'
import { probeModelWithRetry } from './probe'

const STEPS = [
  { key: 'model', label: '配置模型' },
  { key: 'probe', label: '探测连通' },
  { key: 'specification', label: '提交部署规格' },
  { key: 'edge', label: '人工安装 Edge' },
  { key: 'assets', label: '设置防御资产' },
  { key: 'grant', label: '授权值守账户' },
] as const

type StepKey = (typeof STEPS)[number]['key']
type StageKey = 'probe' | 'edge'
type Stage = { key: StageKey; ok: boolean; failed?: boolean }
type EdgePosture = EdgeDeploymentSpecification['posture']

function validCIDR(value: string): boolean {
  const slash = value.lastIndexOf('/')
  if (slash <= 0 || slash === value.length - 1) return false
  const address = value.slice(0, slash)
  const prefix = Number(value.slice(slash + 1))
  if (!Number.isInteger(prefix)) return false
  const octets = address.split('.')
  if (octets.length === 4) {
    return prefix >= 0 && prefix <= 32 && octets.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255)
  }
  if (!address.includes(':') || prefix < 0 || prefix > 128 || address.includes('%')) return false
  try {
    const parsed = new URL(`http://[${address}]/`)
    return parsed.hostname !== ''
  } catch {
    return false
  }
}

const SETUP_OPERATOR_MARKER = 'yufeng.setup.operator'

interface SetupOperatorMarker {
  username: string
  userId: string
  grantWritten: boolean
}

function loadOperatorMarker(): SetupOperatorMarker | null {
  try {
    const parsed = JSON.parse(sessionStorage.getItem(SETUP_OPERATOR_MARKER) ?? 'null') as Partial<SetupOperatorMarker> | null
    if (parsed === null || typeof parsed.username !== 'string' || typeof parsed.userId !== 'string') return null
    return { username: parsed.username, userId: parsed.userId, grantWritten: parsed.grantWritten === true }
  } catch {
    return null
  }
}

function saveOperatorMarker(marker: SetupOperatorMarker): void {
  try {
    sessionStorage.setItem(SETUP_OPERATOR_MARKER, JSON.stringify(marker))
  } catch {
    // 当前页面仍用内存标记继续；禁用会话存储只影响刷新后的恢复。
  }
}

function clearOperatorMarker(): void {
  try {
    sessionStorage.removeItem(SETUP_OPERATOR_MARKER)
  } catch {
    // 禁用会话存储时无持久标记可清理。
  }
}

function activeStep(o: Onboarding, assetsDone: boolean): StepKey {
  if (o.state === 'ONBOARDING_STATE_EDGE_LIVE') return assetsDone ? 'grant' : 'assets'
  if (o.state === 'ONBOARDING_STATE_MODEL_LIVE') return o.deploymentSpecDigest === '' ? 'specification' : 'edge'
  if (o.state === 'ONBOARDING_STATE_MODEL_CONFIGURED') return 'probe'
  if (o.state === 'ONBOARDING_STATE_FAILED') {
    return 'model'
  }
  return 'model'
}

function stepStatus(key: StepKey, current: StepKey): 'done' | 'current' | 'pending' {
  const i = STEPS.findIndex((s) => s.key === key)
  const j = STEPS.findIndex((s) => s.key === current)
  if (i < j) return 'done'
  if (i === j) return 'current'
  return 'pending'
}

function SetupStatusCard({
  title,
  sub,
  running,
  done,
  ok,
  failed,
  onRetry,
}: {
  title: string
  sub?: string
  running: string
  done: string
  ok: boolean
  failed?: boolean
  onRetry?: () => void
}) {
  const label = failed ? `${title}失败` : ok ? done : running
  return (
    <section className="fs-panel" aria-label={title}>
      <div className="fs-panel-head">
        <div>
          <p className="fs-panel-title">{title}</p>
          {sub !== undefined && <p className="fs-panel-sub">{sub}</p>}
        </div>
      </div>
      <div className="yf-setup-status">
        {!failed && <SetupMark ok={ok} label={label} />}
        <p className="text-sm text-[#8b98a1]" aria-live="polite">
          {failed ? '未成功，可以重试' : ok ? done : running}
        </p>
        {failed && onRetry !== undefined && (
          <Button color="primary" radius="md" onPress={onRetry}>
            重试
          </Button>
        )}
      </div>
    </section>
  )
}

export function SetupPage() {
  const { user, onboarding, client, refreshOnboarding, refreshAccess, logout } = useAuth()
  const localAssetId = onboarding?.localAssetId ?? ''
  const onboardingState = onboarding?.state
  const navigate = useNavigate()
  const [baseUrl, setBaseUrl] = useState(DefaultModelBaseURL)
  const [model, setModel] = useState(DefaultChatModel)
  const [dialect, setDialect] = useState<ModelDialect>('MODEL_DIALECT_OPENAI_CHAT')
  const [secret, setSecret] = useState('')
  const [posture, setPosture] = useState<EdgePosture>('INGRESS_POSTURE_REVERSE_PROXY')
  const [unitId, setUnitId] = useState('edge-1')
  const [assetId, setAssetId] = useState('asset-local')
  const [trafficKey, setTrafficKey] = useState('')
  const [listenAddress, setListenAddress] = useState(':18080')
  const [upstreamUrl, setUpstreamUrl] = useState('')
  const [trustedProxyCidrs, setTrustedProxyCidrs] = useState('')
  const [modelProfileId, setModelProfileId] = useState('http-threat-model/default')
  const [modelGroup, setModelGroup] = useState('http-threat')
  const [modelType, setModelType] = useState('PVM')
  const [modelVersion, setModelVersion] = useState('v1')
  const [alertThreshold, setAlertThreshold] = useState('0.9')
  const [reviewFloor, setReviewFloor] = useState('0.5')
  const [allowedHeaders, setAllowedHeaders] = useState('accept, content-type, user-agent')
  const [modelWindowItems, setModelWindowItems] = useState('4096')
  const [modelWindowMemoryMiB, setModelWindowMemoryMiB] = useState('128')
  const [modelWindowAgeSeconds, setModelWindowAgeSeconds] = useState('2')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [missing, setMissing] = useState<number[]>([])
  const [assetsDone, setAssetsDone] = useState(false)
  const [assetItems, setAssetItems] = useState<AssetDetail[]>([])
  const [assetName, setAssetName] = useState('')
  const [assetMode, setAssetMode] = useState<AccessMode>('ACCESS_MODE_NETWORK')
  const [assetCrit, setAssetCrit] = useState<Criticality>('CRITICALITY_P2')
  const [localName, setLocalName] = useState('')
  const [localCrit, setLocalCrit] = useState<Criticality>('CRITICALITY_P2')
  const [stage, setStage] = useState<Stage | null>(null)
  const [probeFailed, setProbeFailed] = useState(false)
  const cancelled = useRef(false)
  const genRef = useRef(0)
  const running = useRef(false)
  const operatorMarker = useRef<SetupOperatorMarker | null>(loadOperatorMarker())

  useEffect(() => {
    cancelled.current = false
    return () => {
      cancelled.current = true
      genRef.current += 1
      running.current = false
    }
  }, [])

  useEffect(() => {
    if (onboarding === null) return
    // 引导快照是可恢复表单的权威来源。
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (onboarding.baseUrl !== '') setBaseUrl(onboarding.baseUrl)
    if (onboarding.model !== '') setModel(onboarding.model)
    setDialect(normalizeDialect(onboarding.dialect))
  }, [onboarding])

  const fail = (e: unknown) => {
    if (isApiError(e)) {
      setError(`${e.message}（${e.code}）`)
      if (e.missingPredicates !== undefined) setMissing(e.missingPredicates)
    } else {
      setError('操作失败，请重试')
    }
  }

  const after = async () => {
    await refreshOnboarding()
    setError(null)
    setMissing([])
  }

  const begin = () => {
    const gen = ++genRef.current
    running.current = true
    return () => gen === genRef.current && !cancelled.current
  }

  const finishStage = async (key: StageKey, startedAt: number, alive: () => boolean) => {
    await holdAtLeast(startedAt)
    if (!alive()) return
    setStage({ key, ok: true })
    await sleep(SetupOkHoldMs)
  }

  const waitEdge = async (alive: () => boolean) => {
    for (;;) {
      if (!alive()) return
      const snap = await client.getOnboarding()
      if (snap.edgeReady) return
      await refreshOnboarding()
      await sleep(1000)
    }
  }

  const deploymentRequest = (): { request?: EdgeDeploymentSpecification; error?: string } => {
    const key = trafficKey.trim()
    const listen = listenAddress.trim()
    const normalizedUnitId = unitId.trim()
    const normalizedAssetId = assetId.trim()
    const normalizedProfileId = modelProfileId.trim()
    const normalizedModelGroup = modelGroup.trim()
    const normalizedModelType = modelType.trim()
    const normalizedModelVersion = modelVersion.trim()
    const alert = Number(alertThreshold)
    const floor = Number(reviewFloor)
    const windowItems = Number(modelWindowItems)
    const windowMemoryMiB = Number(modelWindowMemoryMiB)
    const windowAgeSeconds = Number(modelWindowAgeSeconds)
    if (normalizedUnitId === '' || normalizedUnitId.length > 64) return { error: 'Edge 单元标识必须为 1–64 个字符。' }
    if (normalizedAssetId === '' || normalizedAssetId.length > 128) return { error: '资产标识必须为 1–128 个字符。' }
    if (key === '') return { error: '请填写流量键。' }
    if (listen === '') return { error: '请填写监听地址。' }
    if (!normalizedProfileId || !normalizedModelGroup || !normalizedModelType || !normalizedModelVersion) {
      return { error: '请完整填写模型档案坐标。' }
    }
    if (!Number.isFinite(alert) || !Number.isFinite(floor) || floor < 0 || alert > 1 || floor >= alert) {
      return { error: '复核下限必须不小于 0，且小于不超过 1 的告警阈值。' }
    }
    if (!Number.isInteger(windowItems) || windowItems < 1 || windowItems > 65536) {
      return { error: 'Edge 缓存窗口条目数必须是 1–65536 的整数。' }
    }
    if (!Number.isInteger(windowMemoryMiB) || windowMemoryMiB < 1 || windowMemoryMiB > 256) {
      return { error: 'Edge 缓存窗口内存必须是 1–256 MiB 的整数。' }
    }
    if (!Number.isFinite(windowAgeSeconds) || windowAgeSeconds < 0.01 || windowAgeSeconds > 300) {
      return { error: 'Edge 缓存窗口年龄必须在 0.01–300 秒之间。' }
    }
    const cidrs = [...new Set(trustedProxyCidrs.split(/[\s,]+/).map((value) => value.trim()).filter(Boolean))].sort()
    if (cidrs.some((value) => !validCIDR(value))) return { error: '可信代理必须是有效的 IPv4 或 IPv6 CIDR。' }
    const headers = [...new Set(allowedHeaders.split(/[\s,]+/).map((value) => value.trim().toLowerCase()).filter(Boolean))].sort()
    const common = {
      unitId: normalizedUnitId,
      assetId: normalizedAssetId,
      trafficKey: key,
      trustedProxyCidrs: cidrs,
      modelProfile: {
        profileId: normalizedProfileId,
        modelGroup: normalizedModelGroup,
        modelType: normalizedModelType,
        modelVersion: normalizedModelVersion,
        alertThreshold: alert,
        reviewFloor: floor,
        reviewWindowSeconds: 300,
        maxReviewPerUnit: 4,
        maxReviewPerRoute: 1,
        dedupeRule: 'MODEL_DEDUPE_RULE_METHOD_ROUTE_HIGHEST_SCORE' as const,
        allowedHeaders: headers,
        maxBodyBytes: 65536,
        reviewNewRoutes: true,
        reviewInsufficientCoverage: true,
      },
      modelIngressWindow: {
        maxItems: windowItems,
        maxRetainedBytes: String(windowMemoryMiB * 1024 * 1024),
        maxQueueAge: `${windowAgeSeconds}s`,
      },
    }
    if (posture === 'INGRESS_POSTURE_EXT_AUTHZ') {
      return { request: { ...common, posture, extAuthz: { listenAddress: listen } } }
    }
    const raw = upstreamUrl.trim()
    if (raw === '' || raw === 'builtin') return { error: '请填写真实的 HTTP 或 HTTPS 上游地址，不能使用 builtin。' }
    try {
      const parsed = new URL(raw)
      if (
        !['http:', 'https:'].includes(parsed.protocol) ||
        parsed.hostname === '' ||
        parsed.username !== '' ||
        parsed.password !== '' ||
        parsed.hash !== ''
      ) {
        return { error: '上游地址必须是无用户信息、无片段的绝对 HTTP 或 HTTPS 地址。' }
      }
    } catch {
      return { error: '上游地址必须是绝对 HTTP 或 HTTPS 地址。' }
    }
    return { request: { ...common, posture, reverseProxy: { listenAddress: listen, upstreamUrl: raw } } }
  }

  const runFrom = async (from: StageKey, existing?: () => boolean) => {
    const alive = existing ?? begin()
    setBusy(true)
    setError(null)
    setProbeFailed(false)
    try {
      if (from === 'probe') {
        setStage({ key: 'probe', ok: false })
        const t0 = Date.now()
        const probed = await probeModelWithRetry(() => client.testModelConnectivity(), { alive })
        if (!alive() || !probed) return
        await finishStage('probe', t0, alive)
        if (!alive()) return
        await after()
        setStage(null)
        return
      }
      setStage({ key: 'edge', ok: false })
      const t0 = Date.now()
      await waitEdge(alive)
      if (!alive()) return
      setAssetsDone(false)
      await finishStage('edge', t0, alive)
      if (!alive()) return
      await after()
      setStage(null)
    } catch (e) {
      if (!alive()) return
      fail(e)
      await refreshOnboarding()
      if (from === 'probe') {
        setStage(null)
        setProbeFailed(true)
      } else {
        setStage((s) => (s !== null ? { ...s, failed: true } : s))
      }
    } finally {
      if (alive()) {
        running.current = false
        setBusy(false)
      }
    }
  }

  useEffect(() => {
    if (onboarding === null || user === null || user.role !== 'USER_ROLE_ADMIN') return
    if (onboarding.state === 'ONBOARDING_STATE_COMPLETED') return
    if (running.current) return
    if (onboarding.state === 'ONBOARDING_STATE_MODEL_CONFIGURED') {
      // 该状态表示上次刷新发生在探测前，挂载后要恢复持久工作流。
      // eslint-disable-next-line react-hooks/set-state-in-effect
      void runFrom('probe')
      return
    }
    if (
      onboarding.state === 'ONBOARDING_STATE_MODEL_LIVE' &&
      onboarding.deploymentSpecDigest !== '' &&
      !onboarding.edgeReady
    ) {
      void runFrom('edge')
    }
    // 刷新落在探测或等待 Edge 心跳时自动续跑；规格提交和进程安装始终由人操作。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [onboarding?.state, onboarding?.deploymentSpecDigest, onboarding?.edgeReady, user?.role])

	const saveAndProbe = async () => {
		if (running.current) return
		if (
			secret === '' &&
			onboarding !== null &&
			(baseUrl !== onboarding.baseUrl || model !== onboarding.model || dialect !== normalizeDialect(onboarding.dialect))
		) {
			setError('修改模型配置时必须重新输入密钥；密钥不会从中台回填。')
			return
		}
    const alive = begin()
    setStage({ key: 'probe', ok: false })
    setBusy(true)
    setError(null)
    setProbeFailed(false)
    try {
      if (secret !== '') {
        await client.putModelConfig({ baseUrl, secret, model, dialect })
        setSecret('')
      }
      if (!alive()) return
      await runFrom('probe', alive)
    } catch (e) {
      if (!alive()) return
      fail(e)
      await refreshOnboarding()
      setStage(null)
      running.current = false
      setBusy(false)
    }
  }

  const submitSpecification = async () => {
    if (running.current) return
    const deployment = deploymentRequest()
    if (deployment.request === undefined) {
      setError(deployment.error ?? '部署规格不完整。')
      return
    }
    const alive = begin()
    setBusy(true)
    setError(null)
    try {
      await client.putDeploymentSpecification(deployment.request)
      if (!alive()) return
      await after()
      if (!alive()) return
      await runFrom('edge', alive)
    } catch (e) {
      if (!alive()) return
      fail(e)
      await refreshOnboarding()
      setStage(null)
    } finally {
      if (alive()) {
        running.current = false
        setBusy(false)
      }
    }
  }

  useEffect(() => {
    if (localAssetId === '') return
    void client
      .listAssets({}, { pageSize: 200 })
      .then((page) => {
        setAssetItems(page.items)
        const local = page.items.find((a) => a.asset.id === localAssetId)
        if (local !== undefined) {
          setLocalName(local.asset.displayName)
          setLocalCrit(local.asset.criticality === 'CRITICALITY_UNSPECIFIED' ? 'CRITICALITY_P2' : local.asset.criticality)
        }
      })
      .catch((e) => fail(e))
  }, [client, localAssetId, onboardingState])

  const saveLocalAsset = async () => {
    if (onboarding === null || onboarding.localAssetId === '') return
    setBusy(true)
    setError(null)
    try {
      const current = assetItems.find((a) => a.asset.id === onboarding.localAssetId)
      await client.updateAsset(
        onboarding.localAssetId,
        { displayName: localName.trim(), criticality: localCrit },
        current?.asset.updatedAt,
      )
      const page = await client.listAssets({}, { pageSize: 200 })
      setAssetItems(page.items)
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  const addDefenseAsset = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.createAsset({ displayName: assetName.trim(), accessMode: assetMode, criticality: assetCrit })
      setAssetName('')
      const page = await client.listAssets({}, { pageSize: 200 })
      setAssetItems(page.items)
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  const removeDefenseAsset = async (assetId: string) => {
    setBusy(true)
    setError(null)
    try {
      await client.deleteAsset(assetId)
      const page = await client.listAssets({}, { pageSize: 200 })
      setAssetItems(page.items)
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  const createGrantComplete = async () => {
    setBusy(true)
    setError(null)
    setMissing([])
    try {
      const snap = await client.getOnboarding()
      const local = snap.localAssetId
      if (local === '') {
        setError('本机资产未就绪，不能完成引导。请先提交规格并由技术人员启动 Edge。')
        return
      }
		let userId = ''
		let marker: SetupOperatorMarker | null = null
		const normalizedUsername = username.trim()
	      try {
	        const created = await client.createUser({
			username: normalizedUsername,
          password,
          displayName: displayName.trim(),
          role: 'USER_ROLE_OPERATOR',
        })
        userId = created.userId
			marker = { username: normalizedUsername, userId, grantWritten: false }
			operatorMarker.current = marker
			saveOperatorMarker(marker)
      } catch (e) {
        if (!hasCode(e, 'already_exists')) throw e
			marker = operatorMarker.current ?? loadOperatorMarker()
			if (marker === null || marker.username !== normalizedUsername) throw e
			const markerUserId = marker.userId
			const page = await client.listUsers({ query: normalizedUsername }, { pageSize: 50 })
			const found = page.items.find(
				(u) =>
					u.username === normalizedUsername &&
					u.userId === markerUserId &&
					u.role === 'USER_ROLE_OPERATOR' &&
					u.state === 'USER_STATE_ACTIVE',
			)
			if (found === undefined) throw e
        userId = found.userId
      }
		if (marker?.grantWritten !== true) {
			await client.putGrant({
				subjectUserId: userId,
				tools: ['govern.promote_enforce'],
				bindings: [{ kind: 'asset', id: local }],
			})
			marker = { username: normalizedUsername, userId, grantWritten: true }
			operatorMarker.current = marker
			saveOperatorMarker(marker)
		}
	      await client.completeOnboarding()
		operatorMarker.current = null
		clearOperatorMarker()
      await refreshOnboarding()
      await refreshAccess()
      navigate('/dashboard', { replace: true })
    } catch (e) {
      fail(e)
      await refreshOnboarding()
    } finally {
      setBusy(false)
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

	const o = onboarding
  const failedBeforeEdge = o.state === 'ONBOARDING_STATE_FAILED'
  const current = probeFailed || (failedBeforeEdge && stage === null) ? 'model' : (stage?.key ?? activeStep(o, assetsDone))
	const modelConfigChanged = baseUrl !== o.baseUrl || model !== o.model || dialect !== normalizeDialect(o.dialect)
	const canSave = secret !== '' || (o.hasSecret && !modelConfigChanged)
  const showStatus = (key: StageKey) => current === key && (stage?.key === key || (stage === null && key !== 'edge'))

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
          <div className="flex items-center gap-2">
            <Button size="sm" radius="md" variant="light" onPress={() => void logout()}>
              退出登录
            </Button>
          </div>
        </header>

        <section className="fs-panel" aria-label="初次配置引导">
          <ol className="yf-setup-steps">
            {STEPS.map((s, i) => {
              const st = stepStatus(s.key, current)
              return (
                <li key={s.key} className={`yf-setup-step yf-setup-step--${st}`}>
                  <span className="yf-setup-step-n">{i + 1}</span>
                  <span>{s.label}</span>
                </li>
              )
            })}
          </ol>
        </section>

        {current === 'model' && (
          <section className="fs-panel" aria-label="配置模型">
            <div className="fs-panel-head">
              <div>
                <p className="fs-panel-title">配置模型</p>
                <p className="fs-panel-sub">保存后自动探测连通，不是单独点探针</p>
              </div>
            </div>
            <div className="flex flex-col gap-3 px-4 py-4">
              <Input label="模型端点" radius="md" value={baseUrl} onValueChange={setBaseUrl} />
              <Input label="模型名" radius="md" value={model} onValueChange={setModel} />
              <Select
                label="模型方言"
                radius="md"
                selectedKeys={[dialect]}
                onChange={(e) => setDialect(normalizeDialect(e.target.value))}
              >
                {MODEL_DIALECTS.map((d) => (
                  <SelectItem key={d.value}>{d.label}</SelectItem>
                ))}
              </Select>
              <Input
                label="模型密钥"
                type="password"
                radius="md"
                value={secret}
                onValueChange={setSecret}
                autoComplete="off"
                description={o.hasSecret ? `已保存 ${o.secretHint}，覆盖请重新输入` : '只写不回读'}
              />
	              <Button color="primary" radius="md" isLoading={busy} isDisabled={!canSave} onPress={() => void saveAndProbe()}>
	                {secret === '' && o.hasSecret && !modelConfigChanged ? '重新探测连通' : '保存并探测连通'}
              </Button>
            </div>
          </section>
        )}

        {showStatus('probe') && (
          <SetupStatusCard
            title="探测连通"
            sub="不是单独点探针"
            running="正在向模型端点发送探测，请稍候"
            done="模型端点可用"
            ok={stage?.key === 'probe' && stage.ok}
            failed={stage?.key === 'probe' && stage.failed}
            onRetry={() => void runFrom('probe')}
          />
        )}

        {current === 'specification' && (
          <section className="fs-panel" aria-label="提交部署规格">
            <div className="fs-panel-head">
              <div>
                <p className="fs-panel-title">提交部署规格</p>
                <p className="fs-panel-sub">Brain 只签发监听计划、基线世代和模型档案，不启动进程</p>
              </div>
            </div>
            <div className="flex flex-col gap-3 px-4 py-4">
              <p className="text-sm text-[#8b98a1]">浏览器不收集业务证书、私钥或单元引导令牌；技术人员稍后在目标主机配置这些文件。</p>
              <Input label="Edge 单元标识" radius="md" value={unitId} onValueChange={setUnitId} />
              <Input label="资产标识" radius="md" value={assetId} onValueChange={setAssetId} />
              <Select
                label="入口姿态"
                radius="md"
                selectedKeys={[posture]}
                onChange={(e) => setPosture(e.target.value as EdgePosture)}
              >
                <SelectItem key="INGRESS_POSTURE_REVERSE_PROXY">反向代理</SelectItem>
                <SelectItem key="INGRESS_POSTURE_EXT_AUTHZ">Envoy 外部授权</SelectItem>
              </Select>
              <Input label="流量键" radius="md" value={trafficKey} onValueChange={setTrafficKey} description="同一条客户流量的稳定标识" />
              <Input label="监听地址" radius="md" value={listenAddress} onValueChange={setListenAddress} description="默认 :18080" />
              <Input
                label="可信代理网段"
                radius="md"
                value={trustedProxyCidrs}
                onValueChange={setTrustedProxyCidrs}
                placeholder="10.0.0.0/8, 2001:db8::/32"
                description="可选；只有这些直接对端可提供 X-Forwarded-For"
              />
              {posture === 'INGRESS_POSTURE_REVERSE_PROXY' ? (
                <Input
                  label="真实上游地址"
                  radius="md"
                  value={upstreamUrl}
                  onValueChange={setUpstreamUrl}
                  placeholder="http://app:8080"
                  description="只接受绝对 HTTP 或 HTTPS 地址；不能使用 builtin"
                />
              ) : (
                <p className="text-sm leading-6 text-[#8b98a1]">
                  部署后把 Envoy 的外部授权 HTTP 服务指向该监听地址；业务传输层安全协议仍由客户入口终止，本表单不收业务上游或私钥。
                </p>
              )}
              <div className="border-t border-[#1d252a] pt-3">
                <p className="mb-3 text-xs text-[#8b98a1]">签名模型档案</p>
                <div className="flex flex-col gap-3">
                  <Input label="档案标识" radius="md" value={modelProfileId} onValueChange={setModelProfileId} />
                  <Input label="模型组" radius="md" value={modelGroup} onValueChange={setModelGroup} />
                  <Input label="模型类型" radius="md" value={modelType} onValueChange={setModelType} />
                  <Input label="模型版本" radius="md" value={modelVersion} onValueChange={setModelVersion} />
                  <Input label="告警阈值" type="number" radius="md" value={alertThreshold} onValueChange={setAlertThreshold} />
                  <Input label="复核下限" type="number" radius="md" value={reviewFloor} onValueChange={setReviewFloor} />
                  <Input
                    label="允许进入模型的请求头"
                    radius="md"
                    value={allowedHeaders}
                    onValueChange={setAllowedHeaders}
                    description="逗号或空格分隔；敏感鉴权头会被 Brain 拒绝"
                  />
                  <p className="text-xs leading-5 text-[#8b98a1]">
                    初始采样固定为五分钟窗口、每单元最多四个代表、同方法和路由只保留最高风险代表；最大正文 65536 字节。
                  </p>
                </div>
              </div>
              <div className="border-t border-[#1d252a] pt-3">
                <p className="mb-3 text-xs text-[#8b98a1]">Edge 模型输入缓存窗口</p>
                <div className="grid gap-3 md:grid-cols-3">
                  <Input
                    label="窗口条目数"
                    type="number"
                    min={1}
                    max={65536}
                    radius="md"
                    value={modelWindowItems}
                    onValueChange={setModelWindowItems}
                  />
                  <Input
                    label="窗口内存（MiB）"
                    type="number"
                    min={1}
                    max={256}
                    radius="md"
                    value={modelWindowMemoryMiB}
                    onValueChange={setModelWindowMemoryMiB}
                  />
                  <Input
                    label="最长排队（秒）"
                    type="number"
                    min={0.01}
                    max={300}
                    step={0.01}
                    radius="md"
                    value={modelWindowAgeSeconds}
                    onValueChange={setModelWindowAgeSeconds}
                  />
                </div>
                <p className="mt-2 text-xs leading-5 text-[#8b98a1]">
                  默认 4096 条、128 MiB、2 秒；超过目标 Edge 的本机硬上限时会自动收窄并报告降级。
                </p>
              </div>
              <Button
                color="primary"
                radius="md"
                isLoading={busy}
                isDisabled={
                  unitId.trim() === '' ||
                  assetId.trim() === '' ||
                  trafficKey.trim() === '' ||
                  listenAddress.trim() === '' ||
                  (posture === 'INGRESS_POSTURE_REVERSE_PROXY' && upstreamUrl.trim() === '')
                }
                onPress={() => void submitSpecification()}
              >
                确定并签发部署规格
              </Button>
            </div>
          </section>
        )}

        {current === 'edge' && (
          <section className="fs-panel" aria-label="人工安装 Edge">
            <div className="fs-panel-head">
              <div>
                <p className="fs-panel-title">人工安装 Edge</p>
                <p className="fs-panel-sub">安装、启动、升级和卸载都由技术人员负责</p>
              </div>
            </div>
            <div className="flex flex-col gap-4 px-4 py-4">
              <p className="text-sm leading-6 text-[#8b98a1]">
                目标单元 {o.localUnitId || unitId} · 资产 {o.localAssetId || assetId}。Edge 启动后会主动注册、拉取签名监听计划与世代；Brain 和贾维斯不会连接或拉起它。
              </p>
              <div>
                <p className="mb-2 text-xs text-[#8b98a1]">原生 Go 二进制</p>
                <pre className="overflow-x-auto rounded-lg bg-[#07090b] p-3 text-xs text-[#c7d2d9]">{`sudo install -m 0755 yufeng-edge /usr/local/bin/yufeng-edge
sudo systemctl enable --now yufeng-edge
# 服务配置须包含：-brain <https-brain-url> -unit ${o.localUnitId || unitId}
# 以及 -bootstrap-token-file、-pubkey、来源假名密钥和相互传输层安全协议证书`}</pre>
              </div>
              <div>
                <p className="mb-2 text-xs text-[#8b98a1]">Edge 与 ModelSide 一体 Docker Compose</p>
                <pre className="overflow-x-auto rounded-lg bg-[#07090b] p-3 text-xs text-[#c7d2d9]">{`YUFENG_EDGE_UNIT=${o.localUnitId || unitId} \\
YUFENG_MODELSIDE_ID=${o.localUnitId || unitId}-modelside \\
YUFENG_MODELSIDE_WEIGHTS_DIR=/srv/yufeng/models \\
docker compose -f deploy/compose.yaml -f deploy/compose.edge-modelside.yaml up -d edge modelside`}</pre>
              </div>
              <div className="yf-setup-status">
                {stage?.failed !== true && <SetupMark ok={stage?.ok === true || o.edgeReady} label={o.edgeReady ? 'Edge 已就绪' : '等待 Edge 主动心跳'} />}
                <p className="text-sm text-[#8b98a1]" aria-live="polite">
                  {stage?.failed ? '读取 Edge 状态失败，可以重试' : o.edgeReady ? 'Edge 已装载期望监听计划与资产世代' : '正在等待 Edge 主动注册并回执已装载版本'}
                </p>
                {stage?.failed && (
                  <Button color="primary" radius="md" onPress={() => void runFrom('edge')}>
                    重新检查
                  </Button>
                )}
              </div>
            </div>
          </section>
        )}

        {current === 'assets' && (
          <section className="fs-panel" aria-label="设置防御资产">
            <div className="fs-panel-head">
              <div>
                <p className="fs-panel-title">设置防御资产</p>
                <p className="fs-panel-sub">先确认本机资产，再按需登记更多旁路目标</p>
              </div>
            </div>
            <div className="flex flex-col gap-4 px-4 py-4">
              <p className="text-sm leading-6 text-[#8b98a1]">
                Edge 已经确认本机资产 {o.localAssetId || '（待注册）'}。把它写成你要保护的对象，也可以再加其它只走流量旁路的资产。本机那一条不能删。
              </p>
              <Input label="本机资产显示名" radius="md" value={localName} onValueChange={setLocalName} />
              <Select
                label="本机关键性"
                radius="md"
                selectedKeys={[localCrit]}
                onChange={(e) => setLocalCrit(e.target.value as Criticality)}
              >
                {EDITABLE_CRITICALITIES.map((c) => (
                  <SelectItem key={c}>{CRITICALITY_BADGE[c].label}</SelectItem>
                ))}
              </Select>
              <Button
                radius="md"
                variant="bordered"
                isLoading={busy}
                isDisabled={localName.trim() === '' || o.localAssetId === ''}
                onPress={() => void saveLocalAsset()}
              >
                保存本机资产
              </Button>
              <div className="border-t border-[#1d252a] pt-3">
                <p className="mb-2 text-xs text-[#8b98a1]">已登记 {assetItems.length} 条</p>
                {assetItems.map((item) => {
                  const local = item.asset.id === o.localAssetId
                  return (
                    <div key={item.asset.id} className="fs-row">
                      <div className="min-w-0 flex-1">
                        <p className="truncate text-sm">{item.asset.displayName}</p>
                        <p className="fs-mono text-xs text-[#8b98a1]">
                          {item.asset.id} · {ACCESS_MODE_LABEL[item.asset.accessMode]}
                          {local ? ' · 本机' : ''}
                        </p>
                      </div>
                      {!local && (
                        <Button size="sm" radius="md" variant="light" color="danger" isDisabled={busy} onPress={() => void removeDefenseAsset(item.asset.id)}>
                          删除
                        </Button>
                      )}
                    </div>
                  )
                })}
              </div>
              <Input label="新增资产显示名" radius="md" value={assetName} onValueChange={setAssetName} />
              <Select
                label="接入模式"
                radius="md"
                selectedKeys={[assetMode]}
                onChange={(e) => setAssetMode(e.target.value as AccessMode)}
              >
                {EDITABLE_ACCESS_MODES.map((m) => (
                  <SelectItem key={m}>{ACCESS_MODE_LABEL[m]}</SelectItem>
                ))}
              </Select>
              <Select
                label="关键性"
                radius="md"
                selectedKeys={[assetCrit]}
                onChange={(e) => setAssetCrit(e.target.value as Criticality)}
              >
                {EDITABLE_CRITICALITIES.map((c) => (
                  <SelectItem key={c}>{CRITICALITY_BADGE[c].label}</SelectItem>
                ))}
              </Select>
              <Button
                radius="md"
                variant="bordered"
                isLoading={busy}
                isDisabled={assetName.trim() === ''}
                onPress={() => void addDefenseAsset()}
              >
                登记资产
              </Button>
              <Button color="primary" radius="md" isDisabled={o.localAssetId === ''} onPress={() => setAssetsDone(true)}>
                下一步，授权值守账户
              </Button>
            </div>
          </section>
        )}

        {current === 'grant' && (
          <section className="fs-panel" aria-label="授权值守账户">
            <div className="fs-panel-head">
              <div>
                <p className="fs-panel-title">授权值守账户</p>
                <p className="fs-panel-sub">值班操作员：只批准上线，不提案</p>
              </div>
            </div>
            <div className="flex flex-col gap-3 px-4 py-4" aria-label="值守账户信息">
              <p className="text-sm leading-6 text-[#8b98a1]">
                贾维斯负责研判和提出策略。管理员完成引导后也没有上线权，必须另建值守账户，由值班的人批准策略上线。
                贾维斯是否在线只影响研判能力，不参与 Edge 安装、启动或探测。
              </p>
              <ul className="list-disc space-y-1 pl-5 text-sm leading-6 text-[#8b98a1]">
                <li>能做：登录控制台，对本机防御资产把已观察过的策略推到全量生效</li>
                <li>不能做：自己提案、改模型配置、管理用户、增删改资产</li>
                <li>范围：只绑本机资产 {o.localAssetId || '（Edge 注册后填入）'}，管不到其它资产</li>
                <li>研判能力：{o.jarvisOnline ? '贾维斯在线' : '贾维斯尚未在线，完成引导前仍需启动'}</li>
              </ul>
              <Input label="值守用户名" radius="md" value={username} onValueChange={setUsername} />
              <Input label="值守密码" type="password" radius="md" value={password} onValueChange={setPassword} />
              <Input label="显示名" radius="md" value={displayName} onValueChange={setDisplayName} />
              <p className="text-xs text-[#8b98a1]" aria-label="值守权限说明">
                权限只覆盖本机防御资产上的全量上线批准
              </p>
              {missing.length > 0 && <p className="text-xs text-[#f1be5b]">未满足谓词：{missing.join(', ')}</p>}
              <Button
                color="primary"
                radius="md"
                isLoading={busy}
                isDisabled={username.trim() === '' || password === '' || displayName.trim() === '' || o.localAssetId === ''}
                onPress={() => void createGrantComplete()}
              >
                创建值守账户
              </Button>
            </div>
          </section>
        )}

        {(error !== null || o.lastError !== '') && (
          <p role="alert" className="text-xs text-[#ff746c]">
            {error ?? o.lastError}
          </p>
        )}
      </div>
    </div>
  )
}
