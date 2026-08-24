// 资产拓扑：资产与单元来自 ListAssets；中台/贾维斯/监督/执行的位置是架构岗位。
// 在线态只认 GetOnboarding.jarvis_online 与 edge_ready。
// 贾维斯与其它 Agent 只连中台，不连资产；单元→中台是注册契约，不是发明的路由。
//
// [贾维斯]: ../../../../docs/glossary.md#jarvis
// [两类边缘]: ../../../../docs/glossary.md#edge-units

import type { AccessMode, AssetDetail, Criticality, WorkerRecord } from '../../api/types'

export type WeakGroupSource = 'biz' | 'env' | 'none'

export type PlaneKind = 'brain' | 'jarvis' | 'managed-agent' | 'agentd'

/** 控制台图例外形，不是账本字段。 */
export type EstateFigure = 'web' | 'router' | 'pc' | 'server' | 'agent'

export type EstateEdgeKind = 'bind' | 'control' | 'register' | 'local'

export interface EstateGroup {
  key: string
  label: string
  assetIds: string[]
}

export interface EstateAssetNode {
  id: string
  name: string
  accessMode: AccessMode
  criticality: Criticality
  health: string
  unitIds: string[]
  activeReleaseCount: number
  labels: Record<string, string>
  groupKey: string
  figure: EstateFigure
  gx: number
  gy: number
  sx: number
  sy: number
  h: number
}

export interface EstateUnitNode {
  id: string
  assetIds: string[]
  gx: number
  gy: number
  sx: number
  sy: number
}

export interface EstatePlaneNode {
  id: string
  kind: PlaneKind
  name: string
  /** true/false 来自引导探测；null 表示控制台没有该进程清单，只标岗位。 */
  live: boolean | null
  gx: number
  gy: number
  sx: number
  sy: number
  h: number
  /** 只有 managed-agent 岗位携带；其余架构岗位为空。 */
  profileId?: string
}

export interface EstateEdge {
  fromId: string
  toId: string
  kind: EstateEdgeKind
  /** 绑定边保留 unitId/assetId，便于旧断言。 */
  unitId?: string
  assetId?: string
}

/** 来自 GetOnboarding 的中台侧实况；缺省按未探测处理。 */
export interface EstatePlaneInput {
  jarvisOnline?: boolean
  edgeReady?: boolean
  managedAgents?: Array<{ agentId: string; displayName: string; enabled: boolean }>
  workers?: WorkerRecord[]
}

export interface EstateGraph {
  source: WeakGroupSource
  groups: EstateGroup[]
  assets: EstateAssetNode[]
  units: EstateUnitNode[]
  plane: EstatePlaneNode[]
  edges: EstateEdge[]
}

export function healthTone(health: string): 'healthy' | 'degraded' | 'unknown' {
  if (health === 'UNIT_HEALTH_HEALTHY' || health === 'healthy') return 'healthy'
  if (health === 'UNIT_HEALTH_DEGRADED' || health === 'degraded') return 'degraded'
  return 'unknown'
}

/** 有任意资产填了 biz 就按业务线；否则有 env 就按环境；都没有则单组，不假装有岛。 */
export function pickWeakGroupSource(assets: AssetDetail[]): WeakGroupSource {
  if (assets.some((a) => (a.asset.labels?.biz ?? '').trim() !== '')) return 'biz'
  if (assets.some((a) => (a.asset.labels?.env ?? '').trim() !== '')) return 'env'
  return 'none'
}

export function groupKeyOf(asset: AssetDetail, source: WeakGroupSource): string {
  if (source === 'none') return ''
  return (asset.asset.labels?.[source] ?? '').trim()
}

export function groupLabel(key: string, source: WeakGroupSource): string {
  if (source === 'none') return '全部'
  if (key === '') return '未标注'
  return key
}

export function isPlaneId(id: string): boolean {
  return id.startsWith('plane:') || id.startsWith('profile:') || id.startsWith('worker:')
}

const FIGURES: Record<EstateFigure, true> = {
  web: true,
  router: true,
  pc: true,
  server: true,
  agent: true,
}

/** 外形是控制台图例。优先 labels.figure；否则看名称里的设备词；再否则画服务器。 */
export function pickAssetFigure(detail: AssetDetail): EstateFigure {
  const raw = (detail.asset.labels?.figure ?? '').trim().toLowerCase()
  if (raw in FIGURES) return raw as EstateFigure
  const text = `${detail.asset.displayName} ${detail.asset.id}`.toLowerCase()
  if (/(router|switch|\bap\b|wifi)/.test(text)) return 'router'
  if (/(gateway|web|http|\bapi\b|site)/.test(text)) return 'web'
  if (/(workstation|desktop|laptop|notebook|\bpc\b)/.test(text)) return 'pc'
  if (/(assistant|jarvis|\bagent\b)/.test(text)) return 'agent'
  return 'server'
}

export function figureFootprint(figure: EstateFigure): { sx: number; sy: number; h: number } {
  if (figure === 'web') return { sx: 1.28, sy: 0.96, h: 36 }
  if (figure === 'router') return { sx: 1.4, sy: 0.7, h: 18 }
  if (figure === 'pc') return { sx: 1.26, sy: 0.78, h: 36 }
  if (figure === 'agent') return { sx: 0.98, sy: 0.76, h: 35 }
  return { sx: 0.78, sy: 0.7, h: 38 }
}

/** 两台资产、单元或岗位的轴对齐包围盒至少隔这么远，禁止贴边。 */
export const MIN_NODE_GAP = 0.7

export const UNIT_FOOTPRINT = { sx: 0.72, sy: 0.5 }

export type EstateBox = { id: string; gx: number; gy: number; sx: number; sy: number }

export function boxesSeparated(a: EstateBox, b: EstateBox, gap = MIN_NODE_GAP): boolean {
  const eps = 1e-9
  const sepX = a.gx + a.sx + gap <= b.gx + eps || b.gx + b.sx + gap <= a.gx + eps
  const sepY = a.gy + a.sy + gap <= b.gy + eps || b.gy + b.sy + gap <= a.gy + eps
  return sepX || sepY
}

export function estateBoxes(g: EstateGraph): EstateBox[] {
  return [
    ...g.assets.map((n) => ({ id: n.id, gx: n.gx, gy: n.gy, sx: n.sx, sy: n.sy })),
    ...g.units.map((n) => ({ id: n.id, gx: n.gx, gy: n.gy, sx: n.sx, sy: n.sy })),
    ...g.plane.map((n) => ({ id: n.id, gx: n.gx, gy: n.gy, sx: n.sx, sy: n.sy })),
  ]
}

/** 沿 +gx / −gy 挪开，直到与已占位盒子满足最小间距。同一输入结果稳定。 */
export function nudgeClear(start: EstateBox, occupied: EstateBox[], gap = MIN_NODE_GAP): EstateBox {
  const next = { ...start }
  for (let step = 0; step < 64; step++) {
    if (occupied.every((o) => o.id === next.id || boxesSeparated(next, o, gap))) return next
    next.gx += 0.2
    if ((step + 1) % 10 === 0) {
      next.gx = start.gx
      next.gy -= 0.2
    }
  }
  return next
}

const CELL_W = 1.4 + MIN_NODE_GAP + 0.12
const CELL_H = 0.96 + MIN_NODE_GAP + 0.12
const GROUP_GAP = 1.05
const ROW_WRAP = 8
const PLANE_GAP = 4.1

/** 由资产详情与引导实况生成等距摆位。同一输入坐标稳定。 */
export function buildEstate(assets: AssetDetail[], plane: EstatePlaneInput = {}): EstateGraph {
  const source = pickWeakGroupSource(assets)
  const bucket = new Map<string, string[]>()
  const sorted = assets.slice().sort((a, b) => a.asset.id.localeCompare(b.asset.id))
  for (const a of sorted) {
    const key = groupKeyOf(a, source)
    const ids = bucket.get(key)
    if (ids) ids.push(a.asset.id)
    else bucket.set(key, [a.asset.id])
  }
  const groups: EstateGroup[] = [...bucket.entries()]
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([key, assetIds]) => ({ key, label: groupLabel(key, source), assetIds }))

  const byId = new Map(sorted.map((a) => [a.asset.id, a]))
  const assetNodes: EstateAssetNode[] = []

  let cursorX = 0
  let cursorY = 0
  let rowH = 0
  for (const g of groups) {
    const cols = Math.min(3, Math.max(1, g.assetIds.length))
    const rows = Math.ceil(g.assetIds.length / cols)
    const gw = cols * CELL_W + 0.95
    const gh = rows * CELL_H + 2.1
    if (cursorX > 0 && cursorX + gw > ROW_WRAP) {
      cursorX = 0
      cursorY += rowH + GROUP_GAP
      rowH = 0
    }
    g.assetIds.forEach((id, i) => {
      const detail = byId.get(id)
      if (!detail) return
      const figure = pickAssetFigure(detail)
      const fp = figureFootprint(figure)
      const col = i % cols
      const row = Math.floor(i / cols)
      assetNodes.push({
        id: detail.asset.id,
        name: detail.asset.displayName || detail.asset.id,
        accessMode: detail.asset.accessMode,
        criticality: detail.asset.criticality,
        health: detail.health,
        unitIds: detail.unitIds ?? [],
        activeReleaseCount: detail.activeReleaseCount,
        labels: detail.asset.labels ?? {},
        groupKey: g.key,
        figure,
        gx: cursorX + 0.55 + col * CELL_W,
        gy: cursorY + 0.45 + row * CELL_H,
        sx: fp.sx,
        sy: fp.sy,
        h: fp.h,
      })
    })
    cursorX += gw + GROUP_GAP
    rowH = Math.max(rowH, gh)
  }

  const spaced: EstateAssetNode[] = []
  const seen: EstateBox[] = []
  for (const n of assetNodes) {
    const placed = nudgeClear({ id: n.id, gx: n.gx, gy: n.gy, sx: n.sx, sy: n.sy }, seen)
    seen.push(placed)
    spaced.push({ ...n, gx: placed.gx, gy: placed.gy })
  }
  assetNodes.length = 0
  assetNodes.push(...spaced)

  const bindEdges: EstateEdge[] = []
  const unitMap = new Map<string, string[]>()
  for (const a of assetNodes) {
    for (const uid of a.unitIds) {
      if (uid === '') continue
      bindEdges.push({ fromId: uid, toId: a.id, kind: 'bind', unitId: uid, assetId: a.id })
      const bound = unitMap.get(uid)
      if (bound) bound.push(a.id)
      else unitMap.set(uid, [a.id])
    }
  }

  // 单元放在所绑资产后方，夹在中台与资产之间，对应「单元注册中台、再绑资产」。
  const occupied: EstateBox[] = assetNodes.map((n) => ({ id: n.id, gx: n.gx, gy: n.gy, sx: n.sx, sy: n.sy }))
  const units: EstateUnitNode[] = []
  for (const [id, assetIds] of [...unitMap.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
    const bound = assetNodes.filter((n) => assetIds.includes(n.id))
    const cx = bound.reduce((s, n) => s + n.gx + n.sx / 2, 0) / Math.max(1, bound.length)
    const behind = bound.reduce((s, n) => Math.min(s, n.gy), 8) - UNIT_FOOTPRINT.sy - MIN_NODE_GAP
    const placed = nudgeClear(
      { id, gx: cx - UNIT_FOOTPRINT.sx / 2, gy: behind, sx: UNIT_FOOTPRINT.sx, sy: UNIT_FOOTPRINT.sy },
      occupied,
    )
    occupied.push(placed)
    units.push({ id, assetIds, gx: placed.gx, gy: placed.gy, sx: placed.sx, sy: placed.sy })
  }

  const xs = [...assetNodes.map((n) => n.gx), ...assetNodes.map((n) => n.gx + n.sx), ...units.map((u) => u.gx)]
  const ys = [...assetNodes.map((n) => n.gy), ...units.map((u) => u.gy)]
  const midX = xs.length > 0 ? (Math.min(...xs) + Math.max(...xs)) / 2 : 2.4
  const minGy = ys.length > 0 ? Math.min(...ys) : 2.2
  const planeY = minGy - PLANE_GAP

  const jarvisOnline = plane.jarvisOnline === true
  const managedDraft: EstatePlaneNode[] = (plane.managedAgents ?? [])
    .slice()
    .sort((a, b) => a.agentId.localeCompare(b.agentId))
    .map((profile, index) => ({
      id: `profile:${profile.agentId}`,
      profileId: profile.agentId,
      kind: 'managed-agent' as const,
      name: profile.displayName || profile.agentId,
      live: profile.enabled,
      gx: midX - 5.35 - (index % 4) * 1.55,
      gy: planeY + 0.18 + Math.floor(index / 4) * 1.45,
      sx: 0.98,
      sy: 0.76,
      h: 35,
    }))

  const workerDraft: EstatePlaneNode[] = (plane.workers ?? [])
    .slice()
    .sort((a, b) => a.workerId.localeCompare(b.workerId))
    .map((worker, index) => ({
      id: `worker:${worker.workerId}`,
      kind: 'agentd' as const,
      name: worker.workerId,
      live: null,
      gx: midX + 2.1 + (index % 4) * 1.55,
      gy: planeY + 0.22 + Math.floor(index / 4) * 1.45,
      sx: 0.72,
      sy: 0.52,
      h: 20,
    }))

  const planeDraft: EstatePlaneNode[] = [
    {
      id: 'plane:jarvis',
      kind: 'jarvis',
      name: '贾维斯',
      live: jarvisOnline,
      gx: midX - 3.6,
      gy: planeY + 0.18,
      sx: 0.98,
      sy: 0.76,
      h: 35,
    },
    {
      id: 'plane:brain',
      kind: 'brain',
      name: '中台',
      live: true,
      gx: midX - 0.64,
      gy: planeY,
      sx: 1.28,
      sy: 0.9,
      h: 60,
    },
    ...managedDraft,
    ...workerDraft,
  ]
  const planeNodes: EstatePlaneNode[] = []
  for (const n of planeDraft) {
    const placed = nudgeClear({ id: n.id, gx: n.gx, gy: n.gy, sx: n.sx, sy: n.sy }, occupied)
    occupied.push(placed)
    planeNodes.push({ ...n, gx: placed.gx, gy: placed.gy })
  }

  const edges: EstateEdge[] = [
    ...bindEdges,
    { fromId: 'plane:jarvis', toId: 'plane:brain', kind: 'control' },
    ...managedDraft.map((profile) => ({ fromId: profile.id, toId: 'plane:brain', kind: 'control' as const })),
    ...workerDraft.map((worker) => ({ fromId: worker.id, toId: 'plane:brain', kind: 'control' as const })),
  ]
  for (const u of units) {
    edges.push({ fromId: u.id, toId: 'plane:brain', kind: 'register' })
  }
  return { source, groups, assets: assetNodes, units, plane: planeNodes, edges }
}
