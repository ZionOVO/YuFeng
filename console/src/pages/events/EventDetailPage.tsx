// 事件详情页：概要头部、元数据、超文本传输协议与人工智能载荷、检测结论、发布轨迹与误报举报。
// 误报举报走 GovernService.DenyFeedback：仅 canary/enforce 发布的 BLOCK 事件可举报（docs/api.md §7.8）。

import { useState, type ReactNode } from 'react'
import {
  Button,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
  Select,
  SelectItem,
  Textarea,
  useDisclosure,
} from '@heroui/react'
import { Flag } from 'lucide-react'
import { Link, useParams } from 'react-router-dom'
import { hasCode, isApiError } from '../../api/errors'
import type { AiPayload, Event, EventKind, HttpPayload, ReleaseMode, Tier } from '../../api/types'
import { useAuth } from '../../auth/useAuth'
import { formatTime } from '../../components/format'
import { Badge, StateView, VerdictBadge } from '../../components/ui'
import { useAsyncData } from '../../components/useAsyncData'

type BadgeTone = 'green' | 'amber' | 'red' | 'mute'

/** 事件大类 → 中文。 */
const KIND_LABEL: Record<EventKind, string> = {
  KIND_UNSPECIFIED: '未知',
  KIND_TRAFFIC: '流量',
  KIND_SENSOR: '传感',
  KIND_INTEL: '情报',
  KIND_AGENT: 'Agent',
}

/** 检测层级 → 中文（修复连续谱 L0–L3，docs/glossary.md#repair-continuum）。 */
const TIER_LABEL: Record<Tier, string> = {
  TIER_UNSPECIFIED: '未知层级',
  TIER_L0_REPORT: '只出报告 L0',
  TIER_L1_TRAFFIC: '流量拦截 L1',
  TIER_L2_RUNTIME: '运行时约束 L2',
  TIER_L3_COLD_PATCH: '冷补丁 L3',
}

/** 发布模式 → 徽章（语义色与发布状态一致：小比例 amber、全量 green）。 */
const MODE_BADGE: Record<ReleaseMode, { label: string; tone: BadgeTone }> = {
  RELEASE_MODE_UNSPECIFIED: { label: '未知', tone: 'mute' },
  RELEASE_MODE_SHADOW: { label: '影子', tone: 'mute' },
  RELEASE_MODE_CANARY: { label: '小比例', tone: 'amber' },
  RELEASE_MODE_ENFORCE: { label: '全量', tone: 'green' },
}

/** 详情页面板：fs-panel 头 + 正文，action 放头部右侧。 */
function Panel({ title, sub, action, children }: { title: string; sub: string; action?: ReactNode; children: ReactNode }) {
  return (
    <section className="fs-panel" aria-label={title}>
      <div className="fs-panel-head">
        <div>
          <p className="fs-panel-title">{title}</p>
          <p className="fs-panel-sub mt-1">{sub}</p>
        </div>
        {action}
      </div>
      {children}
    </section>
  )
}

function HttpPanel({ http }: { http: HttpPayload }) {
  const headers = Object.entries(http.headersRedacted ?? {})
  return (
    <Panel title="HTTP 载荷" sub="HTTP PAYLOAD">
      <div className="flex flex-col gap-4 px-[18px] py-4">
        <p className="fs-mono text-sm">
          {http.method} {http.path}
        </p>
        <dl className="yf-kv">
          <dt>状态码</dt>
          <dd className="fs-mono">{http.statusCode}</dd>
          <dt>耗时</dt>
          <dd className="fs-mono">{(Number(http.latencyMicros) / 1000).toFixed(1)} ms</dd>
          <dt>源</dt>
          <dd className="fs-mono">{http.srcPseudonym}</dd>
          <dt>目的</dt>
          <dd className="fs-mono">{http.dst}</dd>
        </dl>
        {http.queryRedacted !== '' && (
          <div>
            <p className="mb-1.5 text-xs text-[#8b98a1]">查询串（已脱敏）</p>
            <pre className="yf-code">{http.queryRedacted}</pre>
          </div>
        )}
        {headers.length > 0 && (
          <div>
            <p className="mb-1.5 text-xs text-[#8b98a1]">请求头（已脱敏）</p>
            <table aria-label="脱敏请求头" className="fs-mono w-full text-xs">
              <tbody>
                {headers.map(([k, v]) => (
                  <tr key={k} className="border-b border-[#1d252a] last:border-b-0">
                    <td className="w-40 py-1.5 pr-4 text-[#8b98a1]">{k}</td>
                    <td className="py-1.5">{v}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {http.bodyRedacted !== '' && (
          <div>
            <p className="mb-1.5 text-xs text-[#8b98a1]">脱敏后片段（base64）</p>
            <pre className="yf-code">{http.bodyRedacted}</pre>
          </div>
        )}
      </div>
    </Panel>
  )
}

function AiPanel({ ai }: { ai: AiPayload }) {
  const roles = Object.entries(ai.roleCounts ?? {})
  const toolCalls = ai.toolCalls ?? []
  return (
    <Panel title="人工智能载荷" sub="ARTIFICIAL INTELLIGENCE PAYLOAD">
      <div className="flex flex-col gap-4 px-[18px] py-4">
        <dl className="yf-kv">
          <dt>提供方</dt>
          <dd>{ai.provider}</dd>
          <dt>模型</dt>
          <dd className="fs-mono">{ai.model}</dd>
          <dt>消息角色</dt>
          <dd className="flex flex-wrap gap-1.5">
            {roles.length === 0 ? '—' : roles.map(([role, count]) => <Badge key={role} label={`${role}=${count}`} tone="mute" />)}
          </dd>
        </dl>
        {toolCalls.length > 0 && (
          <div>
            <p className="mb-1.5 text-xs text-[#8b98a1]">工具调用</p>
            <div className="divide-y divide-[#1d252a]">
              {toolCalls.map((c, i) => (
                <div key={`${c.name}-${i}`} className="flex items-baseline gap-3 py-1.5 text-xs">
                  <span className="fs-mono">{c.name}</span>
                  <span className="fs-mono min-w-0 flex-1 truncate text-[#8b98a1]">{c.argsDigest}</span>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </Panel>
  )
}

/** 误报举报弹窗：选归属发布 + 举报说明（1–2000 字），成功后原地显示回执。 */
function DenyFeedbackDialog({ open, event, onClose }: { open: boolean; event: Event; onClose: () => void }) {
  const { client } = useAuth()
  const firstReleaseId = (event.releaseTraces ?? [])[0]?.releaseId ?? ''
  const [releaseId, setReleaseId] = useState(firstReleaseId)
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState(false)

  const noteTooLong = note.length > 2000
  const canSubmit = releaseId !== '' && note.trim() !== '' && !noteTooLong

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.denyFeedback(releaseId, event.id, note.trim())
      setDone(true)
    } catch (e) {
      setError(isApiError(e) ? `${e.message}（${e.code}）` : '提交失败')
    } finally {
      setBusy(false)
    }
  }

  const close = () => {
    setReleaseId(firstReleaseId)
    setNote('')
    setError(null)
    setDone(false)
    onClose()
  }

  return (
    <Modal isOpen={open} onClose={close} placement="center" radius="lg">
      <ModalContent>
        <ModalHeader>举报误报</ModalHeader>
        <ModalBody className="gap-3">
          {done ? (
            <p className="text-sm text-[#62e6a7]">已提交举报，将计入该发布的误报计数</p>
          ) : (
            <>
              <Select
                label="归属发布"
                radius="md"
                selectedKeys={releaseId === '' ? [] : [releaseId]}
                disallowEmptySelection
                onChange={(e) => setReleaseId(e.target.value)}
              >
                {(event.releaseTraces ?? []).map((t) => (
                  <SelectItem key={t.releaseId}>{t.releaseId}</SelectItem>
                ))}
              </Select>
              <Textarea
                label="举报说明"
                placeholder="必填，说明判定为误报的理由"
                value={note}
                onValueChange={setNote}
                isRequired
                minRows={3}
                radius="md"
                description={`${note.length}/2000`}
                isInvalid={noteTooLong}
                errorMessage={noteTooLong ? '说明不能超过 2000 字' : undefined}
              />
              {error !== null && <p className="text-xs text-[#ff746c]">{error}</p>}
            </>
          )}
        </ModalBody>
        <ModalFooter>
          <Button variant="light" radius="md" onPress={close} isDisabled={busy}>
            {done ? '关闭' : '取消'}
          </Button>
          {!done && (
            <Button color="primary" radius="md" isLoading={busy} isDisabled={!canSubmit} onPress={submit}>
              确认举报
            </Button>
          )}
        </ModalFooter>
      </ModalContent>
    </Modal>
  )
}

export function EventDetailPage() {
  const { client, canOnAsset } = useAuth()
  const { eventId } = useParams<{ eventId: string }>()
  const reportModal = useDisclosure()
  const { data: event, status, error, reload } = useAsyncData(() => client.getEvent(eventId ?? ''), [eventId], false)

  if (status === 'error' && error !== null) {
    if (hasCode(error, 'permission_denied')) return <StateView kind="denied" />
    if (hasCode(error, 'not_found')) {
      return <StateView kind="error" title="事件不存在" message={`未找到事件 ${eventId ?? ''}`} onRetry={reload} />
    }
    return <StateView kind="error" message={error.message} onRetry={reload} />
  }
  if (status !== 'ok' || event === null) return <StateView kind="loading" />

  // 与服务端校验一致的前置条件（§7.8），不满足时连按钮都不渲染
  const canReport =
    canOnAsset('govern.deny_feedback', event.assetId) && event.verdict === 'VERDICT_BLOCK' && (event.releaseTraces ?? []).length > 0
  const labels = Object.entries(event.labels ?? {})
  const detections = event.detections ?? []
  const traces = event.releaseTraces ?? []

  return (
    <>
      <section className="fs-panel" aria-label="事件概要">
        <div className="fs-panel-head">
          <div>
            <div className="flex items-center gap-2">
              <VerdictBadge verdict={event.verdict} />
              <Badge label={KIND_LABEL[event.kind] ?? KIND_LABEL.KIND_UNSPECIFIED} tone="mute" />
            </div>
            <p className="fs-mono mt-2 text-lg font-semibold">{formatTime(event.occurredAt)}</p>
          </div>
          <p className="fs-panel-sub">EVENT / DETAIL</p>
        </div>
      </section>

      <Panel title="元数据" sub="METADATA">
        <dl className="yf-kv px-[18px] py-4">
          <dt>事件标识</dt>
          <dd className="fs-mono">{event.id}</dd>
          <dt>请求标识</dt>
          <dd className="fs-mono">{event.requestId}</dd>
          <dt>资产</dt>
          <dd>
            <Link to={`/assets/${event.assetId}`} className="fs-mono text-[#62e6a7] hover:underline">
              {event.assetId}
            </Link>
          </dd>
          <dt>单元</dt>
          <dd className="fs-mono">{event.unitId}</dd>
          <dt>来源</dt>
          <dd>{event.source}</dd>
		  <dt>入口姿态</dt>
		  <dd className="fs-mono">{event.ingressPosture}</dd>
		  <dt>观察状态</dt>
		  <dd className="fs-mono">{event.observation}</dd>
		  <dt>研判原因</dt>
		  <dd className="fs-mono">{event.triageReason}</dd>
		  <dt>资产世代</dt>
		  <dd className="fs-mono">
			{event.generationId || '—'} / {event.generationSeq}
		  </dd>
		  <dt>流量键</dt>
		  <dd className="fs-mono">{event.trafficKey || '—'}</dd>
		  <dt>观察态会拦截</dt>
		  <dd>{event.wouldHaveBlocked ? '是' : '否'}</dd>
          <dt>标签</dt>
          <dd className="flex flex-wrap gap-1.5">
            {labels.length === 0 ? '—' : labels.map(([k, v]) => <Badge key={k} label={`${k}=${v}`} tone="mute" />)}
          </dd>
        </dl>
      </Panel>

      {event.http !== undefined && <HttpPanel http={event.http} />}
      {event.ai !== undefined && <AiPanel ai={event.ai} />}

	  <Panel title="检查覆盖度" sub="INSPECTION COVERAGE">
		{event.coverage.length === 0 ? (
		  <StateView kind="empty" title="无覆盖度记录" message="" />
		) : (
		  <div>
			{event.coverage.map((coverage) => (
			  <div key={coverage.target} className="fs-row">
				<span className="fs-mono">{coverage.target}</span>
				<Badge label={coverage.status} tone={coverage.status === 'COVERAGE_STATUS_FULL' ? 'green' : 'amber'} />
				<span className="fs-mono text-[11px] text-[#8b98a1]">
				  {coverage.inspectedBytes} / {coverage.totalBytesKnown || '未知'} bytes
				</span>
			  </div>
			))}
		  </div>
		)}
	  </Panel>

      <Panel title="检测结论" sub="DETECTIONS">
        {detections.length === 0 ? (
          <StateView kind="empty" title="无检测结论" message="" />
        ) : (
          <div>
            {detections.map((d, i) => (
              <div key={`${d.detectorId}:${d.ruleId}:${i}`} className="fs-row">
                <span className="fs-mono">{d.ruleId}</span>
                <span className="fs-mono text-[11px] text-[#8b98a1]">{d.detectorId}</span>
                <Badge label={TIER_LABEL[d.tier] ?? TIER_LABEL.TIER_UNSPECIFIED} tone="mute" />
                <span className="fs-mono text-[11px] text-[#8b98a1]">{Math.round(d.confidence * 100)}%</span>
                <span className="fs-row-note">{d.message}</span>
				{d.attackClass !== undefined && <span className="fs-mono text-[11px] text-[#8b98a1]">{d.attackClass}</span>}
				{d.taxonomyVersion && <span className="fs-mono text-[11px] text-[#8b98a1]">taxonomy={d.taxonomyVersion}</span>}
				{d.matchedVariable && <span className="fs-mono text-[11px] text-[#8b98a1]">variable={d.matchedVariable}</span>}
				{d.evidenceSpan && <span className="fs-mono text-[11px] text-[#8b98a1]">span={d.evidenceSpan}</span>}
				{d.inspectionCoverageRef && <span className="fs-mono text-[11px] text-[#8b98a1]">coverage={d.inspectionCoverageRef}</span>}
				{(d.rawTags?.length ?? 0) > 0 && <span className="fs-mono text-[11px] text-[#8b98a1]">tags={d.rawTags?.join(',')}</span>}
              </div>
            ))}
          </div>
        )}
      </Panel>

      <Panel
        title="发布轨迹"
        sub="RELEASE TRACES"
        action={
          canReport ? (
            <Button size="sm" radius="md" color="primary" startContent={<Flag size={13} aria-hidden />} onPress={reportModal.onOpen}>
              举报误报
            </Button>
          ) : undefined
        }
      >
        {traces.length === 0 ? (
          <StateView kind="empty" title="关键事件才记录发布轨迹" message="" />
        ) : (
          <div>
            {traces.map((t) => {
              const mode = MODE_BADGE[t.mode] ?? MODE_BADGE.RELEASE_MODE_UNSPECIFIED
              return (
                <div key={t.releaseId} className="fs-row">
                  <Link to={`/releases/${t.releaseId}`} className="fs-mono text-[#62e6a7] hover:underline">
                    {t.releaseId}
                  </Link>
                  <Badge label={mode.label} tone={mode.tone} />
                  {t.canaryPercent > 0 && <span className="fs-mono text-[11px] text-[#f1be5b]">灰度 {t.canaryPercent}%</span>}
                  <span className="ml-auto flex items-center gap-4">
                    <span className="flex items-center gap-1.5 text-[11px] text-[#8b98a1]">
                      <span className={`fs-dot${t.canarySelected ? '' : ' fs-dot--mute'}`} aria-hidden />
                      {t.canarySelected ? '灰度选中' : '未选中'}
                    </span>
                    <span className="flex items-center gap-1.5 text-[11px] text-[#8b98a1]">
                      <span className={`fs-dot${t.matched ? '' : ' fs-dot--mute'}`} aria-hidden />
                      {t.matched ? '规则命中' : '未命中'}
                    </span>
                  </span>
                </div>
              )
            })}
          </div>
        )}
        {!canReport && (
          <p className="border-t border-[#1d252a] px-[18px] py-2.5 text-[11px] text-[#8b98a1]">
            仅 canary/enforce 发布的 BLOCK 事件可举报误报（docs/api.md §7.8）
          </p>
        )}
      </Panel>

      {canReport && <DenyFeedbackDialog open={reportModal.isOpen} event={event} onClose={reportModal.onClose} />}
    </>
  )
}
