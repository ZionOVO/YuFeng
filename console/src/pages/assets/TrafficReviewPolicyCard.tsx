import { Button } from '@heroui/react'
import { useMemo, useState } from 'react'
import type { ConsoleClient } from '../../api/client'
import { isApiError } from '../../api/errors'
import type { TrafficReviewMode, TrafficReviewPolicyStatus, UnitProjection } from '../../api/types'
import { useAsyncData } from '../../components/useAsyncData'
import { Badge, StateView } from '../../components/ui'

const MODES: Array<{ value: TrafficReviewMode; label: string; description: string }> = [
  { value: 'TRAFFIC_REVIEW_MODE_OFF', label: '关闭', description: '不生成统计窗、案件或证据' },
  { value: 'TRAFFIC_REVIEW_MODE_STATISTICS_ONLY', label: '只统计', description: '只上传有界五分钟统计窗' },
  { value: 'TRAFFIC_REVIEW_MODE_REDACTED_CASES', label: '脱敏案件', description: '允许中台按候选建立脱敏案件' },
  { value: 'TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL', label: '人工证据', description: '人工逐案批准后才分析受控证据' },
  { value: 'TRAFFIC_REVIEW_MODE_SHADOW_CANDIDATES', label: '仅记录候选', description: '疑似漏报通过确定性门禁后可建立仅记录防护策略' },
]

function modeIndex(mode: TrafficReviewMode): number {
  const index = MODES.findIndex((item) => item.value === mode)
  return index < 0 ? 0 : index
}

function edgeLoaded(status: TrafficReviewPolicyStatus, units: UnitProjection[]): boolean {
  if (status.generationSeq === '0') return true
  const edges = units.filter((unit) => unit.kind.toLowerCase() === 'edge')
  return edges.length > 0 && edges.every((unit) => {
    try {
      return BigInt(unit.currentGenerationSeq ?? '0') >= BigInt(status.generationSeq)
    } catch {
      return false
    }
  })
}

export function TrafficReviewPolicyCard({
  assetId,
  units,
  canWrite,
  client,
  onRefreshAsset,
}: {
  assetId: string
  units: UnitProjection[]
  canWrite: boolean
  client: ConsoleClient
  onRefreshAsset: () => void
}) {
  const request = useAsyncData(() => client.getTrafficReviewPolicy(assetId), [client, assetId], false)
  const [localStatus, setLocalStatus] = useState<{ assetId: string; value: TrafficReviewPolicyStatus } | null>(null)
  const [selection, setSelection] = useState<{ generationId: string; value: TrafficReviewMode } | null>(null)
  const [busy, setBusy] = useState(false)
  const [writeError, setWriteError] = useState<string | null>(null)
  const status = localStatus?.assetId === assetId ? localStatus.value : request.data
  const target = status !== null && selection?.generationId === status.generationId ? selection.value : status?.policy.mode ?? 'TRAFFIC_REVIEW_MODE_OFF'

  const allowedModes = useMemo(() => {
    if (status === null) return MODES.slice(0, 1)
    return MODES.slice(0, Math.min(MODES.length, modeIndex(status.policy.mode) + 2))
  }, [status])

  const refresh = () => {
    setLocalStatus(null)
    setSelection(null)
    request.reload()
    onRefreshAsset()
  }

  const apply = async () => {
    if (status === null || target === status.policy.mode) return
    setBusy(true)
    setWriteError(null)
    try {
      const next = await client.updateTrafficReviewPolicy(assetId, target, status.generationId)
      setLocalStatus({ assetId, value: next })
      setSelection(null)
      onRefreshAsset()
    } catch (error) {
      setWriteError(isApiError(error) ? `${error.message}（${error.code}）` : '更新失败，请重试')
    } finally {
      setBusy(false)
    }
  }

  if (request.status === 'error') {
    return <StateView kind="error" title="流量审查策略" message={request.error?.message ?? '读取失败'} onRetry={refresh} />
  }
  if (status === null) return <StateView kind="loading" title="流量审查策略" />

  const current = MODES[modeIndex(status.policy.mode)]
  const loaded = edgeLoaded(status, units)
  return (
    <section className="fs-panel" aria-label="流量审查策略">
      <div className="fs-panel-head flex-wrap gap-2">
        <div>
          <p className="fs-panel-title">流量审查策略</p>
          <p className="fs-panel-sub">签名资产世代 · 固定安全上限</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Badge label={current.label} tone={status.policy.mode === 'TRAFFIC_REVIEW_MODE_OFF' ? 'mute' : 'green'} />
          <Badge label={!status.edgeSupported ? 'Edge 不支持' : loaded ? 'Edge 已生效' : '等待 Edge 装载'} tone={!status.edgeSupported ? 'red' : loaded ? 'green' : 'amber'} />
        </div>
      </div>
      <div className="grid gap-4 p-4 lg:grid-cols-[minmax(0,1fr)_minmax(16rem,0.72fr)]">
        <div className="min-w-0 space-y-3">
          <p className="text-sm text-[#c4d0ce]">{current.description}</p>
          <dl className="yf-kv">
            <dt>目标世代</dt><dd className="fs-mono break-all">{status.generationId || '尚未生成'}</dd>
            <dt>世代序号</dt><dd className="fs-mono">{status.generationSeq}</dd>
            <dt>策略摘要</dt><dd className="fs-mono break-all">{status.policyDigest || '—'}</dd>
            <dt>统计与路由</dt><dd>{status.policy.windowSeconds} 秒 · 近似前 {status.policy.topRouteCells} 个高频组合</dd>
            <dt>候选与证据</dt><dd>每窗 {status.policy.maxCandidatesPerWindow} 个 · 单候选 {Math.round(status.policy.maxEvidenceBytes / 1024)} KiB</dd>
            <dt>本地证据库</dt><dd>{Math.round(Number(status.policy.vaultMaxBytes) / 1024 / 1024)} MiB · {Math.round(Number(status.policy.evidenceTtlSeconds) / 3600)} 小时</dd>
          </dl>
        </div>
        <div className="min-w-0 rounded-xl border border-white/10 bg-black/10 p-3">
          <label className="block text-xs font-semibold text-[#b8c9c7]" htmlFor="traffic-review-target">目标阶段</label>
          <select
            id="traffic-review-target"
            className="mt-2 min-h-11 w-full rounded-lg border border-white/15 bg-[#102326] px-3 text-sm text-[#e6efed]"
            value={target}
            disabled={!canWrite || busy || !status.edgeSupported}
            onChange={(event) => setSelection({ generationId: status.generationId, value: event.target.value as TrafficReviewMode })}
          >
            {allowedModes.map((mode) => <option key={mode.value} value={mode.value}>{mode.label}</option>)}
          </select>
          <p className="mt-2 text-xs leading-5 text-[#839894]">启用只能逐级进行；降级可直接选择，关闭会立即发布新的失败关闭世代。</p>
          {writeError !== null && <p className="mt-2 text-xs text-danger" role="alert">{writeError}</p>}
          <div className="mt-3 flex flex-wrap gap-2">
            <Button color="primary" size="sm" isLoading={busy} isDisabled={!canWrite || target === status.policy.mode || !status.edgeSupported} onPress={apply}>发布目标阶段</Button>
            <Button size="sm" variant="light" onPress={refresh}>刷新装载状态</Button>
          </div>
        </div>
      </div>
    </section>
  )
}
