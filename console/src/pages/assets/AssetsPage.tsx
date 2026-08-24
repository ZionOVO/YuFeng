// 资产台账：拓扑图（同筛选、最多 200 台）+ query/criticality 筛选 + 不透明游标分页。
// 增删改按钮同时对齐 Tools × Bindings 与资产服务的管理员角色硬门。

import { useState } from 'react'
import {
  Button,
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
  useDisclosure,
} from '@heroui/react'
import { Plus, Search } from 'lucide-react'
import { Link, useNavigate } from 'react-router-dom'
import { canOnAsset, hasTool } from '../../api/access'
import type { ListAssetsFilter } from '../../api/client'
import { hasCode, isApiError } from '../../api/errors'
import { PageSizeMax } from '../../api/limits'
import type { AccessMode, AssetDetail, Criticality } from '../../api/types'
import { useAuth } from '../../auth/useAuth'
import { useJarvisSession } from '../../components/chat/useJarvisSession'
import { AssetEstateMap } from '../../components/estate/AssetEstateMap'
import { useAsyncData } from '../../components/useAsyncData'
import { Badge, ConfirmDialog, HealthBadge, StateView, TokenPager } from '../../components/ui'
import { ACCESS_MODE_LABEL, CRITICALITY_BADGE, EDITABLE_ACCESS_MODES, EDITABLE_CRITICALITIES } from './assetMeta'

const PAGE_SIZE = 25

export function AssetsPage() {
  const { client, user, access, onboarding, refreshAccess } = useAuth()
  const jarvis = useJarvisSession()
  const navigate = useNavigate()
  const assetAdmin = user?.role === 'USER_ROLE_ADMIN'
  const canCreate = assetAdmin && hasTool(access, 'asset.create')
  const [query, setQuery] = useState('')
  const [criticality, setCriticality] = useState<Criticality | 'all'>('all')
  // tokens[i] 是取第 i+1 页的 pageToken；筛选变化时重置整条游标链
  const [tokens, setTokens] = useState<string[]>([''])
  const [pageIndex, setPageIndex] = useState(0)

  // “全部”不传 criticality 字段；空 query 同样省略
  const filter: ListAssetsFilter = {
    query: query.trim() === '' ? undefined : query.trim(),
    criticality: criticality === 'all' ? undefined : criticality,
  }
  const { data, status, error, reload } = useAsyncData(
    () => client.listAssets(filter, { pageSize: PAGE_SIZE, pageToken: tokens[pageIndex] }),
    // filter 由 query/criticality 派生，每次渲染是新对象，用 JSON 快照作依赖
    [JSON.stringify(filter), pageIndex],
  )
  const mapQuery = useAsyncData(
    () => client.listAssets(filter, { pageSize: PageSizeMax }),
    [JSON.stringify(filter)],
  )

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

  const createModal = useDisclosure()
  const [createName, setCreateName] = useState('')
  const [createMode, setCreateMode] = useState<AccessMode>('ACCESS_MODE_NETWORK')
  const [createCrit, setCreateCrit] = useState<Criticality>('CRITICALITY_P2')
  const [createBusy, setCreateBusy] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<AssetDetail | null>(null)
  const deleteDialog = useDisclosure()
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const localAssetId = onboarding?.localAssetId ?? ''

  const submitCreate = async () => {
    setCreateBusy(true)
    setCreateError(null)
    try {
      await client.createAsset({ displayName: createName.trim(), accessMode: createMode, criticality: createCrit })
      createModal.onClose()
      setCreateName('')
      await refreshAccess()
      reload()
      mapQuery.reload()
    } catch (e) {
      setCreateError(isApiError(e) ? `登记失败：${e.message}（${e.code}）` : '登记失败，请重试')
    } finally {
      setCreateBusy(false)
    }
  }

  const confirmDelete = async () => {
    if (deleteTarget === null) return
    setDeleteBusy(true)
    setDeleteError(null)
    try {
      await client.deleteAsset(deleteTarget.asset.id)
      deleteDialog.onClose()
      setDeleteTarget(null)
      await refreshAccess()
      reload()
      mapQuery.reload()
    } catch (e) {
      setDeleteError(isApiError(e) ? `删除失败：${e.message}（${e.code}）` : '删除失败，请重试')
    } finally {
      setDeleteBusy(false)
    }
  }

  if (status === 'error' && error !== null) {
    if (hasCode(error, 'permission_denied')) return <StateView kind="denied" />
    return <StateView kind="error" message={error.message} onRetry={reload} />
  }
  // data 为 null 只出现在初次加载；翻页/筛选期间保留旧数据避免闪烁
  if (data === null) return <StateView kind="loading" />

  return (
    <>
    {mapQuery.data !== null ? (
      <AssetEstateMap
        assets={mapQuery.data.items}
        plane={{
          jarvisOnline: onboarding?.jarvisOnline,
          edgeReady: onboarding?.edgeReady,
        }}
        truncated={mapQuery.data.nextPageToken !== ''}
        density="full"
        focusAssetIds={jarvis.focusAssetIds}
        onOpenAsset={(id) => navigate(`/assets/${id}`)}
        onOpenJarvis={() => jarvis.setDockOpen(true)}
        onSelectAsset={(id) => jarvis.setContextLabel(id)}
      />
    ) : mapQuery.status === 'error' && mapQuery.error !== null ? (
      <StateView
        kind={hasCode(mapQuery.error, 'permission_denied') ? 'denied' : 'error'}
        message={mapQuery.error.message}
        onRetry={mapQuery.reload}
      />
    ) : null}
    <section className="fs-panel" aria-label="资产台账">
      <div className="fs-panel-head">
        <div>
          <p className="fs-panel-title">资产台账</p>
          <p className="fs-panel-sub" style={{ marginTop: 4 }}>
            ASSETS
          </p>
        </div>
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <Input
            aria-label="搜索资产"
            placeholder="按标识 / 名称搜索"
            size="sm"
            radius="md"
            value={query}
            onValueChange={(v) => {
              setQuery(v)
              onFilterChange()
            }}
            startContent={<Search size={14} aria-hidden />}
            className="sm:w-56"
            isClearable
          />
          <Select
            aria-label="按关键性筛选"
            size="sm"
            radius="md"
            selectedKeys={[criticality]}
            onChange={(e) => {
              setCriticality(e.target.value as Criticality | 'all')
              onFilterChange()
            }}
            className="sm:w-32"
          >
            <SelectItem key="all">全部</SelectItem>
            <SelectItem key="CRITICALITY_P0">P0</SelectItem>
            <SelectItem key="CRITICALITY_P1">P1</SelectItem>
            <SelectItem key="CRITICALITY_P2">P2</SelectItem>
          </Select>
          {canCreate && (
            <Button size="sm" radius="md" color="primary" startContent={<Plus size={14} />} onPress={createModal.onOpen}>
              登记资产
            </Button>
          )}
        </div>
      </div>
      <Table aria-label="资产列表" removeWrapper className="fs-table-tight">
        <TableHeader>
          <TableColumn>标识</TableColumn>
          <TableColumn>显示名</TableColumn>
          <TableColumn>接入模式</TableColumn>
          <TableColumn>关键性</TableColumn>
          <TableColumn>健康</TableColumn>
          <TableColumn>绑定单元</TableColumn>
          <TableColumn>在役策略</TableColumn>
          <TableColumn>操作</TableColumn>
        </TableHeader>
        <TableBody emptyContent="没有符合条件的资产">
          {data.items.map((item) => {
            const to = `/assets/${item.asset.id}`
            const crit = CRITICALITY_BADGE[item.asset.criticality] ?? CRITICALITY_BADGE.CRITICALITY_UNSPECIFIED
            return (
              <TableRow
                key={item.asset.id}
                className="cursor-pointer transition-colors hover:bg-[#161c21]"
                tabIndex={0}
                onClick={() => navigate(to)}
                onKeyDown={(e) => {
                  // 焦点在行自身时才响应 Enter；落在“详情”链接上交给链接原生行为
                  if (e.key === 'Enter' && e.target === e.currentTarget) navigate(to)
                }}
              >
                <TableCell className="fs-mono">{item.asset.id}</TableCell>
                <TableCell>{item.asset.displayName}</TableCell>
                <TableCell>{ACCESS_MODE_LABEL[item.asset.accessMode] ?? ACCESS_MODE_LABEL.ACCESS_MODE_UNSPECIFIED}</TableCell>
                <TableCell>
                  <Badge label={crit.label} tone={crit.tone} />
                </TableCell>
                <TableCell>
                  <HealthBadge health={item.health} />
                </TableCell>
                <TableCell className="fs-mono">{(item.unitIds ?? []).length}</TableCell>
                <TableCell className="fs-mono">{item.activeReleaseCount}</TableCell>
                <TableCell>
                  <div className="flex items-center gap-2" onClick={(e) => e.stopPropagation()}>
                    <Link to={to} className="text-xs text-[#62e6a7] hover:underline">
                      详情
                    </Link>
                    {assetAdmin && canOnAsset(access, 'asset.delete', item.asset.id) && item.asset.id !== localAssetId && (
                      <Button
                        size="sm"
                        radius="md"
                        variant="light"
                        color="danger"
                        onPress={() => {
                          setDeleteTarget(item)
                          setDeleteError(null)
                          deleteDialog.onOpen()
                        }}
                      >
                        删除
                      </Button>
                    )}
                  </div>
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
      <TokenPager page={pageIndex + 1} hasPrev={pageIndex > 0} hasNext={data.nextPageToken !== ''} onPrev={goPrev} onNext={goNext} />
    </section>

    <Modal isOpen={createModal.isOpen} onClose={createModal.onClose} placement="center" radius="lg">
      <ModalContent>
        <ModalHeader>登记防御资产</ModalHeader>
        <ModalBody className="gap-3">
          <Input label="显示名" radius="md" value={createName} onValueChange={setCreateName} isRequired />
          <Select label="接入模式" radius="md" selectedKeys={[createMode]} onChange={(e) => setCreateMode(e.target.value as AccessMode)}>
            {EDITABLE_ACCESS_MODES.map((m) => (
              <SelectItem key={m}>{ACCESS_MODE_LABEL[m]}</SelectItem>
            ))}
          </Select>
          <Select label="关键性" radius="md" selectedKeys={[createCrit]} onChange={(e) => setCreateCrit(e.target.value as Criticality)}>
            {EDITABLE_CRITICALITIES.map((c) => (
              <SelectItem key={c}>{CRITICALITY_BADGE[c].label}</SelectItem>
            ))}
          </Select>
          {createError !== null && <p className="text-xs text-[#ff746c]">{createError}</p>}
        </ModalBody>
        <ModalFooter>
          <Button variant="light" radius="md" onPress={createModal.onClose} isDisabled={createBusy}>
            取消
          </Button>
          <Button color="primary" radius="md" isLoading={createBusy} isDisabled={createName.trim() === ''} onPress={() => void submitCreate()}>
            登记
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>

    <ConfirmDialog
      open={deleteDialog.isOpen}
      title="删除资产"
      confirmLabel="确认删除"
      danger
      busy={deleteBusy}
      onConfirm={() => void confirmDelete()}
      onClose={deleteDialog.onClose}
    >
      <p className="text-sm text-foreground-500">
        将删除资产 <span className="fs-mono">{deleteTarget?.asset.displayName}</span>（{deleteTarget?.asset.id}）。本机数据面资产不能删。
      </p>
      {deleteError !== null && <p className="text-xs text-[#ff746c]">{deleteError}</p>}
    </ConfirmDialog>
    </>
  )
}
