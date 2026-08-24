import { Button, Input } from '@heroui/react'
import { useState } from 'react'
import type { ConsoleClient } from '../../api/client'
import { isApiError } from '../../api/errors'
import type { ModelIngressDegradationReason, ModelIngressWindow, ModelIngressWindowState, UnitProjection } from '../../api/types'
import { useAsyncData } from '../../components/useAsyncData'
import { Badge, StateView } from '../../components/ui'

const STATE_LABEL: Record<ModelIngressWindowState, string> = {
  MODEL_INGRESS_WINDOW_STATE_UNSPECIFIED: '状态未知',
  MODEL_INGRESS_WINDOW_STATE_APPLIED: '已生效',
  MODEL_INGRESS_WINDOW_STATE_DEGRADED: '本机收窄',
  MODEL_INGRESS_WINDOW_STATE_CONVERGING: '等待收敛',
  MODEL_INGRESS_WINDOW_STATE_DISABLED: '旁路关闭',
}

const REASON_LABEL: Record<ModelIngressDegradationReason, string> = {
  MODEL_INGRESS_DEGRADATION_REASON_UNSPECIFIED: '未知原因',
  MODEL_INGRESS_DEGRADATION_REASON_MAX_ITEMS: '条目硬上限',
  MODEL_INGRESS_DEGRADATION_REASON_MAX_RETAINED_BYTES: '保留字节硬上限',
  MODEL_INGRESS_DEGRADATION_REASON_MAX_QUEUE_AGE: '排队年龄硬上限',
}

function durationSeconds(value: string): number {
  const match = /^([0-9]+(?:\.[0-9]+)?)s$/.exec(value)
  return match === null ? 0 : Number(match[1])
}

function durationValue(seconds: number): string {
  return `${Math.round(seconds * 1000) / 1000}s`
}

function mib(value: string): number {
  return Number(value) / 1024 / 1024
}

function formatWindow(window: ModelIngressWindow | undefined): string {
  if (window === undefined) return '—'
  return `${window.maxItems} 条 · ${mib(window.maxRetainedBytes).toFixed(0)} MiB · ${durationSeconds(window.maxQueueAge)} 秒`
}

function ModelIngressUnitWindow({
  assetId,
  unit,
  canWrite,
  client,
  onRefreshAsset,
}: {
  assetId: string
  unit: UnitProjection
  canWrite: boolean
  client: ConsoleClient
  onRefreshAsset: () => void
}) {
  const request = useAsyncData(() => client.getModelIngressWindow(assetId, unit.unitId), [client, assetId, unit.unitId], false)
  const [local, setLocal] = useState<typeof request.data>(null)
  const [draft, setDraft] = useState<{ version: string; items: string; bytesMiB: string; ageSeconds: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [writeError, setWriteError] = useState<string | null>(null)
  const status = local ?? request.data

  if (request.status === 'error') {
    return <StateView kind="error" title={unit.unitId} message={request.error?.message ?? '窗口读取失败'} onRetry={request.reload} />
  }
  if (status === null) return <StateView kind="loading" title={unit.unitId} />

  const base = {
    version: status.desiredListenPlanVersion,
    items: String(status.desired.maxItems),
    bytesMiB: String(Math.round(mib(status.desired.maxRetainedBytes))),
    ageSeconds: String(durationSeconds(status.desired.maxQueueAge)),
  }
  const values = draft?.version === status.desiredListenPlanVersion ? draft : base
  const updateDraft = (patch: Partial<typeof base>) => setDraft({ ...values, ...patch })
  const items = Number(values.items)
  const bytesMiB = Number(values.bytesMiB)
  const ageSeconds = Number(values.ageSeconds)
  const valid = Number.isInteger(items) && items >= 1 && items <= 65536 && Number.isFinite(bytesMiB) && bytesMiB >= 1 && bytesMiB <= 256 && Number.isFinite(ageSeconds) && ageSeconds >= 0.01 && ageSeconds <= 300
  const desired: ModelIngressWindow = {
    maxItems: items,
    maxRetainedBytes: String(Math.round(bytesMiB * 1024 * 1024)),
    maxQueueAge: durationValue(ageSeconds),
  }
  const changed = valid && (desired.maxItems !== status.desired.maxItems || desired.maxRetainedBytes !== status.desired.maxRetainedBytes || desired.maxQueueAge !== status.desired.maxQueueAge)
  const health = unit.producerHealth
  const drops = health.modelIngressDrops

  const apply = async () => {
    if (!changed) return
    setBusy(true)
    setWriteError(null)
    try {
      const next = await client.updateModelIngressWindow(assetId, unit.unitId, desired, status.desiredListenPlanVersion)
      setLocal(next)
      setDraft(null)
      onRefreshAsset()
    } catch (error) {
      setWriteError(isApiError(error) ? `${error.message}（${error.code}）` : '更新失败，请重试')
    } finally {
      setBusy(false)
    }
  }

  const tone = status.state === 'MODEL_INGRESS_WINDOW_STATE_APPLIED' ? 'green' : status.state === 'MODEL_INGRESS_WINDOW_STATE_DISABLED' ? 'mute' : 'amber'
  return (
    <div className="rounded-xl border border-white/10 bg-black/10 p-4" data-testid={`model-ingress-${unit.unitId}`}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div><p className="fs-mono text-sm text-[#dce8e6]">{unit.unitId}</p><p className="mt-1 text-xs text-[#839894]">监听计划 {status.appliedListenPlanVersion} / {status.desiredListenPlanVersion}</p></div>
        <Badge label={STATE_LABEL[status.state]} tone={tone} />
      </div>
      <dl className="yf-kv mt-3">
        <dt>中央期望</dt><dd>{formatWindow(status.desired)}</dd>
        <dt>实际窗口</dt><dd>{formatWindow(status.effective)}</dd>
        <dt>排队 / 在途</dt><dd>{health.modelIngressQueuedItems} / {health.modelIngressInFlightItems} 条 · {mib(health.modelIngressQueuedBytes).toFixed(1)} / {mib(health.modelIngressInFlightBytes).toFixed(1)} MiB</dd>
        <dt>最老排队</dt><dd>{health.modelIngressOldestAgeMillis} 毫秒</dd>
        <dt>丢弃</dt><dd>淘汰 {drops.evictedOldest} · 过期 {drops.expired} · 单项超限 {drops.itemTooLarge} · 在途 {drops.inFlightCapacity} · 准入预算 {drops.admissionBudget} · 传输 {drops.transportFailed} · ModelSide {drops.modelsideRejected}</dd>
        <dt>降级原因</dt><dd>{status.degradationReasons.length === 0 ? '—' : status.degradationReasons.map((reason) => REASON_LABEL[reason]).join('、')}</dd>
      </dl>
      <div className="mt-4 grid gap-3 md:grid-cols-3">
        <Input label="窗口条目数" type="number" min={1} max={65536} value={values.items} onValueChange={(value) => updateDraft({ items: value })} isDisabled={!canWrite || busy} />
        <Input label="窗口内存（MiB）" type="number" min={1} max={256} value={values.bytesMiB} onValueChange={(value) => updateDraft({ bytesMiB: value })} isDisabled={!canWrite || busy} />
        <Input label="排队年龄（秒）" type="number" min={0.01} max={300} step={0.01} value={values.ageSeconds} onValueChange={(value) => updateDraft({ ageSeconds: value })} isDisabled={!canWrite || busy} />
      </div>
      {!valid && <p className="mt-2 text-xs text-danger">范围：1–65536 条、1–256 MiB、0.01–300 秒。</p>}
      {writeError !== null && <p className="mt-2 text-xs text-danger" role="alert">{writeError}</p>}
      <div className="mt-3 flex flex-wrap gap-2">
        <Button color="primary" size="sm" isLoading={busy} isDisabled={!canWrite || !changed} onPress={apply}>签发窗口配置</Button>
        <Button size="sm" variant="light" onPress={() => { setLocal(null); setDraft(null); request.reload(); onRefreshAsset() }}>刷新状态</Button>
      </div>
    </div>
  )
}

export function ModelIngressWindowCard({ assetId, units, canWrite, client, onRefreshAsset }: { assetId: string; units: UnitProjection[]; canWrite: boolean; client: ConsoleClient; onRefreshAsset: () => void }) {
  const edges = units.filter((unit) => unit.kind.toLowerCase() === 'edge')
  return (
    <section className="fs-panel" aria-label="模型输入缓存窗口">
      <div className="fs-panel-head"><div><p className="fs-panel-title">模型输入缓存窗口</p><p className="fs-panel-sub">EDGE · 易失至多一次 · 保留最新</p></div></div>
      <div className="space-y-3 p-4">
        {edges.length === 0 ? <p className="text-sm text-[#839894]">当前资产没有绑定 Edge 单元。</p> : edges.map((unit) => <ModelIngressUnitWindow key={unit.unitId} assetId={assetId} unit={unit} canWrite={canWrite} client={client} onRefreshAsset={onRefreshAsset} />)}
      </div>
    </section>
  )
}
