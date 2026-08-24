import {
  Button,
  Checkbox,
  Input,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from '@heroui/react'
import { useState } from 'react'
import { isApiError } from '../../api/errors'
import type { AssetDetail, ManagedAgentProfile } from '../../api/types'
import { formatTime } from '../../components/format'

const TRAFFIC_REVIEW_AGENT_TOOLS = [
  { name: 'case.get', label: '读取案件', note: '闭环必需，只读脱敏案件摘要', required: true },
  { name: 'case.request_evidence', label: '请求证据', note: '闭环必需，只能发起一次性人工审批', required: true },
  { name: 'run.create', label: '分派调查', note: '闭环必需，创建短命、只读调查 run', required: true },
  { name: 'case.complete', label: '提交结论', note: '可选，只接受类型化流量结论', required: false },
] as const

const REQUIRED_TRAFFIC_REVIEW_TOOLS = TRAFFIC_REVIEW_AGENT_TOOLS.filter((tool) => tool.required).map((tool) => tool.name)

export type AgentDialogMode = 'create' | 'edit' | 'batch'

interface AgentProfileDialogProps {
  mode: AgentDialogMode
  open: boolean
  profile?: ManagedAgentProfile
  profiles: ManagedAgentProfile[]
  assets: AssetDetail[]
  onClose: () => void
  onCreate: (value: { displayName: string; tools: string[]; assetIds: string[] }) => Promise<void>
  onUpdate: (value: { agentId: string; displayName: string; enabled: boolean; tools: string[]; assetIds: string[] }) => Promise<void>
  onBatch: (value: { agentIds: string[]; tools: string[]; assetIds: string[] }) => Promise<void>
  onDelete: (agentId: string) => Promise<void>
}

function toggle(values: string[], value: string): string[] {
  return values.includes(value) ? values.filter((item) => item !== value) : [...values, value]
}

export function AgentProfileDialog({
  mode,
  open,
  profile,
  profiles,
  assets,
  onClose,
  onCreate,
  onUpdate,
  onBatch,
  onDelete,
}: AgentProfileDialogProps) {
  const [displayName, setDisplayName] = useState(profile?.displayName ?? '')
  const [enabled, setEnabled] = useState(profile?.state === 'AGENT_PROFILE_STATE_ENABLED')
  const [tools, setTools] = useState<string[]>(
    Array.from(new Set([...(profile?.tools ?? TRAFFIC_REVIEW_AGENT_TOOLS.map((tool) => tool.name)), ...REQUIRED_TRAFFIC_REVIEW_TOOLS])),
  )
  const [assetIds, setAssetIds] = useState<string[]>(
    profile?.bindings.filter((binding) => binding.kind === 'asset').map((binding) => binding.id) ?? [],
  )
  const [agentIds, setAgentIds] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const title = mode === 'create' ? '新增 Agent' : mode === 'batch' ? '批量设置 Agent' : `编辑 ${profile?.displayName ?? 'Agent'}`
  const valid = mode === 'batch'
    ? agentIds.length > 0 && tools.length > 0 && assetIds.length > 0
    : displayName.trim() !== '' && tools.length > 0 && assetIds.length > 0

  const submit = async () => {
    if (!valid) return
    setBusy(true)
    setError(null)
    try {
      if (mode === 'create') await onCreate({ displayName: displayName.trim(), tools, assetIds })
      else if (mode === 'batch') await onBatch({ agentIds, tools, assetIds })
      else if (profile) await onUpdate({ agentId: profile.agentId, displayName: displayName.trim(), enabled, tools, assetIds })
      onClose()
    } catch (cause) {
      setError(isApiError(cause) ? `${cause.message}（${cause.reasonKey ?? cause.code}）` : '保存失败，请重试')
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!profile) return
    setBusy(true)
    setError(null)
    try {
      await onDelete(profile.agentId)
      onClose()
    } catch (cause) {
      setError(isApiError(cause) ? `${cause.message}（${cause.reasonKey ?? cause.code}）` : '删除失败，请重试')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal isOpen={open} onClose={onClose} size="2xl" scrollBehavior="inside" placement="center" radius="lg" isDismissable={!busy}>
      <ModalContent className="yf-agent-dialog">
        <ModalHeader className="yf-agent-dialog-head flex flex-col items-start gap-1">
          <span>{title}</span>
          <span className="text-xs font-normal text-[#8b98a1]">当前仅支持审查 yufeng-edge 形成的流量案件；档案不会安装进程或绕过证据审批。</span>
        </ModalHeader>
        <ModalBody className="yf-agent-dialog-body gap-5">
          {mode === 'batch' ? (
            <fieldset className="space-y-2">
              <legend className="text-xs font-semibold text-[#b8c9c7]">选择 Agent</legend>
              <div className="grid gap-2 sm:grid-cols-2">
                {profiles.map((item) => (
                  <Checkbox key={item.agentId} isSelected={agentIds.includes(item.agentId)} onValueChange={() => setAgentIds(toggle(agentIds, item.agentId))}>
                    <span className="text-sm">{item.displayName}</span>
                  </Checkbox>
                ))}
              </div>
            </fieldset>
          ) : (
            <div className="space-y-3">
              <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
                <Input label="Agent 名称" value={displayName} onValueChange={setDisplayName} maxLength={80} radius="md" isRequired />
                {mode === 'edit' && <Checkbox isSelected={enabled} onValueChange={setEnabled}>允许领取新案件</Checkbox>}
              </div>
              {mode === 'edit' && profile !== undefined && (
                <dl className="yf-kv rounded-lg border border-[#294147] bg-[#0b171a] p-3 text-xs">
                  <dt>执行模式</dt><dd>短命分布式 run</dd>
                  <dt>活动 run</dt><dd>{profile.activeRunCount ?? 0}</dd>
                  <dt>最近 Worker</dt><dd>{profile.lastWorkerId ? `${profile.lastWorkerId} · ${profile.lastWorkerPlatform || '平台未报告'}` : '尚未分派'}</dd>
                  <dt>最近执行</dt><dd>{profile.lastRunAt ? formatTime(profile.lastRunAt) : '尚未执行'}</dd>
                  <dt>配置摘要</dt><dd className="font-mono break-all">{profile.configDigest || '保存后生成'}</dd>
                </dl>
              )}
            </div>
          )}

          <fieldset className="space-y-2">
            <legend className="text-xs font-semibold text-[#b8c9c7]">流量审查工具</legend>
            <div className="grid gap-2 sm:grid-cols-2">
              {TRAFFIC_REVIEW_AGENT_TOOLS.map((tool) => (
                <Checkbox
                  key={tool.name}
                  isSelected={tools.includes(tool.name)}
                  isDisabled={tool.required}
                  onValueChange={() => setTools(toggle(tools, tool.name))}
                >
                  <span className="block text-sm">{tool.label}</span>
                  <span className="block text-[11px] text-[#8b98a1]">{tool.note}</span>
                </Checkbox>
              ))}
            </div>
          </fieldset>

          <fieldset className="space-y-2">
            <legend className="text-xs font-semibold text-[#b8c9c7]">需要维护的资产</legend>
            <div className="grid gap-2 sm:grid-cols-2">
              {assets.map((item) => (
                <Checkbox key={item.asset.id} isSelected={assetIds.includes(item.asset.id)} onValueChange={() => setAssetIds(toggle(assetIds, item.asset.id))}>
                  <span className="text-sm">{item.asset.displayName}</span>
                  <span className="ml-2 font-mono text-[11px] text-[#8b98a1]">{item.asset.id}</span>
                </Checkbox>
              ))}
            </div>
          </fieldset>

          {mode === 'batch' && <p className="text-xs text-[#b9aa80]">批量保存会用上面的工具和资产统一覆盖所选 Agent；启停状态保持不变。</p>}
          {error !== null && <p className="text-xs text-[#ff746c]">{error}</p>}
          {mode === 'edit' && confirmDelete && (
            <div className="rounded-lg border border-[#6f3935] bg-[#241513] p-3 text-xs text-[#f3aaa4]">
              删除只阻止新案件委派，历史案件和审计记录仍保留。确认删除这个 Agent？
              <div className="mt-3 flex gap-2">
                <Button size="sm" color="danger" isLoading={busy} onPress={() => void remove()}>确认删除</Button>
                <Button size="sm" variant="light" onPress={() => setConfirmDelete(false)}>取消</Button>
              </div>
            </div>
          )}
        </ModalBody>
        <ModalFooter className="yf-agent-dialog-foot justify-between">
          <div>
            {mode === 'edit' && !confirmDelete && (
              <Button color="danger" variant="light" size="sm" onPress={() => setConfirmDelete(true)}>删除 Agent</Button>
            )}
          </div>
          <div className="flex gap-2">
            <Button variant="light" onPress={onClose}>取消</Button>
            <Button color="primary" isLoading={busy} isDisabled={!valid || confirmDelete} onPress={() => void submit()}>
              {mode === 'batch' ? '应用批量设置' : '保存'}
            </Button>
          </div>
        </ModalFooter>
      </ModalContent>
    </Modal>
  )
}
