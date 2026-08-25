// 编排场：资产拓扑当台面，右侧复用同一套贾维斯对话（不是右下角弹窗）。
// SessionService 只认 Login.token + 属主（docs/api.md §18.5）。

import { Button } from '@heroui/react'
import { ArrowLeft, Map, MessageSquareText, Plus, SlidersHorizontal } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { PageSizeMax } from '../../api/limits'
import type { ConsoleClient, Page } from '../../api/client'
import type { InvestigationCase, InvestigationCaseState, ManagedAgentProfile, WorkerRecord } from '../../api/types'
import { useAuth } from '../../auth/useAuth'
import { YfChat } from '../../components/chat/YfChat'
import { useJarvisSession } from '../../components/chat/useJarvisSession'
import { AssetEstateMap } from '../../components/estate/AssetEstateMap'
import { useAsyncData } from '../../components/useAsyncData'
import { StateView } from '../../components/ui'
import { formatTime } from '../../components/format'
import { hasCode } from '../../api/errors'
import { CaseWorkspace } from '../../components/cases/CaseWorkspace'
import { CaseCard } from '../../components/cases/CaseCards'
import { AgentProfileDialog, type AgentDialogMode } from './AgentProfileDialog'

const ACTIVE_CASE_STATES: InvestigationCaseState[] = [
  'INVESTIGATION_CASE_STATE_OPEN',
  'INVESTIGATION_CASE_STATE_WAITING_EVIDENCE_APPROVAL',
  'INVESTIGATION_CASE_STATE_QUEUED',
  'INVESTIGATION_CASE_STATE_INVESTIGATING',
  'INVESTIGATION_CASE_STATE_FINDING_READY',
  'INVESTIGATION_CASE_STATE_SHADOW_OBSERVING',
]

async function collectPages<T>(load: (pageToken: string) => Promise<Page<T>>): Promise<Page<T>> {
  const items: T[] = []
  const seen = new Set<string>()
  let pageToken = ''
  do {
    if (seen.has(pageToken)) throw new Error('服务端返回了重复分页游标')
    seen.add(pageToken)
    const page = await load(pageToken)
    items.push(...page.items)
    pageToken = page.nextPageToken
  } while (pageToken !== '')
  return { items, nextPageToken: '' }
}

async function loadActiveCases(client: ConsoleClient): Promise<Page<InvestigationCase>> {
  const pages = await Promise.all(ACTIVE_CASE_STATES.map((state) => collectPages((pageToken) =>
    client.listCases({ state }, { pageSize: PageSizeMax, pageToken }),
  )))
  return { items: pages.flatMap((page) => page.items), nextPageToken: '' }
}

export function AgentPage() {
  const { client, user, onboarding, hasTool } = useAuth()
  const navigate = useNavigate()
  const { ensureSession, focusAssetIds, setContextLabel, messages, contextLabel, thinking, busy, error, jarvisOnline, refreshSignals, send } = useJarvisSession()
  const estate = useAsyncData(() => client.listAssets({}, { pageSize: PageSizeMax }), [client])
  const cases = useAsyncData(
    () => hasTool('case.read') ? loadActiveCases(client) : Promise.resolve({ items: [] as InvestigationCase[], nextPageToken: '' }),
    [client, hasTool],
  )
  const profiles = useAsyncData(() => collectPages<ManagedAgentProfile>((pageToken) => client.listAgentProfiles({ pageSize: PageSizeMax, pageToken })), [client])
  const workers = useAsyncData(
    () => hasTool('worker.enroll') ? collectPages<WorkerRecord>((pageToken) => client.listWorkers({ pageSize: PageSizeMax, pageToken })) : Promise.resolve({ items: [] as WorkerRecord[], nextPageToken: '' }),
    [client, hasTool],
  )
  const [selectedAssetId, setSelectedAssetId] = useState('')
  const [selectedCaseId, setSelectedCaseId] = useState('')
  const [selectedAgentId, setSelectedAgentId] = useState('')
  const [dialogMode, setDialogMode] = useState<AgentDialogMode | null>(null)
  const [mobilePane, setMobilePane] = useState<'map' | 'chat'>('map')
  const caseStats = useMemo(() => {
    const result: Record<string, { openCount: number; highestPriority: number }> = {}
    for (const item of cases.data?.items ?? []) {
      if (item.state === 'INVESTIGATION_CASE_STATE_RESOLVED') continue
      const current = result[item.assetId] ?? { openCount: 0, highestPriority: 0 }
      current.openCount += 1
      current.highestPriority = Math.max(current.highestPriority, item.priority)
      result[item.assetId] = current
    }
    return result
  }, [cases.data])
  const selectedAssetCases = (cases.data?.items ?? []).filter((item) => item.assetId === selectedAssetId)
  const profileItems = (profiles.data?.items ?? []).filter((profile) => profile.state !== 'AGENT_PROFILE_STATE_TOMBSTONED')
  const manageableProfiles = profileItems.filter((profile) => profile.canManage)
  const selectedProfile = profileItems.find((profile) => profile.agentId === selectedAgentId)
  const profileFocusAssetIds = selectedProfile?.bindings.filter((binding) => binding.kind === 'asset').map((binding) => binding.id) ?? []
  const mapFocusAssetIds = [...new Set([...focusAssetIds, ...profileFocusAssetIds])]
  const canManageAgents = hasTool('agent.manage')

  const closeDialog = () => setDialogMode(null)
  const profileBindings = (assetIds: string[]) => assetIds.map((id) => ({ kind: 'asset' as const, id }))
  const createProfile = async (value: { displayName: string; tools: string[]; assetIds: string[] }) => {
    await client.createAgentProfile({ displayName: value.displayName, tools: value.tools, bindings: profileBindings(value.assetIds) })
    profiles.reload()
  }
  const updateProfile = async (value: { agentId: string; displayName: string; enabled: boolean; tools: string[]; assetIds: string[] }) => {
    await client.updateAgentProfile({
      agentId: value.agentId,
      displayName: value.displayName,
      state: value.enabled ? 'AGENT_PROFILE_STATE_ENABLED' : 'AGENT_PROFILE_STATE_DISABLED',
      tools: value.tools,
      bindings: profileBindings(value.assetIds),
    })
    profiles.reload()
  }
  const batchProfiles = async (value: { agentIds: string[]; tools: string[]; assetIds: string[] }) => {
    await client.batchUpdateAgentProfiles({ agentIds: value.agentIds, tools: value.tools, bindings: profileBindings(value.assetIds) })
    profiles.reload()
  }
  const deleteProfile = async (agentId: string) => {
    await client.deleteAgentProfile(agentId)
    if (selectedAgentId === agentId) setSelectedAgentId('')
    profiles.reload()
  }

  useEffect(() => {
    void ensureSession()
  }, [ensureSession])

  if (estate.status === 'error' && estate.error !== null) {
    if (hasCode(estate.error, 'permission_denied')) return <StateView kind="denied" />
    return <StateView kind="error" message={estate.error.message} onRetry={estate.reload} />
  }
  if (profiles.status === 'error' && profiles.error !== null) {
    if (hasCode(profiles.error, 'permission_denied')) return <StateView kind="denied" />
    return <StateView kind="error" message={profiles.error.message} onRetry={profiles.reload} />
  }

  return (
    <div className={`yf-orchestra is-mobile-${mobilePane}`}>
      <div className="yf-agent-mobile-tabs" role="tablist" aria-label="Agent 管理视图">
        <button id="agent-map-tab" type="button" role="tab" aria-selected={mobilePane === 'map'} aria-controls="agent-map-panel" onClick={() => setMobilePane('map')}>
          <Map size={17} aria-hidden />
          资产拓扑
        </button>
        <button id="agent-chat-tab" type="button" role="tab" aria-selected={mobilePane === 'chat'} aria-controls="agent-chat-panel" onClick={() => setMobilePane('chat')}>
          <MessageSquareText size={17} aria-hidden />
          贾维斯
        </button>
      </div>
      <div id="agent-map-panel" className="yf-orchestra-map" role="tabpanel" aria-labelledby="agent-map-tab">
        {selectedCaseId !== '' ? (
          <div className="h-full overflow-y-auto bg-[#071114] p-4">
            <Button size="sm" variant="light" startContent={<ArrowLeft size={14} />} onPress={() => setSelectedCaseId('')}>返回资产拓扑</Button>
            <div className="mt-3"><CaseWorkspace caseId={selectedCaseId} /></div>
          </div>
        ) : estate.data !== null ? (
          <div className="relative h-full">
          {cases.status === 'error' && cases.error !== null && <div className="absolute left-4 right-4 top-20 z-20"><StateView kind="error" message={`案件数据加载失败：${cases.error.message}`} onRetry={cases.reload} /></div>}
          {workers.status === 'error' && workers.error !== null && <div className="absolute bottom-4 left-4 right-4 z-20"><StateView kind="error" message={`执行池数据加载失败：${workers.error.message}`} onRetry={workers.reload} /></div>}
          <div className="yf-agent-toolbar" aria-label="Agent 管理工具栏">
            <div className="yf-agent-toolbar-summary">
              <p className="text-xs font-semibold text-[#d8e6e3]">Agent 管理</p>
              <p className="text-[10px] text-[#71888c]">{profileItems.length} 个流量审查岗位 · Jarvis 固定</p>
            </div>
            {canManageAgents && (
              <>
                <Button size="sm" color="primary" startContent={<Plus size={14} />} onPress={() => {
                  setSelectedAgentId('')
                  setDialogMode('create')
                }}>新增 Agent</Button>
                <Button size="sm" variant="bordered" startContent={<SlidersHorizontal size={14} />} isDisabled={manageableProfiles.length === 0} onPress={() => setDialogMode('batch')}>批量设置</Button>
              </>
            )}
            {profileItems.map((profile) => (
              <Button
                key={profile.agentId}
                size="sm"
                variant={selectedAgentId === profile.agentId ? 'solid' : 'flat'}
                color={selectedAgentId === profile.agentId ? 'primary' : 'default'}
                aria-pressed={selectedAgentId === profile.agentId}
                aria-label={`${profile.canManage ? '编辑' : '查看'} Agent ${profile.displayName}`}
                onPress={() => {
                  setSelectedAgentId(profile.agentId)
                  setSelectedAssetId('')
                  setContextLabel(`${profile.displayName} · 流量审查`)
                  if (profile.canManage) setDialogMode('edit')
                }}
              >
                <span className="flex min-w-0 flex-col items-start py-1 text-left leading-tight">
                  <span className="max-w-48 truncate text-xs font-semibold">{profile.displayName}</span>
                  <span className="mt-1 text-[10px] opacity-75">短命 run · 案件 {(cases.data?.items ?? []).filter((item) => item.assignedAgentId === profile.agentId).length} · 活动 {profile.activeRunCount ?? 0}</span>
                  <span className="mt-0.5 max-w-52 truncate text-[9px] opacity-60">{profile.lastWorkerPlatform || '等待合格 Worker'} · {profile.lastRunAt ? formatTime(profile.lastRunAt) : '尚未执行'}</span>
                </span>
              </Button>
            ))}
          </div>
          <AssetEstateMap
            assets={estate.data.items}
            plane={{
              jarvisOnline: onboarding?.jarvisOnline,
              managedAgents: profileItems.map((profile) => ({
                agentId: profile.agentId,
                displayName: profile.displayName,
                enabled: profile.state === 'AGENT_PROFILE_STATE_ENABLED',
              })),
              workers: workers.data?.items ?? [],
            }}
            truncated={estate.data.nextPageToken !== ''}
            density="full"
            layout="bare"
            focusAssetIds={mapFocusAssetIds}
            caseStats={caseStats}
            onSelectAsset={(id) => {
              setSelectedAssetId(id)
              setSelectedAgentId('')
              const name = estate.data?.items.find((a) => a.asset.id === id)?.asset.displayName ?? id
              setContextLabel(`${name} · ${id}`)
            }}
            onSelectAgent={(id) => {
              setSelectedAgentId(id)
              setSelectedAssetId('')
              const profile = profileItems.find((item) => item.agentId === id)
              setContextLabel(`${profile?.displayName ?? id} · 流量审查`)
              if (profile?.canManage === true) setDialogMode('edit')
            }}
            onClearSelection={() => {
              setSelectedAssetId('')
              setSelectedAgentId('')
              setContextLabel('中台 / 未选择对象')
            }}
            onOpenAsset={(id) => navigate(`/assets/${id}`)}
            onOpenJarvis={() => {
              setSelectedAssetId('')
              setSelectedAgentId('')
              setContextLabel('贾维斯 / 中台')
            }}
          />
          {selectedAssetId !== '' && selectedAssetCases.length > 0 && (
            <aside className="yf-agent-cases" aria-label="资产案件">
              <p className="text-xs font-semibold text-[#b8c9c7]">未结案件 · {selectedAssetCases.length}</p>
              {selectedAssetCases.map((item) => <CaseCard key={item.caseId} item={item} onSelect={() => setSelectedCaseId(item.caseId)} />)}
            </aside>
          )}
          {dialogMode !== null && (
            <AgentProfileDialog
              key={`${dialogMode}:${selectedProfile?.agentId ?? 'new'}`}
              mode={dialogMode}
              open
              profile={dialogMode === 'edit' ? selectedProfile : undefined}
              profiles={manageableProfiles}
              assets={estate.data.items}
              onClose={closeDialog}
              onCreate={createProfile}
              onUpdate={updateProfile}
              onBatch={batchProfiles}
              onDelete={deleteProfile}
            />
          )}
          </div>
        ) : (
          <StateView kind="loading" />
        )}
      </div>
      <div id="agent-chat-panel" className="yf-agent-chat-panel" role="tabpanel" aria-labelledby="agent-chat-tab">
        <YfChat
          mode="stage"
          messages={messages}
          selfId={user?.userId ?? ''}
          contextLabel={contextLabel}
          thinking={thinking}
          busy={busy}
          error={error}
          online={jarvisOnline}
          onSend={(text) => void send(text)}
          onApprovalDecided={refreshSignals}
        />
      </div>
    </div>
  )
}
