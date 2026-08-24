// 事件流列表页：资产 / 判定 / 大类 / 关键词四维筛选，Table 展示，行点击进详情。
// 分页用不透明游标：pageToken 只回传不解析（docs/api.md §0.6），筛选变化回到第一页。

import { useState } from 'react'
import {
  Input,
  Select,
  SelectItem,
  Table,
  TableBody,
  TableCell,
  TableColumn,
  TableHeader,
  TableRow,
} from '@heroui/react'
import { Search } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import type { ListEventsFilter } from '../../api/client'
import { hasCode } from '../../api/errors'
import type { EventKind, Verdict } from '../../api/types'
import { useAuth } from '../../auth/useAuth'
import { formatTime } from '../../components/format'
import { Badge, StateView, TokenPager, VerdictBadge } from '../../components/ui'
import { useAsyncData } from '../../components/useAsyncData'

const PAGE_SIZE = 25

/** 事件大类 → 中文。 */
const KIND_LABEL: Record<EventKind, string> = {
  KIND_UNSPECIFIED: '未知',
  KIND_TRAFFIC: '流量',
  KIND_SENSOR: '传感',
  KIND_INTEL: '情报',
  KIND_AGENT: 'Agent',
}

const VERDICT_OPTIONS: { key: Verdict; label: string }[] = [
  { key: 'VERDICT_ALLOW', label: 'ALLOW' },
  { key: 'VERDICT_BLOCK', label: 'BLOCK' },
  { key: 'VERDICT_OBSERVE', label: 'OBSERVE' },
  { key: 'VERDICT_ESCALATE', label: 'ESCALATE' },
]

const KIND_OPTIONS: { key: EventKind; label: string }[] = [
  { key: 'KIND_TRAFFIC', label: '流量' },
  { key: 'KIND_SENSOR', label: '传感' },
  { key: 'KIND_INTEL', label: '情报' },
  { key: 'KIND_AGENT', label: 'Agent' },
]

export function EventsPage() {
  const { client } = useAuth()
  const navigate = useNavigate()
  const [assetKey, setAssetKey] = useState('all')
  const [verdictKey, setVerdictKey] = useState('all')
  const [kindKey, setKindKey] = useState('all')
  const [query, setQuery] = useState('')
  // tokens[i] 是第 i 页（0 起）的入参游标；第一页恒为空串
  const [tokens, setTokens] = useState<string[]>([''])
  const [pageIndex, setPageIndex] = useState(0)

  // 游标只对当前筛选条件有效：任何筛选变化都丢弃游标栈、回第一页
  const resetPaging = () => {
    setTokens([''])
    setPageIndex(0)
  }

  const filter: ListEventsFilter = {}
  if (assetKey !== 'all') filter.assetId = assetKey
  if (verdictKey !== 'all') filter.verdict = verdictKey as Verdict
  if (kindKey !== 'all') filter.kind = kindKey as EventKind
  if (query.trim() !== '') filter.query = query.trim()
  const filterKey = JSON.stringify(filter)

  // 资产选项一次性取全量（pageSize 上限 200），失败不阻塞事件列表
  const assets = useAsyncData(() => client.listAssets({}, { pageSize: 200 }), [])
  const { data, status, error, reload } = useAsyncData(
    () => client.listEvents(filter, { pageSize: PAGE_SIZE, pageToken: tokens[pageIndex] }),
    // filter 以序列化串参与比较；tokens 随 pageIndex 同步变化，无需单列
    // （exhaustive-deps 的 disable 在 useAsyncData 内部，调用点是普通参数不受检）
    [filterKey, pageIndex],
  )

  const goPrev = () => setPageIndex((i) => Math.max(0, i - 1))
  const goNext = () => {
    if (data === null || data.nextPageToken === '') return
    // 截掉当前页之后的旧游标再追加，来回翻页不会让游标栈无限增长
    setTokens((prev) => [...prev.slice(0, pageIndex + 1), data.nextPageToken])
    setPageIndex((i) => i + 1)
  }

  return (
    <>
      <section className="fs-panel p-4" aria-label="事件筛选">
        <p className="mb-3 text-xs leading-5 text-[#8b98a1]">
          来源：中台 PostgreSQL 不可变事件账。流量与传感事件主要由 Edge 或 Host 单元上报，也包含中台确定性派生事件；这里不是全量原始流量库。
        </p>
        <div className="flex flex-wrap items-center gap-3">
          <Select
            aria-label="按资产筛选"
            size="sm"
            radius="md"
            className="w-48"
            selectedKeys={[assetKey]}
            disallowEmptySelection
            onChange={(e) => {
              setAssetKey(e.target.value)
              resetPaging()
            }}
          >
            {[
              <SelectItem key="all">全部资产</SelectItem>,
              ...(assets.data?.items ?? []).map((a) => <SelectItem key={a.asset.id}>{a.asset.displayName}</SelectItem>),
            ]}
          </Select>
          <Select
            aria-label="按判定筛选"
            size="sm"
            radius="md"
            className="w-36"
            selectedKeys={[verdictKey]}
            disallowEmptySelection
            onChange={(e) => {
              setVerdictKey(e.target.value)
              resetPaging()
            }}
          >
            {[
              <SelectItem key="all">全部判定</SelectItem>,
              ...VERDICT_OPTIONS.map((o) => <SelectItem key={o.key}>{o.label}</SelectItem>),
            ]}
          </Select>
          <Select
            aria-label="按大类筛选"
            size="sm"
            radius="md"
            className="w-32"
            selectedKeys={[kindKey]}
            disallowEmptySelection
            onChange={(e) => {
              setKindKey(e.target.value)
              resetPaging()
            }}
          >
            {[
              <SelectItem key="all">全部大类</SelectItem>,
              ...KIND_OPTIONS.map((o) => <SelectItem key={o.key}>{o.label}</SelectItem>),
            ]}
          </Select>
          <Input
            aria-label="路径 / 规则关键词"
            placeholder="路径 / 规则关键词"
            size="sm"
            radius="md"
            className="w-64"
            isClearable
            value={query}
            onValueChange={(v) => {
              setQuery(v)
              resetPaging()
            }}
            startContent={<Search size={14} aria-hidden />}
          />
        </div>
      </section>

      {status === 'error' && error !== null ? (
        hasCode(error, 'permission_denied') ? (
          <StateView kind="denied" />
        ) : (
          <StateView kind="error" message={error.message} onRetry={reload} />
        )
      ) : status !== 'ok' || data === null ? (
        <StateView kind="loading" />
      ) : data.items.length === 0 ? (
        <StateView kind="empty" />
      ) : (
        <section className="fs-panel">
          <Table aria-label="事件列表" removeWrapper radius="none" className="fs-table-tight">
            <TableHeader>
              <TableColumn>时间</TableColumn>
              <TableColumn>判定</TableColumn>
              <TableColumn>大类</TableColumn>
              <TableColumn>路径</TableColumn>
              <TableColumn>规则</TableColumn>
              <TableColumn>资产</TableColumn>
              <TableColumn>来源</TableColumn>
            </TableHeader>
            <TableBody>
              {data.items.map((e) => (
                <TableRow
                  key={e.id}
                  tabIndex={0}
                  aria-label={`查看事件 ${e.id}`}
                  className="cursor-pointer transition-colors hover:bg-[#151c22]"
                  onClick={() => navigate(`/events/${e.id}`)}
                  onKeyDown={(ev) => {
                    if (ev.key === 'Enter') navigate(`/events/${e.id}`)
                  }}
                >
                  <TableCell className="fs-mono whitespace-nowrap">{formatTime(e.occurredAt)}</TableCell>
                  <TableCell>
                    <VerdictBadge verdict={e.verdict} />
                  </TableCell>
                  <TableCell>
                    <Badge label={KIND_LABEL[e.kind] ?? KIND_LABEL.KIND_UNSPECIFIED} tone="mute" />
                  </TableCell>
                  <TableCell className="fs-mono">{e.http?.path ?? '—'}</TableCell>
                  <TableCell className="fs-mono">{(e.detections ?? [])[0]?.ruleId ?? '—'}</TableCell>
                  <TableCell className="fs-mono">{e.assetId}</TableCell>
                  <TableCell>{e.source}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <div className="border-t border-[#1d252a]">
            <TokenPager page={pageIndex + 1} hasPrev={pageIndex > 0} hasNext={data.nextPageToken !== ''} onPrev={goPrev} onNext={goNext} />
          </div>
        </section>
      )}
    </>
  )
}
