// 防护策略详情页：状态管道、概要、回放报告、统计窗口、状态机操作与时间线（docs/api.md §7）。
// 写操作严格跟随状态机：非法状态的操作不渲染；门禁不通过不是错误（replayReport.passed=false），
// 推进门槛不满足时以 failed_precondition + GateResult 渲染逐项检查（GateChecksView）。

import { Fragment, useState } from 'react'
import {
  Button,
  Input,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
  Table,
  TableBody,
  TableCell,
  TableColumn,
  TableHeader,
  TableRow,
  Textarea,
} from '@heroui/react'
import { useParams } from 'react-router-dom'
import { canOnRelease } from '../../api/access'
import { ApiError, isApiError } from '../../api/errors'
import type { GateOutcome, Release, ReleaseState, ReleaseWindowStats, ReplayReport } from '../../api/types'
import { useAuth } from '../../auth/useAuth'
import { formatDuration, formatTime } from '../../components/format'
import { useAsyncData } from '../../components/useAsyncData'
import {
  Badge,
  ConfirmDialog,
  GateChecksView,
  ReleaseStateBadge,
  StateView,
} from '../../components/ui'

/* ---------- 状态管道 ---------- */

/** 管道主链顺序；retired 不在主链上（任何在役态都可直接退休），仅按时间戳判断是否经过。 */
const PIPELINE: { state: ReleaseState; label: string; at: (r: Release) => string | undefined }[] = [
  { state: 'RELEASE_STATE_DRAFT', label: '草稿', at: (r) => r.proposedAt },
  { state: 'RELEASE_STATE_SIGNED', label: '已签名', at: (r) => r.signedAt },
  { state: 'RELEASE_STATE_SHADOW', label: '影子', at: (r) => r.shadowStartedAt },
  { state: 'RELEASE_STATE_CANARY', label: '小比例', at: (r) => r.canaryStartedAt },
  { state: 'RELEASE_STATE_ENFORCE', label: '全量', at: (r) => r.enforcedAt },
  { state: 'RELEASE_STATE_RETIRED', label: '已退休', at: (r) => r.retiredAt },
]

function pipelineStepClass(r: Release, index: number): string {
  const step = PIPELINE[index]
  if (r.state === 'RELEASE_STATE_RETIRED') {
    if (step.state === 'RELEASE_STATE_RETIRED') return 'is-current'
    return step.at(r) !== undefined ? 'is-done' : ''
  }
  const currentIndex = PIPELINE.findIndex((p) => p.state === r.state)
  if (index === currentIndex) return 'is-current'
  if (index < currentIndex) return 'is-done'
  return ''
}

/* ---------- 概要辅助 ---------- */

/** 制品标识形如 sha256:<64 hex>，概要里只保留头部，完整值放 title。 */
function shortenId(id: string): string {
  return id.length > 28 ? `${id.slice(0, 28)}…` : id
}

/** 概要面板：yf-kv 键值栅格，只渲染存在的时间戳。 */
function SummaryPanel({ release: r }: { release: Release }) {
  const times: [string, string | undefined][] = [
    ['提案时间', r.proposedAt],
    ['签名时间', r.signedAt],
    ['影子开始', r.shadowStartedAt],
    ['小比例开始', r.canaryStartedAt],
    ['全量时间', r.enforcedAt],
    ['退休时间', r.retiredAt],
  ]
  const artifactId = r.artifact?.id ?? ''
  return (
    <section className="fs-panel" aria-label="防护策略概要">
      <div className="fs-panel-head">
        <p className="fs-panel-title">概要</p>
        <p className="fs-panel-sub">SUMMARY</p>
      </div>
      <dl className="yf-kv px-4 py-3">
        <dt>策略标识</dt>
        <dd className="fs-mono">{r.releaseId}</dd>
        <dt>状态</dt>
        <dd>
          <ReleaseStateBadge state={r.state} />
        </dd>
        <dt>创建者</dt>
        <dd>{r.createdBy}</dd>
        <dt>制品标识</dt>
        {artifactId !== '' ? (
          <dd className="fs-mono" title={artifactId}>
            {shortenId(artifactId)}
          </dd>
        ) : (
          <dd>未签名</dd>
        )}
        {r.artifact !== undefined && (
          <>
            <dt>存活时限</dt>
            <dd>{formatDuration(r.artifact.ttl)}</dd>
            <dt>取代</dt>
            <dd className="fs-mono">{r.artifact.supersedes !== '' ? r.artifact.supersedes : '—'}</dd>
          </>
        )}
        {times
          .filter(([, v]) => v !== undefined)
          .map(([label, v]) => (
            <Fragment key={label}>
              <dt>{label}</dt>
              <dd className="fs-mono">{formatTime(v)}</dd>
            </Fragment>
          ))}
      </dl>
    </section>
  )
}

/* ---------- 回放报告 ---------- */

function ReplayCounts({ report }: { report: ReplayReport }) {
  return (
    <dl className="yf-kv">
      <dt>恶意拦截</dt>
      <dd className="fs-mono">
        {report.maliciousBlocked}/{report.maliciousTotal}
      </dd>
      <dt>良性误伤</dt>
      <dd className="fs-mono">
        {report.benignBlocked}/{report.benignTotal}
      </dd>
      <dt>管理面命中</dt>
      <dd className="fs-mono">
        {report.managementBlocked}/{report.managementTotal}
      </dd>
      <dt>语料</dt>
      <dd className="fs-mono">{report.corpusRef}</dd>
    </dl>
  )
}

function ReplayPanel({ release: r }: { release: Release }) {
  const report = r.artifact?.replayReport
  return (
    <section className="fs-panel" aria-label="回放报告">
      <div className="fs-panel-head">
        <p className="fs-panel-title">回放报告</p>
        <p className="fs-panel-sub">REPLAY GATE</p>
      </div>
      {report === undefined ? (
        <p className="px-4 py-8 text-center text-xs text-[#8b98a1]">尚未执行回放门禁</p>
      ) : (
        <div className="flex flex-col gap-3 px-4 py-3">
          <div>{report.passed ? <Badge label="通过" tone="green" /> : <Badge label="未通过" tone="red" />}</div>
          <ReplayCounts report={report} />
        </div>
      )}
    </section>
  )
}

/* ---------- 统计窗口 ---------- */

/** 单个阶段窗口小卡：计数均为 protojson 的 uint64 字符串，直接展示；p99 微秒换算毫秒。 */
function WindowCard({ title, window }: { title: string; window: ReleaseWindowStats }) {
  const rows: [string, string][] = [
    ['时长', formatDuration(window.duration)],
    ['请求', window.requests],
    ['拦截', window.blocks],
    ['观察', window.observes],
    ['灰度命中', window.canarySelected],
    ['误报举报', window.denyFeedbackTotal],
    ['上游 5xx', window.upstream5xx],
    ['p99', `${(Number(window.p99Micros) / 1000).toFixed(1)} ms`],
  ]
  return (
    <div className="rounded-lg border border-[#263038] bg-[#0c1013] p-3">
      <p className="mb-2 text-xs font-medium">{title}</p>
      <dl className="grid grid-cols-2 gap-x-3 gap-y-1 text-xs">
        {rows.map(([k, v]) => (
          <div key={k} className="flex items-baseline justify-between gap-2">
            <dt className="text-[#8b98a1]">{k}</dt>
            <dd className="fs-mono">{v}</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}

/* ---------- 操作区 ---------- */

type PendingAction = 'gate' | 'shadow' | 'canary' | 'enforce' | 'rollback' | 'retire'

/** 灰度百分比合法域：1–25 的整数（docs/api.md §7.5）。 */
function parseCanaryPercent(raw: string): number | null {
  const n = Number(raw)
  if (!Number.isInteger(n) || n < 1 || n > 25) return null
  return n
}

export function ReleaseDetailPage() {
  const { releaseId } = useParams<{ releaseId: string }>()
  const { client, access, user } = useAuth()
  const id = releaseId ?? ''

  const releaseQuery = useAsyncData(() => client.getRelease(id), [id], false)
  const statsQuery = useAsyncData(() => client.getReleaseStats(id), [id], false)
  const timelineQuery = useAsyncData(() => client.getReleaseTimeline(id, { pageSize: 50 }), [id], false)

  const [pending, setPending] = useState<PendingAction | null>(null)
  const [busy, setBusy] = useState(false)
  const [opError, setOpError] = useState<ApiError | null>(null)
  const [gateOutcome, setGateOutcome] = useState<GateOutcome | null>(null)
  const [canaryPercent, setCanaryPercent] = useState('5')
  const [reason, setReason] = useState('')

  const reloadAll = () => {
    releaseQuery.reload()
    statsQuery.reload()
    timelineQuery.reload()
  }

  const openAction = (action: PendingAction) => {
    setOpError(null)
    setGateOutcome(null)
    setReason('')
    if (action === 'canary') setCanaryPercent('5')
    setPending(action)
  }

  /** 统一执行写操作：成功关弹窗返回结果，失败关弹窗并把错误挂到操作区。 */
  async function runAction<T>(action: () => Promise<T>): Promise<T | null> {
    setBusy(true)
    setOpError(null)
    try {
      const result = await action()
      setPending(null)
      return result
    } catch (e) {
      setPending(null)
      setOpError(isApiError(e) ? e : new ApiError({ code: 'unknown', message: String(e), httpStatus: 0 }))
      return null
    } finally {
      setBusy(false)
    }
  }

  const doGate = async () => {
    const outcome = await runAction(() => client.gateArtifact(id))
    if (outcome !== null) {
      // 门禁不通过不是错误：结果区按 replayReport.passed 展示，状态是否变化统一刷新
      setGateOutcome(outcome)
      reloadAll()
    }
  }
  const doShadow = async () => {
    if ((await runAction(() => client.startShadow(id))) !== null) reloadAll()
  }
  const doCanary = async () => {
    const percent = parseCanaryPercent(canaryPercent)
    if (percent === null) return
    if ((await runAction(() => client.promoteCanary(id, percent))) !== null) reloadAll()
  }
  const doEnforce = async () => {
    if ((await runAction(() => client.promoteEnforce(id))) !== null) reloadAll()
  }
  const doRollback = async () => {
    if ((await runAction(() => client.rollbackRelease(id, reason.trim()))) !== null) reloadAll()
  }
  const doRetire = async () => {
    if ((await runAction(() => client.retireRelease(id, reason.trim()))) !== null) reloadAll()
  }

  if (releaseQuery.status === 'loading') return <StateView kind="loading" />
  if (releaseQuery.status === 'error') {
    const err = releaseQuery.error
    if (err?.code === 'not_found') {
      return <StateView kind="error" title="发布不存在" message={`未找到发布 ${id}`} onRetry={releaseQuery.reload} />
    }
    if (err?.code === 'permission_denied') return <StateView kind="denied" />
    return <StateView kind="error" message={err?.message} onRetry={releaseQuery.reload} />
  }
  const r = releaseQuery.data
  if (r === null) return null

  const percent = parseCanaryPercent(canaryPercent)
  const stats = statsQuery.data
  const hasWindow = stats !== null && (stats.shadow !== undefined || stats.canary !== undefined || stats.enforce !== undefined)
  const timelineItems = timelineQuery.data?.items ?? []
  const gateReport = gateOutcome?.replayReport

  return (
    <div className="flex flex-col gap-4">
      {/* 状态管道 */}
      <section className="fs-panel" aria-label="状态管道">
        <div className="fs-panel-head">
          <p className="fs-panel-title">状态管道</p>
          <p className="fs-panel-sub">PIPELINE</p>
        </div>
        <div className="px-4 py-3">
          <div className="yf-pipeline">
            {PIPELINE.map((step, i) => (
              <Fragment key={step.state}>
                {i > 0 && (
                  <span className="yf-pipeline-arrow" aria-hidden>
                    →
                  </span>
                )}
                <span className={`yf-pipeline-step ${pipelineStepClass(r, i)}`.trim()}>{step.label}</span>
              </Fragment>
            ))}
          </div>
        </div>
      </section>

      {/* 概要 + 回放报告 */}
      <section className="fs-grid2">
        <SummaryPanel release={r} />
        <ReplayPanel release={r} />
      </section>

      {/* 统计窗口 */}
      {statsQuery.status === 'loading' && <StateView kind="loading" />}
      {statsQuery.status === 'error' && (
        <StateView kind="error" message={statsQuery.error?.message} onRetry={statsQuery.reload} />
      )}
      {statsQuery.status === 'ok' && stats !== null && !hasWindow && (
        <StateView kind="empty" title="尚无统计窗口" message="进入影子阶段后开始产生统计" />
      )}
      {statsQuery.status === 'ok' && stats !== null && hasWindow && (
        <section className="fs-panel" aria-label="防护策略统计">
          <div className="fs-panel-head">
            <p className="fs-panel-title">统计窗口</p>
            <p className="fs-panel-sub">STATS</p>
          </div>
          <div className="grid grid-cols-1 gap-3 px-4 py-3 md:grid-cols-3">
            {stats.shadow !== undefined && <WindowCard title="影子窗口" window={stats.shadow} />}
            {stats.canary !== undefined && <WindowCard title="小比例窗口" window={stats.canary} />}
            {stats.enforce !== undefined && <WindowCard title="全量窗口" window={stats.enforce} />}
          </div>
          {stats.guard !== undefined && (
            <div className="mx-4 mb-3 rounded-lg border border-[#263038] bg-[#0c1013] p-3 text-xs">
              <p className="mb-2 font-medium">守护窗口</p>
              <p>
                连续坏窗口：
                <span className={`fs-mono ${stats.guard.consecutiveBadWindows > 0 ? 'text-[#ff746c]' : ''}`}>
                  {stats.guard.consecutiveBadWindows}
                </span>
              </p>
              {stats.guard.lastBadWindowAt !== undefined && (
                <p className="mt-1 text-[#8b98a1]">最近坏窗口：{formatTime(stats.guard.lastBadWindowAt)}</p>
              )}
              {stats.guard.lastBadReasons.length > 0 && (
                <ul className="mt-1 list-inside list-disc text-[#8b98a1]">
                  {stats.guard.lastBadReasons.map((bad) => (
                    <li key={bad}>{bad}</li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </section>
      )}

      {/* 操作区：按 access.tools 显隐，提案人不能点推进（docs/api.md §17.4.1） */}
      {(() => {
        const proposer = user?.username === r.createdBy || user?.userId === r.createdBy
        const assets = r.artifact?.scope?.assetIds
        const showGate = r.state === 'RELEASE_STATE_DRAFT' && canOnRelease(access, 'govern.gate', assets)
        const showShadow = r.state === 'RELEASE_STATE_SIGNED' && canOnRelease(access, 'govern.start_shadow', assets)
        const showCanary = r.state === 'RELEASE_STATE_SHADOW' && canOnRelease(access, 'govern.promote_canary', assets)
        const showEnforce =
          (r.state === 'RELEASE_STATE_CANARY' || r.state === 'RELEASE_STATE_SHADOW') &&
          canOnRelease(access, 'govern.promote_enforce', assets)
        const showRollback =
          (r.state === 'RELEASE_STATE_SHADOW' || r.state === 'RELEASE_STATE_CANARY' || r.state === 'RELEASE_STATE_ENFORCE') &&
          canOnRelease(access, 'govern.rollback', assets)
        const showRetire =
          (r.state === 'RELEASE_STATE_SHADOW' || r.state === 'RELEASE_STATE_CANARY' || r.state === 'RELEASE_STATE_ENFORCE') &&
          canOnRelease(access, 'govern.retire', assets)
        const any =
          r.state === 'RELEASE_STATE_RETIRED' ||
          showGate ||
          showShadow ||
          showCanary ||
          showEnforce ||
          showRollback ||
          showRetire ||
          ((showCanary || showEnforce) && proposer)
        if (!any) return null
        return (
        <section className="fs-panel" aria-label="防护策略操作">
          <div className="fs-panel-head">
            <p className="fs-panel-title">操作</p>
            <p className="fs-panel-sub">ACTIONS</p>
          </div>
          <div className="px-4 py-3">
            {r.state === 'RELEASE_STATE_RETIRED' ? (
              <p className="text-xs text-[#8b98a1]">防护策略已退休，无可用操作</p>
            ) : (
              <div className="flex flex-wrap gap-2">
                {showGate && (
                  <Button color="primary" size="sm" radius="md" onPress={() => openAction('gate')}>
                    执行门禁
                  </Button>
                )}
                {showShadow && (
                  <Button color="primary" size="sm" radius="md" onPress={() => openAction('shadow')}>
                    启动影子
                  </Button>
                )}
                {showCanary && (
                  <Button color="primary" size="sm" radius="md" isDisabled={proposer} onPress={() => openAction('canary')}>
                    推进小比例
                  </Button>
                )}
                {showEnforce && (
                  <Button color="primary" size="sm" radius="md" isDisabled={proposer} onPress={() => openAction('enforce')}>
                    推进全量
                  </Button>
                )}
                {showRollback && (
                    <Button color="danger" variant="bordered" size="sm" radius="md" onPress={() => openAction('rollback')}>
                      回滚
                    </Button>
                )}
                {showRetire && (
                    <Button color="danger" variant="bordered" size="sm" radius="md" onPress={() => openAction('retire')}>
                      退休
                    </Button>
                )}
                {proposer && (showCanary || showEnforce) && (
                  <p className="basis-full text-xs text-[#8b98a1]">须由其他持权用户推进（提案人不能推进自己的发布）</p>
                )}
              </div>
            )}

            {/* 门禁结果区：通过/不通过都不是异常 */}
            {gateOutcome !== null && (
              <div
                className={`mt-3 rounded-lg border px-3 py-2 ${
                  gateReport?.passed === true ? 'border-[#28583f] bg-[#10251b]' : 'border-[#65512a] bg-[#251f12]'
                }`}
              >
                {gateReport?.passed === true ? (
                  <p className="text-xs text-[#62e6a7]">门禁通过，制品已签名</p>
                ) : (
                  <div className="flex flex-col gap-2">
                    <p className="text-xs text-[#f1be5b]">门禁未通过，发布留在草稿</p>
                    {gateReport !== undefined && <ReplayCounts report={gateReport} />}
                  </div>
                )}
              </div>
            )}

            {/* 写操作错误：门槛逐项 / 状态冲突 / 其他 */}
            {opError !== null &&
              (opError.gateChecks !== undefined && opError.gateChecks.length > 0 ? (
                <div className="mt-3 rounded-lg border border-[#65512a] bg-[#251f12] px-3 py-2">
                  <p className="text-xs text-[#f1be5b]">推进门槛未满足（{opError.code}）</p>
                  <GateChecksView checks={opError.gateChecks} />
                </div>
              ) : opError.reasonKey === 'release_state_conflict' ? (
                <p className="mt-3 flex items-center gap-2 text-xs text-[#ff746c]">
                  状态已变化，请刷新
                  <Button size="sm" radius="md" variant="bordered" onPress={reloadAll}>
                    刷新
                  </Button>
                </p>
              ) : (
                <p className="mt-3 text-xs text-[#ff746c]">
                  {opError.message}（{opError.code}）
                </p>
              ))}
          </div>
        </section>
        )
      })()}

      {/* 状态流转时间线 */}
      {timelineQuery.status === 'loading' && <StateView kind="loading" />}
      {timelineQuery.status === 'error' && (
        <StateView kind="error" message={timelineQuery.error?.message} onRetry={timelineQuery.reload} />
      )}
      {timelineQuery.status === 'ok' && timelineItems.length === 0 && <StateView kind="empty" title="暂无状态流转记录" />}
      {timelineQuery.status === 'ok' && timelineItems.length > 0 && (
        <section className="fs-panel" aria-label="状态流转时间线">
          <div className="fs-panel-head">
            <p className="fs-panel-title">时间线</p>
            <p className="fs-panel-sub">TIMELINE</p>
          </div>
          <Table aria-label="状态流转时间线" removeWrapper radius="none" className="fs-table-tight">
            <TableHeader>
              <TableColumn>序号</TableColumn>
              <TableColumn>状态流转</TableColumn>
              <TableColumn>操作者</TableColumn>
              <TableColumn>原因</TableColumn>
              <TableColumn>时间</TableColumn>
            </TableHeader>
            <TableBody emptyContent="暂无状态流转记录">
              {timelineItems.map((e) => (
                <TableRow key={e.sequence}>
                  <TableCell className="fs-mono">{e.sequence}</TableCell>
                  <TableCell>
                    <span className="flex items-center gap-1.5">
                      <ReleaseStateBadge state={e.fromState} />
                      <span className="text-[#8b98a1]" aria-hidden>
                        →
                      </span>
                      <ReleaseStateBadge state={e.toState} />
                    </span>
                  </TableCell>
                  <TableCell>{e.actor}</TableCell>
                  <TableCell>{e.reason === '' ? '—' : e.reason}</TableCell>
                  <TableCell className="fs-mono text-[#8b98a1]">{formatTime(e.occurredAt)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </section>
      )}

      {/* 操作弹窗 */}
      <ConfirmDialog
        open={pending === 'gate'}
        title="执行回放门禁"
        confirmLabel="执行"
        busy={busy}
        onConfirm={() => void doGate()}
        onClose={() => setPending(null)}
      >
        <p className="text-sm text-foreground-500">将用内置语料回放验证制品：通过则签名进入已签名状态，不通过则留在草稿。</p>
      </ConfirmDialog>

      <ConfirmDialog
        open={pending === 'shadow'}
        title="启动影子"
        confirmLabel="启动"
        busy={busy}
        onConfirm={() => void doShadow()}
        onClose={() => setPending(null)}
      >
        <p className="text-sm text-foreground-500">制品进入影子阶段：参与决策评估，只观察、不拦截。</p>
      </ConfirmDialog>

      <Modal isOpen={pending === 'canary'} onClose={() => setPending(null)} placement="center" radius="md">
        <ModalContent>
          <ModalHeader>推进小比例</ModalHeader>
          <ModalBody>
            <p className="text-sm text-foreground-500">影子阶段指标达标后，按灰度百分比切入真实流量。</p>
            <Input
              label="灰度百分比"
              type="number"
              min={1}
              max={25}
              radius="md"
              value={canaryPercent}
              onValueChange={setCanaryPercent}
              description="取值范围 1–25，缺省 5"
              isRequired
            />
          </ModalBody>
          <ModalFooter>
            <Button variant="light" radius="md" onPress={() => setPending(null)} isDisabled={busy}>
              取消
            </Button>
            <Button color="primary" radius="md" isLoading={busy} isDisabled={percent === null} onPress={() => void doCanary()}>
              推进
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>

      <ConfirmDialog
        open={pending === 'enforce'}
        title="推进全量"
        confirmLabel="推进"
        busy={busy}
        onConfirm={() => void doEnforce()}
        onClose={() => setPending(null)}
      >
        <p className="text-sm text-foreground-500">门槛检查通过后，全部边缘将按该制品执行拦截。</p>
      </ConfirmDialog>

      <ConfirmDialog
        open={pending === 'rollback'}
        title="回滚防护策略"
        confirmLabel="确认回滚"
        danger
        busy={busy}
        confirmDisabled={reason.trim() === ''}
        onConfirm={() => void doRollback()}
        onClose={() => setPending(null)}
      >
        <p className="text-sm text-foreground-500">置为已退休（原因：回滚），边缘下次拉取收到墓碑并卸载。</p>
        <Textarea
          label="回滚原因"
          placeholder="必填，写入审计链"
          radius="md"
          minRows={2}
          value={reason}
          onValueChange={setReason}
          isRequired
        />
      </ConfirmDialog>

      <ConfirmDialog
        open={pending === 'retire'}
        title="退休防护策略"
        confirmLabel="确认退休"
        danger
        busy={busy}
        confirmDisabled={reason.trim() === ''}
        onConfirm={() => void doRetire()}
        onClose={() => setPending(null)}
      >
        <p className="text-sm text-foreground-500">人工退休（MANUAL）：置为已退休，边缘下次拉取收到墓碑并卸载。</p>
        <Textarea
          label="退休原因"
          placeholder="必填，写入审计链"
          radius="md"
          minRows={2}
          value={reason}
          onValueChange={setReason}
          isRequired
        />
      </ConfirmDialog>
    </div>
  )
}
