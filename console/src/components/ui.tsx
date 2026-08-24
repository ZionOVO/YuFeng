// 页面共享组件：状态视图、徽章、确认弹窗、分页器、时间与门槛渲染。
// 徽章沿用圆融主题的语义三件套（theme/fusion.css 的 fs-badge）。

import { Button, Modal, ModalBody, ModalContent, ModalFooter, ModalHeader, Spinner } from '@heroui/react'
import { ShieldAlert } from 'lucide-react'
import type { ReactNode } from 'react'
import type { GateCheck, ReleaseState, Verdict } from '../api/types'

/* ---------- 页面状态：loading / error / empty / denied ---------- */

export interface StateViewProps {
  kind: 'loading' | 'error' | 'empty' | 'denied'
  title?: string
  message?: string
  onRetry?: () => void
}

const DEFAULT_TEXT: Record<StateViewProps['kind'], { title: string; message: string }> = {
  loading: { title: '加载中', message: '' },
  error: { title: '加载失败', message: '请求出错，请重试' },
  empty: { title: '暂无数据', message: '当前筛选条件下没有记录' },
  denied: { title: '没有权限', message: '当前角色无权查看此内容' },
}

export function StateView({ kind, title, message, onRetry }: StateViewProps) {
  const text = DEFAULT_TEXT[kind]
  return (
    <div className="fs-panel flex flex-col items-center justify-center gap-3 px-6 py-16 text-center" role={kind === 'error' || kind === 'denied' ? 'alert' : undefined}>
      {kind === 'loading' && <Spinner size="lg" aria-label="加载中" />}
      {kind === 'denied' && <ShieldAlert size={28} className="text-[#f1be5b]" aria-hidden />}
      <p className="text-sm font-medium">{title ?? text.title}</p>
      {(message ?? text.message) !== '' && <p className="max-w-md text-xs text-[#8b98a1]">{message ?? text.message}</p>}
      {kind === 'error' && onRetry !== undefined && (
        <Button size="sm" radius="md" variant="bordered" onPress={onRetry}>
          重试
        </Button>
      )}
    </div>
  )
}

/* ---------- 语义徽章 ---------- */

type BadgeTone = 'green' | 'amber' | 'red' | 'mute'

const RELEASE_STATE_BADGE: Record<ReleaseState, { label: string; tone: BadgeTone }> = {
  RELEASE_STATE_UNSPECIFIED: { label: '未知', tone: 'mute' },
  RELEASE_STATE_DRAFT: { label: '草稿', tone: 'mute' },
  RELEASE_STATE_SIGNED: { label: '已签名', tone: 'mute' },
  RELEASE_STATE_SHADOW: { label: '影子', tone: 'mute' },
  RELEASE_STATE_CANARY: { label: '小比例', tone: 'amber' },
  RELEASE_STATE_ENFORCE: { label: '全量', tone: 'green' },
  RELEASE_STATE_RETIRED: { label: '已退休', tone: 'red' },
}

const VERDICT_BADGE: Record<Verdict, { label: string; tone: BadgeTone }> = {
  VERDICT_UNSPECIFIED: { label: '未知', tone: 'mute' },
  VERDICT_ALLOW: { label: 'ALLOW', tone: 'mute' },
  VERDICT_BLOCK: { label: 'BLOCK', tone: 'red' },
  VERDICT_OBSERVE: { label: 'OBSERVE', tone: 'amber' },
  VERDICT_ESCALATE: { label: 'ESCALATE', tone: 'amber' },
}

export function Badge({ label, tone }: { label: string; tone: BadgeTone }) {
  return <span className={`fs-badge fs-badge--${tone}`}>{label}</span>
}

export function ReleaseStateBadge({ state }: { state: ReleaseState }) {
  const b = RELEASE_STATE_BADGE[state] ?? RELEASE_STATE_BADGE.RELEASE_STATE_UNSPECIFIED
  return <Badge label={b.label} tone={b.tone} />
}

export function VerdictBadge({ verdict }: { verdict: Verdict }) {
  const b = VERDICT_BADGE[verdict] ?? VERDICT_BADGE.VERDICT_UNSPECIFIED
  return <Badge label={b.label} tone={b.tone} />
}

/** health 是 string（proto 定义），按 UnitHealth 枚举名宽松匹配。 */
export function HealthBadge({ health }: { health: string }) {
  // 契约枚举名与资产服务当前写入的小写 health 都认。
  if (health === 'UNIT_HEALTH_HEALTHY' || health === 'healthy') return <Badge label="健康" tone="green" />
  if (health === 'UNIT_HEALTH_DEGRADED' || health === 'degraded') return <Badge label="降级" tone="amber" />
  if (
    health === 'UNIT_HEALTH_TAP_SILENT' ||
    health === 'tap_silent' ||
    health === 'UNIT_HEALTH_TAP_SKEW' ||
    health === 'tap_skew'
  ) {
    return <Badge label="执行面可能看不见" tone="amber" />
  }
  return <Badge label="未知" tone="mute" />
}

/* ---------- 危险操作确认弹窗 ---------- */

export interface ConfirmDialogProps {
  open: boolean
  title: string
  /** 确认按钮文案与颜色。 */
  confirmLabel: string
  danger?: boolean
  busy?: boolean
  /** 确认不可用（如必填原因未填）。 */
  confirmDisabled?: boolean
  onConfirm: () => void
  onClose: () => void
  children: ReactNode
}

export function ConfirmDialog({ open, title, confirmLabel, danger = false, busy = false, confirmDisabled = false, onConfirm, onClose, children }: ConfirmDialogProps) {
  return (
    <Modal isOpen={open} onClose={onClose} placement="center" radius="lg">
      <ModalContent>
        <ModalHeader>{title}</ModalHeader>
        <ModalBody>{children}</ModalBody>
        <ModalFooter>
          <Button variant="light" radius="md" onPress={onClose} isDisabled={busy}>
            取消
          </Button>
          <Button color={danger ? 'danger' : 'primary'} radius="md" onPress={onConfirm} isLoading={busy} isDisabled={confirmDisabled}>
            {confirmLabel}
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  )
}

/* ---------- 不透明游标分页器（pageToken 只回传不解析，docs/api.md §0.6） ---------- */

export interface TokenPagerProps {
  /** 当前页号（从 1 开始）。 */
  page: number
  hasPrev: boolean
  hasNext: boolean
  onPrev: () => void
  onNext: () => void
}

export function TokenPager({ page, hasPrev, hasNext, onPrev, onNext }: TokenPagerProps) {
  return (
    <div className="flex items-center justify-end gap-3 px-4 py-2.5 text-xs text-[#8b98a1]">
      <span className="fs-mono">第 {page} 页</span>
      <Button size="sm" radius="md" variant="bordered" onPress={onPrev} isDisabled={!hasPrev}>
        上一页
      </Button>
      <Button size="sm" radius="md" variant="bordered" onPress={onNext} isDisabled={!hasNext}>
        下一页
      </Button>
    </div>
  )
}

/* ---------- 推进门槛失败展示（failed_precondition + GateResult，docs/api.md §17.7） ---------- */

export function GateChecksView({ checks }: { checks: GateCheck[] }) {
  return (
    <ul className="mt-2 space-y-1.5">
      {checks.map((g) => (
        <li key={g.gateKey} className="flex items-start gap-2 text-xs">
          <span className={`mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full ${g.passed ? 'bg-[#62e6a7]' : 'bg-[#ff746c]'}`} aria-hidden />
          <span className="fs-mono text-[#8b98a1]">{g.gateKey}</span>
          <span>
            {g.message}（要求 <span className="fs-mono">{g.required}</span>，当前 <span className="fs-mono">{g.actual}</span>）
          </span>
        </li>
      ))}
    </ul>
  )
}
