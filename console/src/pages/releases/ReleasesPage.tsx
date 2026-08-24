// 防护策略列表页：状态/关键词筛选 + 不透明游标分页（docs/api.md §7、§0.6）。
// 支持 ?assetId= 过滤参数（来自资产详情页“查看关联防护策略”），以可清除 Badge 呈现。

import { useMemo, useState } from 'react'
import {
  Button,
  Checkbox,
  Input,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
  Select,
  SelectItem,
  Table,
  TableBody,
  TableCell,
  TableColumn,
  TableHeader,
  TableRow,
} from '@heroui/react'
import { Search, X } from 'lucide-react'
import { Link, useSearchParams } from 'react-router-dom'
import type { ListReleasesFilter } from '../../api/client'
import { isApiError } from '../../api/errors'
import type { ProposalKind, Release, ReleaseState, RetireReason } from '../../api/types'
import { useAuth } from '../../auth/useAuth'
import { useAsyncData } from '../../components/useAsyncData'
import { formatTime } from '../../components/format'
import { ReleaseStateBadge, StateView, TokenPager } from '../../components/ui'

const PAGE_SIZE = 25

/** 可筛选的六个发布状态（UNSPECIFIED 不出现在筛选器里）。 */
const FILTERABLE_STATES: { value: ReleaseState; label: string }[] = [
  { value: 'RELEASE_STATE_DRAFT', label: '草稿' },
  { value: 'RELEASE_STATE_SIGNED', label: '已签名' },
  { value: 'RELEASE_STATE_SHADOW', label: '影子' },
  { value: 'RELEASE_STATE_CANARY', label: '小比例' },
  { value: 'RELEASE_STATE_ENFORCE', label: '全量' },
  { value: 'RELEASE_STATE_RETIRED', label: '已退休' },
]

const RETIRE_REASON_LABEL: Record<RetireReason, string> = {
  RETIRE_REASON_UNSPECIFIED: '—',
  RETIRE_REASON_ROLLBACK: '回滚',
  RETIRE_REASON_MANUAL: '人工',
  RETIRE_REASON_TTL: '到期',
  RETIRE_REASON_SUPERSEDED: '被取代',
}

/** 制品种类：L1 只有检测规则给中文名，其余保留枚举原名便于对照契约。 */
function kindLabel(r: Release): string {
  const kind = r.artifact?.kind
  if (kind === undefined) return '—'
  return kind === 'KIND_RULE' ? '检测规则' : kind
}

function ProposeDialog({ open, onClose, onDone }: { open: boolean; onClose: () => void; onDone: () => void }) {
  const { client, access } = useAuth()
  const assetsQuery = useAsyncData(() => client.listAssets({}, { pageSize: 200 }), [])
  const [kind, setKind] = useState<ProposalKind>('PROPOSAL_KIND_POLICY')
  const [clusterId, setClusterId] = useState('')
  const [ruleId, setRuleId] = useState('')
  const [shapeMethods, setShapeMethods] = useState('GET')
  const [shapePrefix, setShapePrefix] = useState('')
  const [shapeSelector, setShapeSelector] = useState('')
  const [shapeMinLen, setShapeMinLen] = useState('1')
  const [shapeMaxLen, setShapeMaxLen] = useState('64')
  const [shapeCharset, setShapeCharset] = useState<'ascii_print' | 'digit' | 'alpha' | 'hex' | 'uuid'>('ascii_print')
  const [assetIds, setAssetIds] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const visible = (assetsQuery.data?.items ?? []).filter((a) =>
    access.bindings.some((b) => b.kind === 'asset' && b.id === a.asset.id),
  )

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.proposeArtifact({
        intent: {
          kind,
          clusterId,
          detectionKeys: kind === 'PROPOSAL_KIND_POLICY' && ruleId.trim() !== '' ? [{ ruleId: ruleId.trim() }] : undefined,
          shapeSource:
            kind === 'PROPOSAL_KIND_SHAPE'
              ? {
                  methods: shapeMethods
                    .split(',')
                    .map((m) => m.trim())
                    .filter((m) => m !== ''),
                  pathPrefix: shapePrefix.trim(),
                  constraints: [
                    {
                      selector: shapeSelector.trim(),
                      minLen: Number(shapeMinLen),
                      maxLen: Number(shapeMaxLen),
                      charset: shapeCharset,
                    },
                  ],
                }
              : undefined,
        },
        scope: { assetIds, routeSelector: '' },
        ttl: '86400s',
      })
      onDone()
      onClose()
    } catch (e) {
      setError(isApiError(e) ? `${e.message}（${e.code}）` : '提案失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal isOpen={open} onClose={onClose} placement="center" radius="md">
      <ModalContent>
        <ModalHeader>提交提案意图</ModalHeader>
        <ModalBody className="gap-3">
          <label className="flex flex-col gap-1 text-sm">
            意图种类
            <select aria-label="意图种类" className="rounded-md bg-[#0c1013] px-2 py-2" value={kind} onChange={(e) => setKind(e.target.value as ProposalKind)}>
              <option value="PROPOSAL_KIND_POLICY">PROPOSAL_KIND_POLICY</option>
              <option value="PROPOSAL_KIND_SHAPE">PROPOSAL_KIND_SHAPE</option>
            </select>
          </label>
          <Input label="聚类标识" radius="md" value={clusterId} onValueChange={setClusterId} />
          {kind === 'PROPOSAL_KIND_POLICY' && (
            <Input label="检测键规则号" radius="md" value={ruleId} onValueChange={setRuleId} />
          )}
          {kind === 'PROPOSAL_KIND_SHAPE' && (
            <>
              <Input label="允许的方法（逗号分隔）" radius="md" value={shapeMethods} onValueChange={setShapeMethods} />
              <Input label="路径前缀（至少两段，例如 /api/items）" radius="md" value={shapePrefix} onValueChange={setShapePrefix} />
              <Input label="约束选择器（例如 query.id）" radius="md" value={shapeSelector} onValueChange={setShapeSelector} />
              <div className="grid grid-cols-2 gap-2">
                <Input label="最小长度" type="number" min={0} value={shapeMinLen} onValueChange={setShapeMinLen} />
                <Input label="最大长度" type="number" min={1} value={shapeMaxLen} onValueChange={setShapeMaxLen} />
              </div>
              <label className="flex flex-col gap-1 text-sm">
                字符集
                <select aria-label="字符集" className="rounded-md bg-[#0c1013] px-2 py-2" value={shapeCharset} onChange={(e) => setShapeCharset(e.target.value as typeof shapeCharset)}>
                  <option value="ascii_print">可打印字符</option>
                  <option value="digit">数字</option>
                  <option value="alpha">字母</option>
                  <option value="hex">十六进制</option>
                  <option value="uuid">UUID</option>
                </select>
              </label>
            </>
          )}
          <fieldset>
            <legend className="mb-2 text-xs text-[#8b98a1]">资产</legend>
            {visible.map((a) => (
              <Checkbox
                key={a.asset.id}
                isSelected={assetIds.includes(a.asset.id)}
                onValueChange={() =>
                  setAssetIds((prev) => (prev.includes(a.asset.id) ? [] : [a.asset.id]))
                }
                aria-label={`资产 ${a.asset.id}`}
              >
                {a.asset.id}
              </Checkbox>
            ))}
          </fieldset>
          {error !== null && <p className="text-xs text-[#ff746c]">{error}</p>}
        </ModalBody>
        <ModalFooter>
          <Button variant="light" radius="md" onPress={onClose}>
            取消
          </Button>
          <Button
            color="primary"
            radius="md"
            isLoading={busy}
            isDisabled={
              clusterId.trim() === '' ||
              assetIds.length !== 1 ||
              (kind === 'PROPOSAL_KIND_POLICY' && ruleId.trim() === '') ||
              (kind === 'PROPOSAL_KIND_SHAPE' &&
                (shapeMethods.split(',').every((m) => m.trim() === '') ||
                  shapePrefix.trim().split('/').filter(Boolean).length < 2 ||
                  shapeSelector.trim() === '' ||
                  !Number.isInteger(Number(shapeMinLen)) ||
                  !Number.isInteger(Number(shapeMaxLen)) ||
                  Number(shapeMinLen) < 0 ||
                  Number(shapeMaxLen) <= 0 ||
                  Number(shapeMinLen) > Number(shapeMaxLen)))
            }
            onPress={() => void submit()}
          >
            提出
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  )
}

export function ReleasesPage() {
  const { client, hasTool } = useAuth()
  const [proposeOpen, setProposeOpen] = useState(false)
  const [searchParams, setSearchParams] = useSearchParams()
  const assetId = searchParams.get('assetId') ?? ''

  const [stateFilter, setStateFilter] = useState('all')
  const [query, setQuery] = useState('')
  // 游标链：tokens[i] 是第 i 页的入参 pageToken，首页为空串；只回传不解析
  const [tokens, setTokens] = useState<string[]>([''])
  const [pageIndex, setPageIndex] = useState(0)

  const filter = useMemo<ListReleasesFilter>(
    () => ({
      states: stateFilter === 'all' ? undefined : [stateFilter as ReleaseState],
      assetId: assetId === '' ? undefined : assetId,
      query: query.trim() === '' ? undefined : query.trim(),
    }),
    [stateFilter, assetId, query],
  )
  const filterKey = JSON.stringify(filter)

  // 筛选变化（含 URL 上 assetId 变化）回到第一页：渲染期间与上一次筛选比较后同步重置，
  // 避免 effect 级联渲染（react.dev 的 adjusting-state-when-props-change 模式）
  const [appliedKey, setAppliedKey] = useState(filterKey)
  if (appliedKey !== filterKey) {
    setAppliedKey(filterKey)
    setTokens([''])
    setPageIndex(0)
  }

  const { data, status, error, reload } = useAsyncData(
    () => client.listReleases(filter, { pageSize: PAGE_SIZE, pageToken: tokens[pageIndex] }),
    // filter 由 useMemo 稳定，依赖用其序列化结果 filterKey
    [filterKey, pageIndex],
  )

  const clearAssetFilter = () => {
    const next = new URLSearchParams(searchParams)
    next.delete('assetId')
    setSearchParams(next, { replace: true })
  }

  const goPrev = () => setPageIndex((i) => Math.max(0, i - 1))
  const goNext = () => {
    if (data === null || data.nextPageToken === '') return
    setTokens((prev) => {
      const next = prev.slice(0, pageIndex + 1)
      next[pageIndex + 1] = data.nextPageToken
      return next
    })
    setPageIndex((i) => i + 1)
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <Input
          aria-label="搜索防护策略"
          placeholder="策略标识 / 制品标识 / 创建者"
          size="sm"
          radius="md"
          isClearable
          value={query}
          onValueChange={setQuery}
          startContent={<Search size={14} aria-hidden />}
          className="sm:w-72"
        />
        <Select
          aria-label="按状态筛选"
          size="sm"
          radius="md"
          selectedKeys={[stateFilter]}
          onChange={(e) => setStateFilter(e.target.value)}
          className="sm:w-40"
        >
          {[
            <SelectItem key="all">全部状态</SelectItem>,
            ...FILTERABLE_STATES.map((s) => <SelectItem key={s.value}>{s.label}</SelectItem>),
          ]}
        </Select>
        {hasTool('govern.propose') && (
          <Button color="primary" size="sm" radius="md" className="sm:ml-auto" onPress={() => setProposeOpen(true)}>
            提交提案
          </Button>
        )}
        {assetId !== '' && (
          <span className="fs-badge fs-badge--mute">
            资产过滤 <span className="fs-mono">{assetId}</span>
            <button type="button" aria-label="清除资产过滤" onClick={clearAssetFilter} className="ml-1 inline-flex items-center">
              <X size={12} aria-hidden />
            </button>
          </span>
        )}
      </div>

      {status === 'loading' && <StateView kind="loading" />}
      {status === 'error' && error !== null && (
        <StateView
          kind={error.code === 'permission_denied' ? 'denied' : 'error'}
          message={error.message}
          onRetry={reload}
        />
      )}
      {status === 'ok' && data !== null && data.items.length === 0 && (
        <StateView kind="empty" title="没有符合条件的防护策略" message="调整筛选条件后重试" />
      )}
      {status === 'ok' && data !== null && data.items.length > 0 && (
        <section className="fs-panel" aria-label="防护策略列表">
          <Table aria-label="防护策略列表" removeWrapper radius="none" className="fs-table-tight">
            <TableHeader>
              <TableColumn>策略标识</TableColumn>
              <TableColumn>种类</TableColumn>
              <TableColumn>状态</TableColumn>
              <TableColumn>创建者</TableColumn>
              <TableColumn>提案时间</TableColumn>
              <TableColumn>退休原因</TableColumn>
            </TableHeader>
            <TableBody emptyContent="没有符合条件的防护策略">
              {data.items.map((r) => (
                <TableRow key={r.releaseId}>
                  <TableCell>
                    <Link to={`/releases/${r.releaseId}`} className="fs-mono text-[#62e6a7] hover:underline">
                      {r.releaseId}
                    </Link>
                  </TableCell>
                  <TableCell>{kindLabel(r)}</TableCell>
                  <TableCell>
                    <ReleaseStateBadge state={r.state} />
                  </TableCell>
                  <TableCell>{r.createdBy}</TableCell>
                  <TableCell className="fs-mono text-[#8b98a1]">{formatTime(r.proposedAt)}</TableCell>
                  <TableCell>{RETIRE_REASON_LABEL[r.retireReason] ?? RETIRE_REASON_LABEL.RETIRE_REASON_UNSPECIFIED}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <TokenPager
            page={pageIndex + 1}
            hasPrev={pageIndex > 0}
            hasNext={data.nextPageToken !== ''}
            onPrev={goPrev}
            onNext={goNext}
          />
        </section>
      )}
      <ProposeDialog open={proposeOpen} onClose={() => setProposeOpen(false)} onDone={reload} />
    </div>
  )
}
