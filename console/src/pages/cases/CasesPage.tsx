import { Select, SelectItem } from '@heroui/react'
import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import type { InvestigationCaseState } from '../../api/types'
import { PageSizeMax } from '../../api/limits'
import { hasCode } from '../../api/errors'
import { useAuth } from '../../auth/useAuth'
import { CaseCard } from '../../components/cases/CaseCards'
import { CaseWorkspace } from '../../components/cases/CaseWorkspace'
import { StateView, TokenPager } from '../../components/ui'
import { useAsyncData } from '../../components/useAsyncData'

const PAGE_SIZE = 25

const STATES: Array<{ key: InvestigationCaseState | 'all'; label: string }> = [
  { key: 'all', label: '全部状态' },
  { key: 'INVESTIGATION_CASE_STATE_OPEN', label: '待编排' },
  { key: 'INVESTIGATION_CASE_STATE_WAITING_EVIDENCE_APPROVAL', label: '等待证据批准' },
  { key: 'INVESTIGATION_CASE_STATE_QUEUED', label: '已排队' },
  { key: 'INVESTIGATION_CASE_STATE_INVESTIGATING', label: '调查中' },
  { key: 'INVESTIGATION_CASE_STATE_FINDING_READY', label: '结论就绪' },
  { key: 'INVESTIGATION_CASE_STATE_SHADOW_OBSERVING', label: 'Shadow 观察' },
  { key: 'INVESTIGATION_CASE_STATE_RESOLVED', label: '已解决' },
  { key: 'INVESTIGATION_CASE_STATE_FAILED', label: '失败' },
  { key: 'INVESTIGATION_CASE_STATE_EVIDENCE_EXPIRED', label: '证据过期' },
]

export function CasesPage() {
  const { client } = useAuth()
  const [params, setParams] = useSearchParams()
  const [state, setState] = useState<InvestigationCaseState | 'all'>('all')
  const assetId = params.get('assetId') ?? ''
  const modules = useAsyncData(() => client.listModules(), [client])
  const assets = useAsyncData(() => client.listAssets({}, { pageSize: PageSizeMax }), [client])
  const [moduleId, setModuleId] = useState('all')
  const [tokens, setTokens] = useState<string[]>([''])
  const [pageIndex, setPageIndex] = useState(0)
  const resetPaging = () => {
    setTokens([''])
    setPageIndex(0)
  }
  const clearCaseSelection = () => {
    const next = new URLSearchParams(params)
    next.delete('caseId')
    setParams(next)
  }
  const cases = useAsyncData(() => client.listCases({
    assetId: assetId === '' ? undefined : assetId,
    moduleId: moduleId === 'all' ? undefined : moduleId,
    state: state === 'all' ? undefined : state,
  }, { pageSize: PAGE_SIZE, pageToken: tokens[pageIndex] }), [client, assetId, moduleId, state, pageIndex])
  const selected = params.get('caseId') ?? cases.data?.items[0]?.caseId ?? ''
  const activeModules = useMemo(() => (modules.data ?? []).filter((item) => item.active), [modules.data])
  const assetOptions = useMemo(() => [{ id: 'all', displayName: '全部资产' }, ...(assets.data?.items ?? []).map((item) => item.asset)], [assets.data])
  const selectAsset = (nextAssetId: string) => {
    resetPaging()
    const next = new URLSearchParams(params)
    next.delete('caseId')
    if (nextAssetId === 'all') next.delete('assetId')
    else next.set('assetId', nextAssetId)
    setParams(next)
  }
  const selectCase = (caseId: string) => {
    const next = new URLSearchParams(params)
    next.set('caseId', caseId)
    setParams(next)
  }
  const goNext = () => {
    if (cases.data === null || cases.data.nextPageToken === '') return
    const nextPageToken = cases.data.nextPageToken
    setTokens((current) => [...current.slice(0, pageIndex + 1), nextPageToken])
    setPageIndex((current) => current + 1)
    clearCaseSelection()
  }

  if (cases.status === 'error' && cases.error !== null) {
    if (hasCode(cases.error, 'permission_denied')) return <StateView kind="denied" />
    return <StateView kind="error" message={cases.error.message} onRetry={cases.reload} />
  }
  return (
    <div className="grid gap-4 xl:grid-cols-[360px_minmax(0,1fr)]">
      <aside className="space-y-3" aria-label="案件列表">
        {modules.status === 'error' && modules.error !== null && <StateView kind="error" message={`模块目录加载失败：${modules.error.message}`} onRetry={modules.reload} />}
        {assets.status === 'error' && assets.error !== null && <StateView kind="error" message={`资产筛选项加载失败：${assets.error.message}`} onRetry={assets.reload} />}
		<Select items={assetOptions} aria-label="按资产筛选案件" selectedKeys={[assetId || 'all']} onSelectionChange={(keys) => selectAsset(String([...keys][0] ?? 'all'))} size="sm" radius="md">
          {(item) => <SelectItem key={item.id}>{item.displayName}</SelectItem>}
        </Select>
        <div className="grid grid-cols-2 gap-2">
          <Select items={[{ moduleId: 'all', displayName: '全部模块' }, ...activeModules]} aria-label="模块" selectedKeys={[moduleId]} onSelectionChange={(keys) => {
            setModuleId(String([...keys][0] ?? 'all'))
            resetPaging()
            clearCaseSelection()
          }} size="sm" radius="md">
            {(module) => <SelectItem key={module.moduleId}>{module.displayName}</SelectItem>}
          </Select>
          <Select aria-label="案件状态" selectedKeys={[state]} onSelectionChange={(keys) => {
            setState(String([...keys][0] ?? 'all') as InvestigationCaseState | 'all')
            resetPaging()
            clearCaseSelection()
          }} size="sm" radius="md">
            {STATES.map((item) => <SelectItem key={item.key}>{item.label}</SelectItem>)}
          </Select>
        </div>
        {cases.data === null ? <StateView kind="loading" /> : cases.data.items.length === 0 ? <StateView kind="empty" title="暂无案件" message="流量统计仍在边缘有界运行；只有高价值候选会形成案件。" /> : cases.data.items.map((item) => (
          <CaseCard key={item.caseId} item={item} selected={item.caseId === selected} onSelect={() => selectCase(item.caseId)} />
        ))}
        {cases.data !== null && cases.data.items.length > 0 && (
          <TokenPager page={pageIndex + 1} hasPrev={pageIndex > 0} hasNext={cases.data.nextPageToken !== ''} onPrev={() => {
            setPageIndex((current) => Math.max(0, current - 1))
            clearCaseSelection()
          }} onNext={goNext} />
        )}
        {assets.data !== null && assets.data.nextPageToken !== '' && <p className="text-xs text-warning">资产筛选项超过 {PageSizeMax} 个，请先缩小授权范围。</p>}
      </aside>
      <div className="min-w-0">{selected === '' ? <StateView kind="empty" title="选择一个案件" message="案件是 Agent 的最小工作单位。" /> : <CaseWorkspace caseId={selected} />}</div>
    </div>
  )
}
