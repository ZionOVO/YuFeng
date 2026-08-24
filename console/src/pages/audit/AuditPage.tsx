// 审计链：objectType/objectId/actor 筛选 + 不透明游标分页（pageToken 只回传不解析，docs/api.md §0.6）。
// 表格上方提供链段校验（VerifyChain）：按序号区间验证哈希链完整性。

import { useState } from 'react'
import { Button, Input, Select, SelectItem, Table, TableBody, TableCell, TableColumn, TableHeader, TableRow } from '@heroui/react'
import type { ListAuditFilter } from '../../api/client'
import { hasCode, isApiError } from '../../api/errors'
import type { ChainVerification } from '../../api/types'
import { useAuth } from '../../auth/useAuth'
import { useAsyncData } from '../../components/useAsyncData'
import { formatTime } from '../../components/format'
import { Badge, StateView, TokenPager } from '../../components/ui'

const PAGE_SIZE = 25
/** 哈希列只显示前 12 位，全文放 title。 */
const HASH_PREVIEW_LEN = 12

/** 对象类型筛选项（与 proto 的 object_type 取值一致）。 */
const OBJECT_TYPES = [
  'release', 'asset', 'user', 'unit', 'case', 'agent_profile', 'evidence_approval',
  'worker_enrollment', 'worker_capacity_change', 'worker',
] as const
type ObjectTypeFilter = (typeof OBJECT_TYPES)[number] | 'all'

export function AuditPage() {
  const { client } = useAuth()
  const [objectType, setObjectType] = useState<ObjectTypeFilter>('all')
  const [objectId, setObjectId] = useState('')
  const [actor, setActor] = useState('')
  // tokens[i] 是取第 i+1 页的 pageToken；筛选变化时重置整条游标链
  const [tokens, setTokens] = useState<string[]>([''])
  const [pageIndex, setPageIndex] = useState(0)

  const filter: ListAuditFilter = {
    objectType: objectType === 'all' ? undefined : objectType,
    objectId: objectId.trim() === '' ? undefined : objectId.trim(),
    actor: actor.trim() === '' ? undefined : actor.trim(),
  }
  const { data, status, error, reload } = useAsyncData(
    () => client.listAuditEntries(filter, { pageSize: PAGE_SIZE, pageToken: tokens[pageIndex] }),
    // filter 由筛选控件派生，每次渲染是新对象，用 JSON 快照作依赖
    [JSON.stringify(filter), pageIndex],
  )

  // 链段校验
  const [startSeq, setStartSeq] = useState('')
  const [endSeq, setEndSeq] = useState('')
  const [verifyBusy, setVerifyBusy] = useState(false)
  const [verifyResult, setVerifyResult] = useState<ChainVerification | null>(null)
  const [verifyError, setVerifyError] = useState<string | null>(null)

  // 筛选变化：回到第一页并丢弃旧游标
  const onFilterChange = () => {
    setTokens([''])
    setPageIndex(0)
  }
  const goNext = () => {
    if (data === null || data.nextPageToken === '') return
    setTokens((t) => [...t.slice(0, pageIndex + 1), data.nextPageToken])
    setPageIndex((i) => i + 1)
  }
  const goPrev = () => setPageIndex((i) => Math.max(0, i - 1))

  const runVerify = async () => {
    setVerifyBusy(true)
    setVerifyError(null)
    setVerifyResult(null)
    try {
      setVerifyResult(await client.verifyChain(startSeq.trim(), endSeq.trim()))
    } catch (e) {
      setVerifyError(isApiError(e) ? `校验请求失败：${e.message}（${e.code}）` : '校验请求失败，请重试')
    } finally {
      setVerifyBusy(false)
    }
  }

  if (status === 'error' && error !== null) {
    if (hasCode(error, 'permission_denied')) return <StateView kind="denied" />
    return <StateView kind="error" message={error.message} onRetry={reload} />
  }
  if (data === null) return <StateView kind="loading" />

  return (
    <>
      {/* 链段校验 */}
      <section className="fs-panel" aria-label="链段校验">
        <div className="fs-panel-head">
          <div>
            <p className="fs-panel-title">链段校验</p>
            <p className="mt-1 text-xs leading-5 text-[#8b98a1]">
              来源：Brain 在控制面事务中追加的审计哈希链，记录用户、Agent 与系统动作；不是 Edge 上报的流量数据。
            </p>
          </div>
          <p className="fs-panel-sub">VERIFY CHAIN</p>
        </div>
        <div className="flex flex-col gap-3 px-4 py-4 sm:flex-row sm:items-end">
          <Input
            label="起始序号"
            type="text"
            inputMode="numeric"
            size="sm"
            radius="md"
            value={startSeq}
            onValueChange={setStartSeq}
            className="sm:w-40"
          />
          <Input
            label="结束序号"
            type="text"
            inputMode="numeric"
            size="sm"
            radius="md"
            value={endSeq}
            onValueChange={setEndSeq}
            className="sm:w-40"
          />
          <Button
            size="sm"
            color="primary"
            radius="md"
            isLoading={verifyBusy}
            isDisabled={startSeq.trim() === '' || endSeq.trim() === ''}
            onPress={() => void runVerify()}
          >
            校验链段
          </Button>
        </div>
        {verifyError !== null && <p className="px-4 pb-4 text-xs text-[#ff746c]">{verifyError}</p>}
        {verifyResult !== null && (
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 pb-4 text-xs">
            {verifyResult.valid ? (
              <>
                <Badge label="校验通过" tone="green" />
                <span className="text-[#8b98a1]">已校验 {verifyResult.entriesChecked} 条</span>
                <span className="fs-mono text-[#8b98a1]" title={verifyResult.startHash}>
                  起点 {verifyResult.startHash.slice(0, HASH_PREVIEW_LEN)}
                </span>
                <span className="fs-mono text-[#8b98a1]" title={verifyResult.endHash}>
                  终点 {verifyResult.endHash.slice(0, HASH_PREVIEW_LEN)}
                </span>
              </>
            ) : (
              <Badge label="校验失败" tone="red" />
            )}
          </div>
        )}
      </section>

      {/* 审计条目 */}
      <section className="fs-panel" aria-label="审计链">
        <div className="fs-panel-head">
          <div>
            <p className="fs-panel-title">审计链</p>
            <p className="fs-panel-sub" style={{ marginTop: 4 }}>
              AUDIT LOG
            </p>
          </div>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Select
              aria-label="按对象类型筛选"
              size="sm"
              radius="md"
              selectedKeys={[objectType]}
              onChange={(e) => {
                setObjectType(e.target.value as ObjectTypeFilter)
                onFilterChange()
              }}
              className="sm:w-32"
            >
              {[
                <SelectItem key="all">全部</SelectItem>,
                ...OBJECT_TYPES.map((t) => <SelectItem key={t}>{t}</SelectItem>),
              ]}
            </Select>
            <Input
              aria-label="按对象标识筛选"
              placeholder="对象标识"
              size="sm"
              radius="md"
              value={objectId}
              onValueChange={(v) => {
                setObjectId(v)
                onFilterChange()
              }}
              className="sm:w-44"
              isClearable
            />
            <Input
              aria-label="按操作者筛选"
              placeholder="操作者"
              size="sm"
              radius="md"
              value={actor}
              onValueChange={(v) => {
                setActor(v)
                onFilterChange()
              }}
              className="sm:w-40"
              isClearable
            />
          </div>
        </div>
        <Table aria-label="审计条目" removeWrapper className="fs-table-tight">
          <TableHeader>
            <TableColumn>序号</TableColumn>
            <TableColumn>时间</TableColumn>
            <TableColumn>操作者</TableColumn>
            <TableColumn>动作</TableColumn>
            <TableColumn>对象</TableColumn>
            <TableColumn>详情</TableColumn>
            <TableColumn>哈希</TableColumn>
          </TableHeader>
          <TableBody emptyContent="没有符合条件的审计条目">
            {data.items.map((e) => (
              <TableRow key={e.sequence}>
                <TableCell className="fs-mono">{e.sequence}</TableCell>
                <TableCell className="fs-mono">{formatTime(e.occurredAt)}</TableCell>
                <TableCell>
                  {e.actorId} <span className="text-[#8b98a1]">（{e.actorType}）</span>
                </TableCell>
                <TableCell className="fs-mono">{e.action}</TableCell>
                <TableCell className="fs-mono">
                  {e.objectType}:{e.objectId}
                </TableCell>
                <TableCell>
                  <span className="fs-mono block max-w-56 truncate" title={e.details}>
                    {e.details}
                  </span>
                </TableCell>
                <TableCell className="fs-mono" title={e.entryHash}>
                  {e.entryHash.slice(0, HASH_PREVIEW_LEN)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <TokenPager page={pageIndex + 1} hasPrev={pageIndex > 0} hasNext={data.nextPageToken !== ''} onPrev={goPrev} onNext={goNext} />
      </section>
    </>
  )
}
