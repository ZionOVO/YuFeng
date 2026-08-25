// 资产详情：元数据、能力矩阵、绑定单元管理（绑定/解绑）与属性编辑。
// 增删改仅 USER_ROLE_ADMIN 可见；鉴权以服务端为准（docs/api.md §0.5、§9）。

import { useState } from 'react'
import { Button, Input, Modal, ModalBody, ModalContent, ModalFooter, ModalHeader, Select, SelectItem, Textarea, useDisclosure } from '@heroui/react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { canOnAsset } from '../../api/access'
import { hasCode, isApiError } from '../../api/errors'
import type { AssetPatch, Criticality, Tier } from '../../api/types'
import { useAuth } from '../../auth/useAuth'
import { useJarvisSession } from '../../components/chat/useJarvisSession'
import { AssetEstateMap } from '../../components/estate/AssetEstateMap'
import { useAsyncData } from '../../components/useAsyncData'
import { formatTime } from '../../components/format'
import { Badge, ConfirmDialog, StateView } from '../../components/ui'
import { CaseCard } from '../../components/cases/CaseCards'
import { ACCESS_MODE_LABEL, CRITICALITY_BADGE, EDITABLE_CRITICALITIES, EDITABLE_TIERS, TIER_LABEL } from './assetMeta'
import { TrafficReviewPolicyCard } from './TrafficReviewPolicyCard'
import { ModelIngressWindowCard } from './ModelIngressWindowCard'
import { EdgeEnrollmentCard } from './EdgeEnrollmentCard'

/** 布尔能力 → 徽章：支持 green / 不支持 mute。 */
function BoolBadge({ on }: { on: boolean }) {
  return <Badge label={on ? '支持' : '不支持'} tone={on ? 'green' : 'mute'} />
}

/** 标签文本（每行 k=v，空行忽略）→ Record；非法行收集后由表单提示并禁止提交。 */
function parseLabels(text: string): { labels: Record<string, string>; invalid: string[] } {
  const labels: Record<string, string> = {}
  const invalid: string[] = []
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (line === '') continue
    const eq = line.indexOf('=')
    // eq<=0：没有等号或键为空，均视为非法
    if (eq <= 0) {
      invalid.push(line)
      continue
    }
    labels[line.slice(0, eq).trim()] = line.slice(eq + 1).trim()
  }
  return { labels, invalid }
}

function labelsToText(labels: Record<string, string>): string {
  return Object.entries(labels)
    .map(([k, v]) => `${k}=${v}`)
    .join('\n')
}

function sameLabels(a: Record<string, string>, b: Record<string, string>): boolean {
  const ka = Object.keys(a)
  const kb = Object.keys(b)
  return ka.length === kb.length && ka.every((k) => b[k] === a[k])
}

export function AssetDetailPage() {
  const { client, user, onboarding, access, hasTool } = useAuth()
  const jarvis = useJarvisSession()
  const navigate = useNavigate()
  const { assetId = '' } = useParams<{ assetId: string }>()
  const assetAdmin = user?.role === 'USER_ROLE_ADMIN'
  const canWrite = assetAdmin && canOnAsset(access, 'asset.update', assetId)
  const canAttach = assetAdmin && canOnAsset(access, 'asset.attach', assetId)
  const canDetach = assetAdmin && canOnAsset(access, 'asset.detach', assetId)
  const canDelete = assetAdmin && canOnAsset(access, 'asset.delete', assetId)
  const { data, status, error, reload } = useAsyncData(() => client.getAsset(assetId), [assetId], false)
  const cases = useAsyncData(
    () => hasTool('case.read') ? client.listCases({ assetId }, { pageSize: 20 }) : Promise.resolve({ items: [], nextPageToken: '' }),
    [client, assetId, hasTool],
    false,
  )

  // 解绑确认弹窗
  const detachDialog = useDisclosure()
  const [detachTarget, setDetachTarget] = useState<string | null>(null)
  const [detachBusy, setDetachBusy] = useState(false)
  const [detachError, setDetachError] = useState<string | null>(null)

  // 绑定表单（单元面板底部）
  const [attachId, setAttachId] = useState('')
  const [attachBusy, setAttachBusy] = useState(false)
  const [unitError, setUnitError] = useState<string | null>(null)

  // 编辑弹窗
  const editModal = useDisclosure()
  const [editName, setEditName] = useState('')
  const [editCriticality, setEditCriticality] = useState<Criticality>('CRITICALITY_UNSPECIFIED')
  const [editTier, setEditTier] = useState<Tier>('TIER_UNSPECIFIED')
  const [editLabels, setEditLabels] = useState('')
  const [editBusy, setEditBusy] = useState(false)
  const [editError, setEditError] = useState<string | null>(null)
  const deleteAssetDialog = useDisclosure()
  const [deleteAssetBusy, setDeleteAssetBusy] = useState(false)
  const [deleteAssetError, setDeleteAssetError] = useState<string | null>(null)

  const openDetach = (unitId: string) => {
    setDetachTarget(unitId)
    setDetachError(null)
    detachDialog.onOpen()
  }

  const confirmDeleteAsset = async () => {
    setDeleteAssetBusy(true)
    setDeleteAssetError(null)
    try {
      await client.deleteAsset(assetId)
      deleteAssetDialog.onClose()
      navigate('/assets')
    } catch (e) {
      setDeleteAssetError(isApiError(e) ? `删除失败：${e.message}（${e.code}）` : '删除失败，请重试')
    } finally {
      setDeleteAssetBusy(false)
    }
  }

  const confirmDetach = async () => {
    if (detachTarget === null) return
    setDetachBusy(true)
    setDetachError(null)
    try {
      await client.detachUnit(assetId, detachTarget)
      detachDialog.onClose()
      reload()
    } catch (e) {
      setDetachError(isApiError(e) ? `解绑失败：${e.message}（${e.code}）` : '解绑失败，请重试')
    } finally {
      setDetachBusy(false)
    }
  }

  const submitAttach = async () => {
    const unitId = attachId.trim()
    if (unitId === '') return
    setAttachBusy(true)
    setUnitError(null)
    try {
      await client.attachUnit(assetId, unitId)
      setAttachId('')
      reload()
    } catch (e) {
      setUnitError(isApiError(e) ? `绑定失败：${e.message}（${e.code}）` : '绑定失败，请重试')
    } finally {
      setAttachBusy(false)
    }
  }

  if (status === 'error' && error !== null) {
    if (hasCode(error, 'permission_denied')) return <StateView kind="denied" />
    if (hasCode(error, 'not_found')) return <StateView kind="error" message="资产不存在或已删除" onRetry={reload} />
    return <StateView kind="error" message={error.message} onRetry={reload} />
  }
  if (data === null) return <StateView kind="loading" />

  const { asset, unitIds } = data
  const caps = asset.capabilities
  const labelEntries = Object.entries(asset.labels ?? {})
  const crit = CRITICALITY_BADGE[asset.criticality] ?? CRITICALITY_BADGE.CRITICALITY_UNSPECIFIED
  const attached = unitIds ?? []
  const unitsById = new Map((data.units ?? []).map((unit) => [unit.unitId, unit]))
  const parsedLabels = parseLabels(editLabels)

  const openEditor = () => {
    setEditName(asset.displayName)
    setEditCriticality(asset.criticality)
    setEditTier(asset.maxAutoTier)
    setEditLabels(labelsToText(asset.labels ?? {}))
    setEditError(null)
    editModal.onOpen()
  }

  // 只回传变化字段（docs/api.md §9：UpdateAsset 为部分更新）；无变化直接关窗
  const submitEdit = async () => {
    const name = editName.trim()
    const patch: AssetPatch = {}
    if (name !== asset.displayName) patch.displayName = name
    if (editCriticality !== asset.criticality) patch.criticality = editCriticality
    if (editTier !== asset.maxAutoTier) patch.maxAutoTier = editTier
    if (!sameLabels(parsedLabels.labels, asset.labels ?? {})) patch.labels = parsedLabels.labels
    if (Object.keys(patch).length === 0) {
      editModal.onClose()
      return
    }
    setEditBusy(true)
    setEditError(null)
    try {
      // 使用当前投影版本阻止多个管理员无提示覆盖彼此的资产属性。
      await client.updateAsset(assetId, patch, asset.updatedAt)
      editModal.onClose()
      reload()
    } catch (e) {
      setEditError(isApiError(e) ? `保存失败：${e.message}（${e.code}）` : '保存失败，请重试')
    } finally {
      setEditBusy(false)
    }
  }

  return (
    <>
      {/* 页头：资产名 + 关联发布 / 编辑入口 */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <p className="truncate text-sm font-medium">{asset.displayName}</p>
          <p className="fs-mono text-xs text-[#8b98a1]">{asset.id}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button as={Link} to={`/releases?assetId=${asset.id}`} size="sm" radius="md" variant="bordered">
            查看关联防护策略
          </Button>
          <Button as={Link} to={`/events?assetId=${asset.id}`} size="sm" radius="md" variant="bordered">
            查看检测事件
          </Button>
          {canWrite && (
            <Button size="sm" radius="md" color="primary" onPress={openEditor}>
              编辑
            </Button>
          )}
          {canDelete && data.edgeEnrollments.length === 0 && (
            <Button size="sm" radius="md" color="danger" variant="bordered" onPress={deleteAssetDialog.onOpen}>
              删除
            </Button>
          )}
        </div>
      </div>

      {jarvis.focusAssetIds.includes(assetId) && (
        <p className="yf-focus-banner" role="status">
          贾维斯这一轮的工具链绑着这台资产。
        </p>
      )}
      <AssetEstateMap
        assets={[data]}
        plane={{
          jarvisOnline: onboarding?.jarvisOnline,
        }}
        density="compact"
        focusAssetIds={jarvis.focusAssetIds.includes(assetId) ? [assetId] : jarvis.focusAssetIds}
        onOpenJarvis={() => jarvis.setDockOpen(true)}
        onSelectAsset={() => jarvis.setContextLabel(`${asset.displayName} · ${asset.id}`)}
      />

      <EdgeEnrollmentCard
        assetId={assetId}
        enrollments={data.edgeEnrollments}
        canWrite={canWrite}
        client={client}
        onRefresh={reload}
      />

      {cases.data !== null && cases.data.items.length > 0 && (
        <section className="fs-panel" aria-label="资产案件摘要">
          <div className="fs-panel-head"><div><p className="fs-panel-title">未结案件</p><p className="fs-panel-sub">AGENT REVIEW</p></div><Button as={Link} to={`/cases?assetId=${assetId}`} size="sm" variant="light">打开案件工作台</Button></div>
          <div className="grid gap-3 p-4 lg:grid-cols-2">{cases.data.items.filter((item) => item.state !== 'INVESTIGATION_CASE_STATE_RESOLVED').slice(0, 4).map((item) => <CaseCard key={item.caseId} item={item} onSelect={() => navigate(`/cases?caseId=${item.caseId}`)} />)}</div>
        </section>
      )}

      <TrafficReviewPolicyCard
        assetId={assetId}
        units={data.units ?? []}
        canWrite={canWrite}
        client={client}
        onRefreshAsset={reload}
      />

      <ModelIngressWindowCard
        assetId={assetId}
        units={data.units ?? []}
        canWrite={canWrite}
        client={client}
        onRefreshAsset={reload}
      />

      {/* 元数据 */}
      <section className="fs-panel" aria-label="资产元数据">
        <div className="fs-panel-head">
          <p className="fs-panel-title">元数据</p>
          <p className="fs-panel-sub">ASSET</p>
        </div>
        <dl className="yf-kv px-4 py-3">
          <dt>标识</dt>
          <dd className="fs-mono">{asset.id}</dd>
          <dt>显示名</dt>
          <dd>{asset.displayName}</dd>
          <dt>接入模式</dt>
          <dd>{ACCESS_MODE_LABEL[asset.accessMode] ?? ACCESS_MODE_LABEL.ACCESS_MODE_UNSPECIFIED}</dd>
          <dt>关键性</dt>
          <dd>
            <Badge label={crit.label} tone={crit.tone} />
          </dd>
          <dt>自动执行上限</dt>
          <dd>{TIER_LABEL[asset.maxAutoTier] ?? TIER_LABEL.TIER_UNSPECIFIED}</dd>
          <dt>标签</dt>
          <dd className="flex flex-wrap gap-1.5">
            {labelEntries.length === 0 ? '—' : labelEntries.map(([k, v]) => <Badge key={k} label={`${k}=${v}`} tone="mute" />)}
          </dd>
          <dt>最近探针</dt>
          <dd className="fs-mono">{formatTime(asset.lastProbeAt)}</dd>
        </dl>
      </section>

      <div className="fs-grid2">
        {/* 能力矩阵：旁路资产没有探针数据 */}
        {caps === undefined ? (
          <StateView kind="empty" title="能力矩阵" message="旁路资产无能力探针数据" />
        ) : (
          <section className="fs-panel" aria-label="能力矩阵">
            <div className="fs-panel-head">
              <p className="fs-panel-title">能力矩阵</p>
              <p className="fs-panel-sub">CAPABILITIES</p>
            </div>
            <dl className="yf-kv px-4 py-3">
              <dt>内核版本</dt>
              <dd className="fs-mono">{caps.kernelVersion}</dd>
              <dt>BPF LSM</dt>
              <dd>
                <BoolBadge on={caps.bpfLsm} />
              </dd>
              <dt>seccomp</dt>
              <dd>
                <BoolBadge on={caps.seccomp} />
              </dd>
              <dt>nftables</dt>
              <dd>
                <BoolBadge on={caps.nftables} />
              </dd>
              <dt>Landlock</dt>
              <dd>
                <BoolBadge on={caps.landlock} />
              </dd>
              <dt>包管理器</dt>
              <dd className="flex flex-wrap gap-1.5">
                {(caps.packageManagers ?? []).length === 0
                  ? '—'
                  : (caps.packageManagers ?? []).map((pm) => <Badge key={pm} label={pm} tone="mute" />)}
              </dd>
            </dl>
          </section>
        )}

        {/* 绑定单元 */}
        <section className="fs-panel" aria-label="绑定单元">
          <div className="fs-panel-head">
            <p className="fs-panel-title">绑定单元</p>
            <p className="fs-panel-sub">{attached.length} 个</p>
          </div>
          {attached.length === 0 ? (
            <p className="px-4 py-6 text-center text-xs text-[#8b98a1]">暂无绑定单元</p>
          ) : (
            <div>
              {attached.map((unitId) => {
                const unit = unitsById.get(unitId)
                const health = unit?.producerHealth
                return (
                  <div key={unitId} className="fs-row items-start">
                    <div className="min-w-0 flex-1 space-y-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="fs-mono">{unitId}</span>
                        {unit !== undefined && <Badge label={unit.health || 'UNIT_HEALTH_UNSPECIFIED'} tone={unit.health.includes('HEALTHY') ? 'green' : 'amber'} />}
                      </div>
                      {unit !== undefined && (
                        <>
                          <p className="text-xs text-[#8b98a1]">
                            {unit.posture} · {unit.trafficKey || '无流量键'} · {unit.version || '未知版本'}
                          </p>
                          <p className="text-xs text-[#8b98a1]">
                            输出 {unit.capabilities.outputs.join(' / ') || '未广告'}；传感 {unit.capabilities.sensors.join(' / ') || '未广告'}
                          </p>
                          <p className="text-xs text-[#8b98a1]">
                            缓冲 关键 {health?.bufferedCriticalEvents ?? '0'} / 普通 {health?.bufferedOrdinarySamples ?? '0'}；丢弃 关键 {health?.droppedCriticalEvents ?? '0'} / 普通 {health?.droppedOrdinarySamples ?? '0'} / 旁路 {health?.droppedLocalBypassItems ?? '0'}
                          </p>
                        </>
                      )}
                    </div>
                    {canDetach && !data.edgeEnrollments.some((enrollment) => enrollment.unitId === unitId) && (
                      <Button size="sm" radius="md" variant="light" color="danger" onPress={() => openDetach(unitId)}>
                        解绑
                      </Button>
                    )}
                  </div>
                )
              })}
            </div>
          )}
          {unitError !== null && <p className="px-4 py-2 text-xs text-[#ff746c]">{unitError}</p>}
          {canAttach && (
            <div className="flex items-center gap-2 border-t border-[#1d252a] px-4 py-3">
              <Input
                aria-label="单元标识"
                placeholder="单元标识，如 unit-edge-03"
                size="sm"
                radius="md"
                value={attachId}
                onValueChange={setAttachId}
                className="max-w-64"
              />
              <Button
                size="sm"
                radius="md"
                color="primary"
                isLoading={attachBusy}
                isDisabled={attachId.trim() === ''}
                onPress={() => void submitAttach()}
              >
                绑定
              </Button>
            </div>
          )}
        </section>
      </div>

      {/* 解绑确认（危险操作） */}
      <ConfirmDialog
        open={detachDialog.isOpen}
        title="解绑单元"
        confirmLabel="确认解绑"
        danger
        busy={detachBusy}
        onConfirm={() => void confirmDetach()}
        onClose={detachDialog.onClose}
      >
        <p className="text-sm text-foreground-500">
          将把单元 <span className="fs-mono">{detachTarget}</span> 从资产 <span className="fs-mono">{asset.id}</span> 解绑；
          解绑后边缘下次快照将卸载经该单元下发的制品。
        </p>
        {detachError !== null && <p className="text-xs text-[#ff746c]">{detachError}</p>}
      </ConfirmDialog>

      <ConfirmDialog
        open={deleteAssetDialog.isOpen}
        title="删除资产"
        confirmLabel="确认删除"
        danger
        busy={deleteAssetBusy}
        onConfirm={() => void confirmDeleteAsset()}
        onClose={deleteAssetDialog.onClose}
      >
        <p className="text-sm text-foreground-500">
          将删除资产 <span className="fs-mono">{asset.displayName}</span>（{asset.id}）。存在人工 Edge 接入配置时必须先完成技术退役。
        </p>
        {deleteAssetError !== null && <p className="text-xs text-[#ff746c]">{deleteAssetError}</p>}
      </ConfirmDialog>

      {/* 编辑弹窗 */}
      <Modal isOpen={editModal.isOpen} onClose={editModal.onClose} placement="center" radius="lg">
        <ModalContent>
          <ModalHeader>编辑资产</ModalHeader>
          <ModalBody className="gap-3">
            <Input label="显示名" radius="md" value={editName} onValueChange={setEditName} isRequired />
            <Select
              label="关键性"
              radius="md"
              selectedKeys={[editCriticality]}
              onChange={(e) => setEditCriticality(e.target.value as Criticality)}
            >
              {EDITABLE_CRITICALITIES.map((c) => (
                <SelectItem key={c}>{CRITICALITY_BADGE[c].label}</SelectItem>
              ))}
            </Select>
            <Select label="自动执行上限" radius="md" selectedKeys={[editTier]} onChange={(e) => setEditTier(e.target.value as Tier)}>
              {EDITABLE_TIERS.map((t) => (
                <SelectItem key={t}>{TIER_LABEL[t]}</SelectItem>
              ))}
            </Select>
            <Textarea
              label="标签"
              placeholder={'env=prod\nbiz=payments'}
              description="每行一条 key=value，空行忽略"
              radius="md"
              minRows={3}
              value={editLabels}
              onValueChange={setEditLabels}
              isInvalid={parsedLabels.invalid.length > 0}
              errorMessage={
                parsedLabels.invalid.length > 0 ? `存在非法行（应为 key=value）：${parsedLabels.invalid.join('、')}` : undefined
              }
            />
            {editError !== null && <p className="text-xs text-[#ff746c]">{editError}</p>}
          </ModalBody>
          <ModalFooter>
            <Button variant="light" radius="md" onPress={editModal.onClose} isDisabled={editBusy}>
              取消
            </Button>
            <Button
              color="primary"
              radius="md"
              isLoading={editBusy}
              isDisabled={editName.trim() === '' || parsedLabels.invalid.length > 0}
              onPress={() => void submitEdit()}
            >
              保存
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>
    </>
  )
}
