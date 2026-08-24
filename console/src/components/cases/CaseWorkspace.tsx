import { Button, Select, SelectItem, Textarea } from '@heroui/react'
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { canOnAsset } from '../../api/access'
import { hasCode } from '../../api/errors'
import { useAuth } from '../../auth/useAuth'
import { useAsyncData } from '../useAsyncData'
import { Badge, StateView } from '../ui'
import { formatTime } from '../format'
import { EvidenceApprovalCard, RunProgressCard, ShadowCandidateCard } from './CaseCards'
import { mergeCaseActivities } from './caseActivity'
import { moduleRenderer } from './moduleRegistry'
import type { CaseActivity, CaseResolution, InvestigationCase } from '../../api/types'

const RESOLUTIONS: Array<{ key: CaseResolution; label: string }> = [
  { key: 'CASE_RESOLUTION_CONFIRMED_MALICIOUS', label: '确认恶意' },
  { key: 'CASE_RESOLUTION_FALSE_POSITIVE', label: '确认误报' },
  { key: 'CASE_RESOLUTION_BENIGN', label: '良性流量' },
  { key: 'CASE_RESOLUTION_INSUFFICIENT_EVIDENCE', label: '证据不足' },
  { key: 'CASE_RESOLUTION_EVIDENCE_DENIED', label: '证据访问被拒绝' },
  { key: 'CASE_RESOLUTION_SHADOW_PUBLISHED', label: '已发布 Shadow 防护策略' },
  { key: 'CASE_RESOLUTION_FAILED', label: '调查失败' },
]

const TERMINAL_STATES = new Set([
  'INVESTIGATION_CASE_STATE_RESOLVED',
  'INVESTIGATION_CASE_STATE_FAILED',
  'INVESTIGATION_CASE_STATE_EVIDENCE_EXPIRED',
])

export function CaseWorkspace(props: { caseId: string; compact?: boolean }) {
  return <CaseWorkspaceContent key={props.caseId} {...props} />
}

function CaseWorkspaceContent({ caseId, compact = false }: { caseId: string; compact?: boolean }) {
  const { client, access } = useAuth()
  const detail = useAsyncData(() => client.getCase(caseId), [client, caseId], false)
  const reloadDetail = detail.reload
  const [activities, setActivities] = useState<CaseActivity[]>([])
  const [activityLoaded, setActivityLoaded] = useState(false)
  const [activityError, setActivityError] = useState<string | null>(null)
  const activityCursor = useRef('0')

  useEffect(() => {
    let cancelled = false
    const loop = async () => {
      while (!cancelled) {
        try {
          const before = activityCursor.current
          const page = await client.pollCaseActivities({ caseId, afterSequence: before, longPollSeconds: 20 })
          if (cancelled) return
          if (page.activities.length > 0) {
            setActivities((current) => mergeCaseActivities(current, page.activities))
            reloadDetail()
          }
          activityCursor.current = page.nextAfterSequence || activityCursor.current
          setActivityLoaded(true)
          setActivityError(null)
          if (page.activities.length === 0 && activityCursor.current === before) {
            await new Promise((resolve) => window.setTimeout(resolve, 1000))
          }
        } catch (cause) {
          if (cancelled) return
          setActivityLoaded(true)
          setActivityError(cause instanceof Error ? cause.message : '案件活动读取失败')
          await new Promise((resolve) => window.setTimeout(resolve, 5000))
        }
      }
    }
    void loop()
    return () => {
      cancelled = true
    }
  }, [caseId, client, reloadDetail])

  if (detail.status === 'error' && detail.error !== null) {
    if (hasCode(detail.error, 'permission_denied')) return <StateView kind="denied" message="案件不在当前授权资产范围内" />
    if (hasCode(detail.error, 'not_found')) return <StateView kind="error" message="案件不存在或已删除" onRetry={detail.reload} />
    return <StateView kind="error" message={detail.error.message} onRetry={detail.reload} />
  }
  if (detail.data === null) return <StateView kind="loading" />
  const item = detail.data
  const renderer = moduleRenderer(item.moduleId)
  return (
    <section className={compact ? 'p-4' : 'fs-panel'} aria-label="案件工作台">
      <div className={compact ? '' : 'fs-panel-head'}>
        <div>
          <div className="flex flex-wrap items-center gap-2"><Badge label={`优先级 ${item.priority}`} tone={item.priority >= 80 ? 'red' : 'amber'} /><Badge label={item.moduleId} tone="mute" /></div>
          <h2 className="mt-2 text-lg font-semibold text-[#e5f2f0]">{item.title}</h2>
          <p className="mt-1 text-sm text-[#8fa2a5]">{item.summary}</p>
        </div>
        <Link className="fs-mono text-xs text-[#62e6a7] underline" to={`/assets/${item.assetId}`}>{item.assetId}</Link>
      </div>

      {renderer === undefined && (
        <p className="m-4 rounded-lg border border-[#705c2e] bg-[#231e12] p-3 text-xs text-[#d9bd75]">当前模块使用通用案件视图；升级客户端可获得专用视图。</p>
      )}
      <div className="grid gap-3 p-4">
        {renderer?.finding?.(item)}
        {canOnAsset(access, 'case.manage', item.assetId) && <CaseManagementPanel item={item} onChanged={reloadDetail} />}
        {activityError !== null && <StateView kind="error" title="案件活动暂不可用" message={activityError} />}
        {activities.map((activity) => {
          if (activity.kind === 'CASE_ACTIVITY_KIND_EVIDENCE_REQUESTED') return <EvidenceApprovalCard key={activity.sequence} approvalId={activity.refId} onDecided={reloadDetail} />
          if (activity.kind === 'CASE_ACTIVITY_KIND_APPROVAL_REQUESTED') return <EvidenceApprovalCard key={activity.sequence} approvalId={activity.refId} onDecided={reloadDetail} />
          if (activity.kind === 'CASE_ACTIVITY_KIND_RUN_PROGRESS') return <RunProgressCard key={activity.sequence} summary={activity.summary} occurredAt={activity.occurredAt} />
          if (activity.kind === 'CASE_ACTIVITY_KIND_SHADOW_CANDIDATE') return <ShadowCandidateCard key={activity.sequence} releaseId={item.shadowReleaseId} summary={activity.summary} />
          return (
            <div key={activity.sequence} className="flex gap-3 border-l border-[#2b4449] py-1 pl-3 text-xs">
              <span className="fs-mono shrink-0 text-[#71888c]">{formatTime(activity.occurredAt)}</span><span className="text-[#b8c9c7]">{activity.summary}</span>
            </div>
          )
        })}
        {activityLoaded && activityError === null && activities.length === 0 && <StateView kind="empty" title="尚无案件活动" message="Jarvis 接手后，审批、调查与结论会按引用出现在这里。" />}
      </div>
    </section>
  )
}

function CaseManagementPanel({ item, onChanged }: { item: InvestigationCase; onChanged: () => void }) {
  const { client } = useAuth()
  const [resolution, setResolution] = useState<CaseResolution>(item.resolution && item.resolution !== 'CASE_RESOLUTION_UNSPECIFIED' ? item.resolution : 'CASE_RESOLUTION_INSUFFICIENT_EVIDENCE')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState<'resolve' | 'feedback' | 'reopen' | null>(null)
  const [message, setMessage] = useState<string | null>(null)
  const terminal = TERMINAL_STATES.has(item.state)

  const perform = async (kind: 'resolve' | 'feedback' | 'reopen') => {
    setBusy(kind)
    setMessage(null)
    try {
      if (kind === 'resolve') await client.resolveCase({ caseId: item.caseId, resolution, note })
      else if (kind === 'feedback') await client.recordCaseFeedback({ caseId: item.caseId, resolution, note })
      else await client.reopenCase({ caseId: item.caseId, note })
      setNote('')
      setMessage(kind === 'resolve' ? '案件已解决' : kind === 'feedback' ? '人工反馈已记录' : '案件已重新打开')
      onChanged()
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : '案件操作失败')
    } finally {
      setBusy(null)
    }
  }

  return (
    <section className="rounded-xl border border-[#294147] bg-[#0b171a] p-4" aria-label="案件人工处置">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div><p className="text-sm font-semibold text-[#e5f2f0]">人工处置</p><p className="mt-1 text-xs text-[#8fa2a5]">反馈影响后续风险排序；解决或重开会写入案件活动与审计账。</p></div>
        {item.resolution && item.resolution !== 'CASE_RESOLUTION_UNSPECIFIED' && <Badge label={RESOLUTIONS.find((entry) => entry.key === item.resolution)?.label ?? item.resolution} tone="mute" />}
      </div>
      <div className="mt-3 grid min-w-0 gap-3 md:grid-cols-[minmax(11rem,0.45fr)_minmax(0,1fr)]">
        <Select aria-label="人工处置结论" selectedKeys={[resolution]} onSelectionChange={(keys) => setResolution(String([...keys][0] ?? resolution) as CaseResolution)} size="sm" radius="md">
          {RESOLUTIONS.map((entry) => <SelectItem key={entry.key}>{entry.label}</SelectItem>)}
        </Select>
        <Textarea aria-label="案件处置说明" value={note} onValueChange={setNote} maxLength={2048} minRows={2} placeholder="可选说明，不要粘贴原始流量" radius="md" />
      </div>
      {message !== null && <p className="mt-2 break-words text-xs text-[#9bb0b2]" role="status">{message}</p>}
      <div className="mt-3 flex flex-wrap justify-end gap-2">
        <Button size="sm" variant="bordered" isDisabled={busy !== null} onPress={() => void perform('feedback')}>记录反馈</Button>
        {terminal
          ? <Button size="sm" color="primary" isLoading={busy === 'reopen'} isDisabled={busy !== null && busy !== 'reopen'} onPress={() => void perform('reopen')}>重新打开</Button>
          : <Button size="sm" color="primary" isLoading={busy === 'resolve'} isDisabled={busy !== null && busy !== 'resolve'} onPress={() => void perform('resolve')}>解决案件</Button>}
      </div>
    </section>
  )
}
