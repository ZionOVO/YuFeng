import { Button } from '@heroui/react'
import { AlertTriangle, Bot, CheckCircle2, Clock3, Cpu, FileSearch, ShieldQuestion } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { canOnAsset, hasTool } from '../../api/access'
import type {
  ApprovalView,
  InvestigationCase,
  TrafficFinding,
  WorkerRecord,
  WorkerEnrollmentDecision,
  WorkerEnrollmentRecord,
} from '../../api/types'
import { useAuth } from '../../auth/useAuth'
import { formatTime } from '../format'
import { Badge, StateView } from '../ui'

const CASE_STATE: Record<string, { label: string; tone: 'green' | 'amber' | 'red' | 'mute' }> = {
  INVESTIGATION_CASE_STATE_OPEN: { label: '待编排', tone: 'mute' },
  INVESTIGATION_CASE_STATE_WAITING_EVIDENCE_APPROVAL: { label: '等待证据批准', tone: 'amber' },
  INVESTIGATION_CASE_STATE_QUEUED: { label: '已排队', tone: 'mute' },
  INVESTIGATION_CASE_STATE_INVESTIGATING: { label: '调查中', tone: 'mute' },
  INVESTIGATION_CASE_STATE_FINDING_READY: { label: '结论就绪', tone: 'green' },
  INVESTIGATION_CASE_STATE_SHADOW_OBSERVING: { label: 'Shadow 观察', tone: 'green' },
  INVESTIGATION_CASE_STATE_RESOLVED: { label: '已解决', tone: 'mute' },
  INVESTIGATION_CASE_STATE_FAILED: { label: '失败', tone: 'red' },
  INVESTIGATION_CASE_STATE_EVIDENCE_EXPIRED: { label: '证据过期', tone: 'red' },
}

export function CaseCard({ item, selected = false, onSelect }: { item: InvestigationCase; selected?: boolean; onSelect?: () => void }) {
  const state = CASE_STATE[item.state] ?? { label: item.state, tone: 'mute' as const }
  return (
    <button
      type="button"
      className={`w-full rounded-xl border p-4 text-left transition ${selected ? 'border-[#43d7b0] bg-[#102a29]' : 'border-[#26383d] bg-[#0b171a] hover:border-[#3b555b]'}`}
      onClick={onSelect}
      aria-pressed={selected}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="mb-1 flex flex-wrap items-center gap-2">
            <Badge label={`优先级 ${item.priority}`} tone={item.priority >= 80 ? 'red' : item.priority >= 50 ? 'amber' : 'mute'} />
            <Badge label={state.label} tone={state.tone} />
          </div>
          <p className="truncate text-sm font-semibold text-[#e5f2f0]">{item.title}</p>
          <p className="mt-1 line-clamp-2 text-xs leading-5 text-[#8fa2a5]">{item.summary || '尚无冻结摘要'}</p>
        </div>
        <ShieldQuestion size={18} className="mt-1 shrink-0 text-[#43d7b0]" aria-hidden />
      </div>
      <div className="mt-3 flex items-center justify-between gap-2 text-[11px] text-[#71888c]">
        <span className="min-w-0 truncate">{item.assignedAgentDisplayName !== '' ? `Agent · ${item.assignedAgentDisplayName}` : '等待 Agent 配置'}</span>
        <span>{formatTime(item.updatedAt)}</span>
      </div>
      <p className="mt-1 truncate text-[10px] text-[#5f777b]">资产 · <span className="fs-mono">{item.assetId}</span></p>
    </button>
  )
}

export function EvidenceApprovalCard({ approvalId, onDecided }: { approvalId: string; onDecided?: () => void }) {
  const { client, access } = useAuth()
  const [approval, setApproval] = useState<ApprovalView | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const load = () => {
    void client.getApproval(approvalId).then(setApproval).catch((cause: unknown) => setError(cause instanceof Error ? cause.message : '读取审批失败'))
  }
  useEffect(load, [approvalId, client])

  if (error !== null) return <StateView kind="error" title="审批不可读取" message={error} />
  if (approval === null) return <StateView kind="loading" />
  if (approval.kind === 'APPROVAL_KIND_WORKER_CAPACITY') {
    return <WorkerCapacityApprovalCard approval={approval} onDecided={onDecided} />
  }
  const canDecide = approval.state === 'pending' && canOnAsset(access, 'evidence.approve', approval.assetId)
  const decide = async (approved: boolean) => {
    setBusy(true)
    setError(null)
    try {
      await client.decideApproval({ approvalId, approved })
      load()
      onDecided?.()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '审批失败')
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="rounded-xl border border-[#705c2e] bg-[#231e12] p-4" aria-label="证据审批">
      <div className="flex items-start justify-between gap-3">
        <div>
          <p className="flex items-center gap-2 text-sm font-semibold text-[#f2d88d]"><FileSearch size={16} aria-hidden />一次性证据访问</p>
          <p className="mt-1 text-xs text-[#b9aa80]">批准在模型调用开始时消费；原始证据只经过内存中继。</p>
        </div>
        <Badge label={approval.state} tone={approval.state === 'pending' ? 'amber' : approval.state === 'approved' ? 'green' : 'mute'} />
      </div>
      <dl className="yf-kv mt-3 text-xs">
        <dt>案件 / 资产</dt><dd className="fs-mono">{approval.caseId} / {approval.assetId}</dd>
        <dt>实际模型</dt><dd>{approval.modelHost} / {approval.modelName}</dd>
        <dt>配置摘要</dt><dd className="fs-mono break-all">{approval.modelConfigDigest}</dd>
        <dt>允许字段</dt><dd>{approval.allowedFields.join('、')}</dd>
        <dt>字节上限</dt><dd>{Number(approval.maxBytes).toLocaleString()} B</dd>
        <dt>有效期至</dt><dd>{formatTime(approval.expiresAt)}</dd>
      </dl>
      {canDecide && (
        <div className="mt-4 flex justify-end gap-2">
          <Button size="sm" radius="md" variant="bordered" isDisabled={busy} onPress={() => void decide(false)}>拒绝</Button>
          <Button size="sm" radius="md" color="warning" isLoading={busy} onPress={() => void decide(true)}>批准一次</Button>
        </div>
      )}
    </section>
  )
}

export function RunProgressCard({ summary, occurredAt }: { summary: string; occurredAt?: string }) {
  return <ActivityCard icon={<Bot size={16} />} title="调查 run" summary={summary} detail={formatTime(occurredAt)} tone="blue" />
}

export function TrafficFindingCard({ finding }: { finding: TrafficFinding }) {
  const insufficient = finding.disposition === 'TRAFFIC_FINDING_DISPOSITION_INSUFFICIENT_EVIDENCE'
  const dangerous = finding.disposition === 'TRAFFIC_FINDING_DISPOSITION_SUSPECTED_MISS' || finding.disposition === 'TRAFFIC_FINDING_DISPOSITION_SUSPECTED_MALICIOUS'
  return (
    <section className="rounded-xl border border-[#294147] bg-[#0b171a] p-4" aria-label="流量研判结论">
      <div className="flex items-center justify-between gap-3">
        <p className="flex items-center gap-2 text-sm font-semibold"><CheckCircle2 size={16} className={dangerous ? 'text-[#ff8c81]' : 'text-[#62e6a7]'} />研判结论</p>
        <Badge label={`${Math.round(finding.confidence * 100)}%`} tone={insufficient ? 'mute' : dangerous ? 'red' : 'green'} />
      </div>
      <p className="mt-3 text-sm text-[#d5e4e2]">{finding.rationale}</p>
      <dl className="yf-kv mt-3 text-xs">
        <dt>分类</dt><dd>{finding.disposition}</dd>
        <dt>路由</dt><dd className="fs-mono">{finding.routeTemplate || '—'}</dd>
        <dt>攻击类别</dt><dd>{finding.attackClass || '—'}</dd>
      </dl>
      {finding.disposition === 'TRAFFIC_FINDING_DISPOSITION_SUSPECTED_MISS' && finding.optionalShapeDraft ? (
        <div className="mt-4 rounded-xl border border-amber-400/20 bg-amber-400/5 p-3 text-xs text-amber-100">
          <p className="font-semibold">待协调请求形状</p>
          <p className="mt-1 break-words fs-mono">
            {(finding.optionalShapeDraft.methods ?? []).join(', ') || '方法待核对'} · {finding.optionalShapeDraft.routeTemplate || finding.optionalShapeDraft.pathPrefix || '路由待核对'}
          </p>
          <p className="mt-1 text-amber-100/70">该草案仍需人工批准、历史世代校验与代表样本回放，不能直接进入生效。</p>
        </div>
      ) : null}
    </section>
  )
}

export function ShadowCandidateCard({ releaseId = '', summary }: { releaseId?: string; summary: string }) {
  return (
    <ActivityCard
      icon={<AlertTriangle size={16} />}
      title="Shadow 候选"
      summary={summary}
      detail={releaseId !== '' ? <Link className="text-[#62e6a7] underline" to={`/releases/${releaseId}`}>查看防护策略</Link> : '等待确定性协调器核验'}
      tone="yellow"
    />
  )
}

export function WorkerEnrollmentCard({ enrollment, bindings, onDecided }: { enrollment: WorkerEnrollmentRecord; bindings: string[]; onDecided?: () => void }) {
  const { client, access } = useAuth()
  const [busy, setBusy] = useState(false)
  const [decided, setDecided] = useState(false)
	const [decision, setDecision] = useState<WorkerEnrollmentDecision | null>(null)
  const [error, setError] = useState<string | null>(null)
  const canDecide = !decided && enrollment.state === 'pending' && hasTool(access, 'worker.enroll')
  const canApprove = canDecide && bindings.length > 0
  const decide = async (approved: boolean) => {
    setBusy(true)
    setError(null)
    try {
      const decision = await client.decideWorkerEnrollment({ enrollmentId: enrollment.enrollmentId, approved, bindings, maxConcurrency: 1 })
      if (approved) setDecision(decision)
      setDecided(true)
      if (!approved) onDecided?.()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '决定 worker 注册失败')
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="rounded-xl border border-[#294147] bg-[#0b171a] p-4" aria-label="外部执行进程注册">
      <div className="flex items-center justify-between gap-3"><p className="flex items-center gap-2 text-sm font-semibold"><Cpu size={16} />{enrollment.workerId}</p><Badge label={decision?.state ?? enrollment.state} tone={decision === null && enrollment.state === 'pending' ? 'amber' : 'green'} /></div>
	  <p className="mt-2 text-xs text-[#9bb0b2]">{enrollment.hostname} · {enrollment.operatingSystem}/{enrollment.architecture} · {enrollment.version || '版本未报告'}</p>
      <p className="fs-mono mt-2 break-all text-[11px] text-[#71888c]">{enrollment.publicKeyFingerprint}</p>
	  <p className="fs-mono mt-1 break-all text-[11px] text-[#71888c]">激活公钥：{enrollment.activationPublicKeyFingerprint || '未提交'}</p>
	  <p className="mt-2 text-xs text-[#8fa2a5]">容量：{enrollment.logicalCpuCapacity || 0} 逻辑处理器 · {Math.round(Number(enrollment.memoryCapacityBytes || '0') / 1024 / 1024)} MiB</p>
      <p className="mt-2 text-xs text-[#8fa2a5]">沙箱：{enrollment.sandboxCapabilities.join('、') || '未验证；可登记但不能领取调查任务'}</p>
      {canDecide && bindings.length === 0 && <p className="mt-2 text-xs text-[#f2d88d]">先按资产筛选案件，再把该资产作为这台 worker 的唯一初始授权边界。</p>}
      {error !== null && <p className="mt-2 text-xs text-[#ff8c81]">{error}</p>}
	  {canDecide && <div className="mt-3 flex flex-wrap justify-end gap-2"><Button size="sm" variant="bordered" isDisabled={busy} onPress={() => void decide(false)}>拒绝</Button><Button size="sm" color="primary" isLoading={busy} isDisabled={!canApprove} onPress={() => void decide(true)}>批准登记</Button></div>}
	  {decision !== null && <div className="mt-3 rounded-lg border border-[#31564f] bg-[#10231f] p-3">
		<p className="text-xs font-semibold text-[#77d6b0]">已生成加密激活包</p>
		<p className="mt-1 text-xs leading-5 text-[#9bb0b2]">激活包只能由持有本机 X25519 私钥的客户端取得和解密，浏览器不接触引导令牌或客户端证书。</p>
		<p className="fs-mono mt-1 break-all text-[10px] text-[#71888c]">{decision.activationBundleRef} · {decision.approvedManifestDigest}</p>
	  </div>}
    </section>
  )
}

export function WorkerRecordCard({ worker }: { worker: WorkerRecord }) {
  return <ActivityCard icon={<Cpu size={16} />} title={`${worker.workerId} · ${worker.operatingSystem}/${worker.architecture}`}
    summary={worker.investigationEligible
      ? `沙箱已验证：${worker.sandboxCapabilities.join('、')}`
      : `不能领取调查任务；缺少：${worker.missingSandboxCapabilities.join('、') || '受支持的平台沙箱'}`}
    detail={`并发 ${worker.maxConcurrency}`}
    tone={worker.investigationEligible ? 'green' : 'red'} />
}

export function WorkerCapacityApprovalCard({ approval, onDecided }: { approval: ApprovalView; onDecided?: () => void }) {
  const { client, access } = useAuth()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [decisionState, setDecisionState] = useState(approval.state)
  const canDecide = decisionState === 'pending' && hasTool(access, 'worker.capacity.approve')
  const decide = async (approved: boolean) => {
    setBusy(true)
    setError(null)
    try {
      const decision = await client.decideApproval({ approvalId: approval.approvalId, approved })
      setDecisionState(decision.state)
      onDecided?.()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '容量审批失败')
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="rounded-xl border border-[#294147] bg-[#0b171a] p-4" aria-label="执行池容量审批">
      <div className="flex items-center justify-between"><p className="text-sm font-semibold">中央调查池临时扩容</p><Badge label={decisionState} tone={decisionState === 'pending' ? 'amber' : decisionState === 'approved' ? 'green' : 'mute'} /></div>
      <p className="mt-2 text-xs text-[#8fa2a5]">{approval.workerId}：{approval.previousCapacity} → {approval.requestedCapacity}，到期自动恢复。</p>
      {error !== null && <p className="mt-2 text-xs text-[#ff8c81]">{error}</p>}
      {canDecide && <div className="mt-3 flex justify-end gap-2"><Button size="sm" variant="bordered" isDisabled={busy} onPress={() => void decide(false)}>拒绝</Button><Button size="sm" color="primary" isLoading={busy} onPress={() => void decide(true)}>批准</Button></div>}
    </section>
  )
}

function ActivityCard({ icon, title, summary, detail, tone }: { icon: ReactNode; title: string; summary: string; detail: ReactNode; tone: 'blue' | 'green' | 'yellow' | 'red' }) {
  return (
    <section className="rounded-xl border border-[#294147] bg-[#0b171a] p-4">
      <div className="flex items-center justify-between gap-3"><p className="flex items-center gap-2 text-sm font-semibold">{icon}{title}</p><Badge label={tone === 'blue' ? '进行中' : tone === 'green' ? '已验证' : tone === 'yellow' ? '待核验' : '失败关闭'} tone={tone === 'blue' ? 'mute' : tone === 'yellow' ? 'amber' : tone} /></div>
      <p className="mt-2 text-xs leading-5 text-[#9bb0b2]">{summary}</p>
      <div className="mt-2 flex items-center gap-1 text-[11px] text-[#71888c]"><Clock3 size={12} aria-hidden />{detail}</div>
    </section>
  )
}
