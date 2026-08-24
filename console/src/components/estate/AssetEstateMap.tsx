// 资产拓扑图：等距中台 / Agent 岗位 + 单元绑定线。
// 数据由 buildEstate 投影；本组件只负责绘制、点选与缩放。贾维斯不得画到资产上。

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Maximize2, Minimize2, RotateCcw } from 'lucide-react'
import { Link } from 'react-router-dom'
import type { AccessMode, AssetDetail, Criticality } from '../../api/types'
import { HealthBadge } from '../ui'
import { drawAssetFigure, drawBox, drawPlaneFigure } from './draw'
import {
  buildEstate,
  healthTone,
  isPlaneId,
  type EstateAssetNode,
  type EstateEdgeKind,
  type EstateFigure,
  type EstatePlaneInput,
  type PlaneKind,
} from './model'

const MODE_ZH: Record<AccessMode, string> = {
  ACCESS_MODE_UNSPECIFIED: '未知',
  ACCESS_MODE_EMBEDDED: '在机',
  ACCESS_MODE_REMOTE: '远程',
  ACCESS_MODE_NETWORK: '旁路',
}
const CRIT_ZH: Record<Criticality, string> = {
  CRITICALITY_UNSPECIFIED: '未知',
  CRITICALITY_P0: 'P0',
  CRITICALITY_P1: 'P1',
  CRITICALITY_P2: 'P2',
}
const FIGURE_ZH: Record<EstateFigure, string> = {
  web: 'Web 服务',
  router: '路由器',
  pc: '个人电脑',
  server: '服务器',
  agent: '人工智能助手',
}
const PLANE_ZH: Record<PlaneKind, { kicker: string; blurb: string; rail: string }> = {
  brain: { kicker: '中台', blurb: '治理内核与三本账。控制台能打开，中台就在。', rail: '中台' },
  jarvis: { kicker: '编排 Agent', blurb: '只连中台：不持模型密钥，不直连资产或边缘。', rail: '贾维斯' },
  'managed-agent': { kicker: '受管 Agent', blurb: '流量审查逻辑岗位；工具与资产范围由中台档案约束，不代表远程安装的进程。', rail: 'Agent' },
  agentd: { kicker: '监督进程', blurb: '来自中台已登记执行池；孵化短命执行实例，不上数据路径。', rail: '监督' },
}

export interface AssetEstateMapProps {
  assets: AssetDetail[]
  plane?: EstatePlaneInput
  /** ListAssets 还有下一页时提示截断（上限 pageSize=200）。 */
  truncated?: boolean
  density?: 'compact' | 'full'
  /** card 是仪表盘/台账里的面板；bare 是编排场铺满背景。 */
  layout?: 'card' | 'bare'
  /** 会话工具链点名的资产：画脉冲高亮。 */
  focusAssetIds?: string[]
  caseStats?: Record<string, { openCount: number; highestPriority: number }>
  onOpenAsset?: (assetId: string) => void
  onOpenJarvis?: () => void
  onSelectAsset?: (assetId: string) => void
  onSelectAgent?: (agentId: string) => void
  onClearSelection?: () => void
}

const TW = 56
const TH = 28
const ZOOM_MIN = 0.4
const ZOOM_MAX = 3.2
const ZOOM_FACTOR = 1.2
const PAD_INK: Record<string, string> = {
  payments: '#1a3a2e',
  mall: '#1a2e3a',
  erp: '#3a2420',
  prod: '#1a3a2e',
  staging: '#3a3018',
}

function iso(camX: number, camY: number, scale: number, gx: number, gy: number, gz: number) {
  return {
    x: camX + (gx - gy) * (TW / 2) * scale,
    y: camY + (gx + gy) * (TH / 2) * scale - gz * scale,
  }
}

function unscaled(gx: number, gy: number, gz = 0) {
  return { x: (gx - gy) * (TW / 2), y: (gx + gy) * (TH / 2) - gz }
}

function liveLabel(live: boolean | null): string {
  if (live === true) return '在线'
  if (live === false) return '离线'
  return '岗位'
}

function clampZoom(s: number): number {
  return Math.max(ZOOM_MIN, Math.min(ZOOM_MAX, s))
}

function zoomLabel(s: number): number {
  return Math.round(s * 100)
}

const STAGE_COMPACT = 320
const STAGE_FULL = 400
const STAGE_VIEW_GAP = 32

function cardChrome(el: HTMLElement): number {
  const head = el.querySelector('.fs-panel-head')
  const rail = el.querySelector('.yf-estate-rail')
  return (head?.getBoundingClientRect().height ?? 52) + (rail?.getBoundingClientRect().height ?? 46)
}

function expandedStageHeight(el: HTMLElement, compact: boolean): number {
  const floor = compact ? STAGE_COMPACT : STAGE_FULL
  return Math.max(floor, Math.round(window.innerHeight - cardChrome(el) - STAGE_VIEW_GAP))
}

function centerCard(el: HTMLElement) {
  const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  const rect = el.getBoundingClientRect()
  const top = window.scrollY + rect.top
  const target = top - Math.max(0, (window.innerHeight - rect.height) / 2)
  window.scrollTo({ top: Math.max(0, target), behavior: reduce ? 'auto' : 'smooth' })
}

export function AssetEstateMap({
  assets,
  plane,
  truncated = false,
  density = 'full',
  layout = 'card',
  focusAssetIds = [],
  caseStats = {},
  onOpenAsset,
  onOpenJarvis,
  onSelectAsset,
  onSelectAgent,
  onClearSelection,
}: AssetEstateMapProps) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const cardRef = useRef<HTMLElement | null>(null)
  const expandTouched = useRef(false)
  const [canvasReady, setCanvasReady] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const [stageH, setStageH] = useState<number | null>(null)
  const setCanvasNode = useCallback((el: HTMLCanvasElement | null) => {
    canvasRef.current = el
    setCanvasReady(el !== null)
  }, [])
  const graph = buildEstate(assets, plane)
  const [hover, setHover] = useState<string | null>(null)
  const [selected, setSelected] = useState<string | null>(null)
  const cam = useRef({ x: 0, y: 0, s: 1 })
  const last = useRef({ w: 0, h: 0 })
  const fitted = useRef('')
  const graphSig = useRef('')
  const userCam = useRef(false)
  const [zoomPct, setZoomPct] = useState(100)
  const zoomApi = useRef<{
    by: (factor: number, cx?: number, cy?: number) => void
    reset: () => void
  }>({
    by: () => {},
    reset: () => {},
  })
  const graphRef = useRef(graph)
  const hoverRef = useRef(hover)
  const selectedRef = useRef(selected)
  const openRef = useRef(onOpenAsset)
  const jarvisRef = useRef(onOpenJarvis)
  const selectRef = useRef(onSelectAsset)
  const selectAgentRef = useRef(onSelectAgent)
  const clearRef = useRef(onClearSelection)
  const densityRef = useRef(density)
  const focusRef = useRef(focusAssetIds)
  useLayoutEffect(() => {
    graphRef.current = graph
    hoverRef.current = hover
    selectedRef.current = selected
    openRef.current = onOpenAsset
    jarvisRef.current = onOpenJarvis
    selectRef.current = onSelectAsset
    selectAgentRef.current = onSelectAgent
    clearRef.current = onClearSelection
    densityRef.current = density
    focusRef.current = focusAssetIds
  })

  const toggleExpand = () => {
    const el = cardRef.current
    const next = !expanded
    expandTouched.current = true
    setExpanded(next)
    setStageH(el && next ? expandedStageHeight(el, density === 'compact') : null)
    userCam.current = false
    fitted.current = ''
  }

  useEffect(() => {
    if (!expanded) return
    const onResize = () => {
      const el = cardRef.current
      if (el) setStageH(expandedStageHeight(el, density === 'compact'))
    }
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [expanded, density])

  useEffect(() => {
    if (!expandTouched.current) return
    if (expanded && stageH === null) return
    expandTouched.current = false
    const el = cardRef.current
    if (!el) return
    const id = requestAnimationFrame(() => centerCard(el))
    return () => cancelAnimationFrame(id)
  }, [expanded, stageH])

  const selectedAsset = graph.assets.find((a) => a.id === selected) ?? null
  const selectedPlane = graph.plane.find((n) => n.id === selected) ?? null
  const graphKey =
    graph.assets.map((a) => a.id).join(',') +
    '|' +
    graph.units.map((u) => u.id).join(',') +
    '|' +
    graph.plane.map((n) => `${n.kind}:${n.live}`).join(',')

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    let raf = 0
    let visible = true
    const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    let invalidate = () => undefined

    const hit = (px: number, py: number): string | null => {
      const { w } = last.current
      const radius = 16 + 14 * cam.current.s
      const gph = graphRef.current
      const nodes = [
        ...gph.plane.map((n) => ({ id: n.id, gx: n.gx + n.sx / 2, gy: n.gy + n.sy / 2, h: n.h })),
        ...gph.assets.map((n) => ({ id: n.id, gx: n.gx + n.sx / 2, gy: n.gy + n.sy / 2, h: n.h })),
      ].sort((a, b) => b.gx + b.gy - (a.gx + a.gy))
      for (const n of nodes) {
        const c = iso(w / 2 + cam.current.x, cam.current.y, cam.current.s, n.gx, n.gy, n.h / 2)
        if (Math.hypot(c.x - px, c.y - py) < radius) return n.id
      }
      return null
    }

    const publishZoom = () => {
      const pct = zoomLabel(cam.current.s)
      setZoomPct((cur) => (cur === pct ? cur : pct))
    }
    const applyFit = (gph0: typeof graphRef.current, w: number, h: number, fitId: string) => {
      let minX = 1e9,
        minY = 1e9,
        maxX = -1e9,
        maxY = -1e9
      const mark = (gx: number, gy: number, gz = 0) => {
        const s = unscaled(gx, gy, gz)
        minX = Math.min(minX, s.x)
        maxX = Math.max(maxX, s.x)
        minY = Math.min(minY, s.y)
        maxY = Math.max(maxY, s.y)
      }
      gph0.assets.forEach((n) => {
        mark(n.gx - 0.25, n.gy - 0.25)
        mark(n.gx + n.sx + 0.35, n.gy + n.sy + 0.9, n.h)
      })
      gph0.units.forEach((u) => {
        mark(u.gx, u.gy)
        mark(u.gx + u.sx, u.gy + u.sy + 0.4, 12)
      })
      gph0.plane.forEach((n) => {
        mark(n.gx - 0.2, n.gy - 0.2)
        mark(n.gx + n.sx + 0.35, n.gy + n.sy + 0.7, n.h)
      })
      const bw = Math.max(40, maxX - minX)
      const bh = Math.max(40, maxY - minY)
      const pad = densityRef.current === 'compact' ? 36 : 48
      cam.current.s = clampZoom(Math.min((w - pad * 2) / bw, (h - pad * 2) / bh))
      cam.current.x = -((minX + maxX) / 2) * cam.current.s
      cam.current.y = h * 0.5 - ((minY + maxY) / 2) * cam.current.s
      fitted.current = fitId
      publishZoom()
    }
    const zoomAt = (factor: number, px?: number, py?: number) => {
      const { w, h } = last.current
      const oldS = cam.current.s
      const next = clampZoom(oldS * factor)
      if (next === oldS) {
        publishZoom()
        return
      }
      const cx = px ?? w / 2
      const cy = py ?? h / 2
      const worldX = (cx - w / 2 - cam.current.x) / oldS
      const worldY = (cy - cam.current.y) / oldS
      cam.current.s = next
      cam.current.x = cx - w / 2 - worldX * next
      cam.current.y = cy - worldY * next
      userCam.current = true
      publishZoom()
      invalidate()
    }
    zoomApi.current = {
      by: (factor, cx, cy) => zoomAt(factor, cx, cy),
      reset: () => {
        userCam.current = false
        const gph0 = graphRef.current
        const { w, h } = last.current
        const graphOnly = `${gph0.assets.map((a) => `${a.id}:${a.gx}:${a.gy}`).join(',')}|${gph0.plane.map((n) => n.kind).join(',')}`
        applyFit(gph0, w, h, `${graphOnly}@${w}x${h}`)
        invalidate()
      },
    }

    const draw = (t: number) => {
      const dpr = Math.min(window.devicePixelRatio || 1, 2)
      const rect = canvas.getBoundingClientRect()
      const w = Math.max(1, Math.floor(rect.width))
      const h = Math.max(1, Math.floor(rect.height))
      if (canvas.width !== w * dpr || canvas.height !== h * dpr) {
        canvas.width = w * dpr
        canvas.height = h * dpr
      }
      last.current = { w, h }
      const ctx = canvas.getContext('2d')
      if (!ctx) return
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
      ctx.clearRect(0, 0, w, h)
      const gph0 = graphRef.current
      const graphOnly = `${gph0.assets.map((a) => `${a.id}:${a.gx}:${a.gy}`).join(',')}|${gph0.plane.map((n) => n.kind).join(',')}`
      const fitId = `${graphOnly}@${w}x${h}`
      if (graphSig.current !== graphOnly) {
        graphSig.current = graphOnly
        userCam.current = false
        fitted.current = ''
      }
      if (!userCam.current && fitted.current !== fitId && (gph0.assets.length > 0 || gph0.plane.length > 0)) {
        applyFit(gph0, w, h, fitId)
      }
      const cx = w / 2 + cam.current.x
      const cy = cam.current.y
      const p = (gx: number, gy: number, gz: number) => iso(cx, cy, cam.current.s, gx, gy, gz)

      const diamond = (gx: number, gy: number, fill: string, stroke?: string) => {
        const a = p(gx, gy, 0),
          b = p(gx + 1, gy, 0),
          c = p(gx + 1, gy + 1, 0),
          d = p(gx, gy + 1, 0)
        ctx.beginPath()
        ctx.moveTo(a.x, a.y)
        ctx.lineTo(b.x, b.y)
        ctx.lineTo(c.x, c.y)
        ctx.lineTo(d.x, d.y)
        ctx.closePath()
        ctx.fillStyle = fill
        ctx.fill()
        if (stroke) {
          ctx.strokeStyle = stroke
          ctx.lineWidth = 1
          ctx.stroke()
        }
      }

      const gph = graphRef.current
      const paintPad = (minGx: number, minGy: number, maxGx: number, maxGy: number, ink: string, label: string) => {
        for (let x = Math.floor(minGx); x < maxGx; x++) {
          for (let y = Math.floor(minGy); y < maxGy; y++) {
            diamond(x, y, (x + y) & 1 ? '#141a1f' : '#10161a', 'rgba(80,90,96,0.16)')
          }
        }
        ctx.globalAlpha = 0.38
        for (let x = Math.floor(minGx); x < maxGx; x++) {
          for (let y = Math.floor(minGy); y < maxGy; y++) diamond(x, y, ink)
        }
        ctx.globalAlpha = 1
        const lab = p((minGx + maxGx) / 2, maxGy + 0.1, 1)
        ctx.fillStyle = '#8b98a1'
        ctx.font = '500 10px ui-monospace, SFMono-Regular, Menlo, monospace'
        ctx.textAlign = 'center'
        ctx.fillText(label, lab.x, lab.y)
        ctx.textAlign = 'left'
      }

      if (gph.plane.length > 0) {
        const minGx = Math.min(...gph.plane.map((n) => n.gx)) - 0.4
        const minGy = Math.min(...gph.plane.map((n) => n.gy)) - 0.35
        const maxGx = Math.max(...gph.plane.map((n) => n.gx + n.sx)) + 0.45
        const maxGy = Math.max(...gph.plane.map((n) => n.gy + n.sy)) + 0.4
        paintPad(minGx, minGy, maxGx, maxGy, '#241c14', '中台侧')
      }

      for (const g of gph.groups) {
        const nodes = gph.assets.filter((a) => a.groupKey === g.key)
        if (nodes.length === 0) continue
        const minGx = Math.min(...nodes.map((n) => n.gx)) - 0.35
        const minGy = Math.min(...nodes.map((n) => n.gy)) - 0.35
        const maxGx = Math.max(...nodes.map((n) => n.gx + n.sx)) + 0.45
        const maxGy =
          Math.max(
            ...nodes.map((n) => n.gy + n.sy),
            ...gph.units.filter((u) => u.assetIds.some((id) => nodes.some((n) => n.id === id))).map((u) => u.gy + 0.7),
          ) + 0.35
        paintPad(minGx, minGy, maxGx, maxGy, PAD_INK[g.key] ?? '#1c2428', g.label)
      }

      const focus = selectedRef.current ?? hoverRef.current
      const related = new Set<string>()
      const litIds = new Set(focusRef.current)
      if (focus) {
        related.add(focus)
        gph.edges.forEach((e) => {
          if (e.fromId === focus || e.toId === focus) {
            related.add(e.fromId)
            related.add(e.toId)
          }
        })
        if (focus === 'plane:brain') {
          gph.plane.forEach((n) => related.add(n.id))
          gph.units.forEach((u) => related.add(u.id))
        }
      }
      for (const id of litIds) {
        related.add(id)
        gph.edges.forEach((e) => {
          if (e.fromId === id || e.toId === id) {
            related.add(e.fromId)
            related.add(e.toId)
          }
        })
      }

      const posOf = (id: string): { gx: number; gy: number; h: number } | null => {
        const pl = gph.plane.find((n) => n.id === id)
        if (pl) return { gx: pl.gx + pl.sx / 2, gy: pl.gy + pl.sy / 2, h: pl.h }
        const un = gph.units.find((n) => n.id === id)
        if (un) return { gx: un.gx + un.sx / 2, gy: un.gy + un.sy / 2, h: 10 }
        const as = gph.assets.find((n) => n.id === id)
        if (as) return { gx: as.gx + as.sx / 2, gy: as.gy + as.sy / 2, h: as.h }
        return null
      }

      const edgeColor = (kind: EstateEdgeKind, hot: boolean): string => {
        if (hot) {
          if (kind === 'control') return '#e6c27a'
          if (kind === 'local') return '#62e6a7'
          if (kind === 'register') return '#7ec8ff'
          return '#62e6a7'
        }
        if (kind === 'control') return 'rgba(198,162,90,0.5)'
        if (kind === 'local') return 'rgba(98,230,167,0.4)'
        if (kind === 'register') return 'rgba(126,200,255,0.38)'
        return 'rgba(90,168,200,0.45)'
      }

      const strokePath = (ax: number, ay: number, ah: number, bx: number, by: number, bh: number, kind: EstateEdgeKind, dashed: boolean, hot: boolean) => {
        const a = p(ax, ay, Math.min(ah, 18) * 0.35)
        const mid = p(bx, ay, 2.2)
        const b = p(bx, by, Math.min(bh, 18) * 0.35)
        ctx.beginPath()
        ctx.moveTo(a.x, a.y)
        ctx.lineTo(mid.x, mid.y)
        ctx.lineTo(b.x, b.y)
        ctx.strokeStyle = edgeColor(kind, hot)
        ctx.lineWidth = hot ? 2.2 : kind === 'control' ? 1.6 : 1.3
        ctx.setLineDash(dashed ? [5, 4] : [])
        ctx.stroke()
        ctx.setLineDash([])
        if (!reduce && hot) {
          const tt = (t * 0.00018) % 1
          const x = tt < 0.5 ? a.x + (mid.x - a.x) * tt * 2 : mid.x + (b.x - mid.x) * (tt - 0.5) * 2
          const y = tt < 0.5 ? a.y + (mid.y - a.y) * tt * 2 : mid.y + (b.y - mid.y) * (tt - 0.5) * 2
          ctx.beginPath()
          ctx.fillStyle = edgeColor(kind, true)
          ctx.arc(x, y, 2, 0, Math.PI * 2)
          ctx.fill()
        }
      }

      const assetById = new Map(gph.assets.map((a) => [a.id, a]))
      for (const e of gph.edges) {
        const a = posOf(e.fromId)
        const b = posOf(e.toId)
        if (!a || !b) continue
        const hot = !focus || related.has(e.fromId)
        const remote = e.kind === 'bind' && assetById.get(e.toId)?.accessMode === 'ACCESS_MODE_REMOTE'
        const dashed = e.kind === 'local' || remote
        ctx.globalAlpha = focus && !hot ? 0.1 : 1
        strokePath(a.gx, a.gy, a.h, b.gx, b.gy, b.h, e.kind, dashed, hot && !!focus)
        ctx.globalAlpha = 1
      }

      const drawAsset = (n: EstateAssetNode, on: boolean) => {
        const tone = healthTone(n.health)
        const top = tone === 'healthy' ? '#245644' : tone === 'degraded' ? '#5a4820' : '#3a4248'
        drawAssetFigure(ctx, p, n, on, top)
        const cap = p(n.gx + n.sx / 2, n.gy + n.sy / 2, n.h)
        if (on || n.criticality === 'CRITICALITY_P0') {
          ctx.fillStyle = on ? '#e9edf0' : '#b7c4cb'
          ctx.font = (on ? '600 12px' : '500 11px') + ' ui-sans-serif, system-ui, sans-serif'
          ctx.textAlign = 'center'
          ctx.fillText(n.name, cap.x, cap.y - 10)
          ctx.textAlign = 'left'
        }
      }

      const drawPlane = (n: (typeof gph.plane)[number], on: boolean) => {
        drawPlaneFigure(ctx, p, n, on)
        const cap = p(n.gx + n.sx / 2, n.gy + n.sy / 2, n.h)
        ctx.fillStyle = on || n.kind === 'brain' || n.live === true ? '#e9edf0' : '#8b98a1'
        ctx.font = (on ? '600 11px' : '500 10px') + ' ui-sans-serif, system-ui, sans-serif'
        ctx.textAlign = 'center'
        ctx.fillText(n.name, cap.x, cap.y - 9)
        ctx.textAlign = 'left'
      }

      const drawables: { key: string; depth: number; draw: () => void }[] = []
      for (const n of gph.plane) {
        const on = n.id === hoverRef.current || n.id === selectedRef.current
        drawables.push({
          key: n.id,
          depth: n.gx + n.gy,
          draw: () => {
            ctx.globalAlpha = focus && !related.has(n.id) ? 0.22 : 1
            drawPlane(n, on)
            ctx.globalAlpha = 1
          },
        })
      }
      for (const n of gph.assets) {
        const on = n.id === hoverRef.current || n.id === selectedRef.current || litIds.has(n.id)
        drawables.push({
          key: n.id,
          depth: n.gx + n.gy,
          draw: () => {
            ctx.globalAlpha = focus && !related.has(n.id) && !litIds.has(n.id) ? 0.22 : 1
            drawAsset(n, on)
            if (litIds.has(n.id) && !reduce) {
              const cap = p(n.gx + n.sx / 2, n.gy + n.sy / 2, n.h)
              const pulse = 0.55 + 0.45 * Math.sin(t * 0.006)
              ctx.beginPath()
              ctx.strokeStyle = `rgba(98,230,167,${0.35 + pulse * 0.5})`
              ctx.lineWidth = 2
              ctx.arc(cap.x, cap.y, 20 + pulse * 10, 0, Math.PI * 2)
              ctx.stroke()
            }
            ctx.globalAlpha = 1
          },
        })
      }
      for (const u of gph.units) {
        drawables.push({
          key: u.id,
          depth: u.gx + u.gy,
          draw: () => {
            const on = related.has(u.id)
            ctx.globalAlpha = focus && !related.has(u.id) ? 0.2 : 1
            drawBox(ctx, p, u.gx, u.gy, u.sx, u.sy, 0, 10, {
              left: '#141c22',
              right: '#1c2830',
              top: on ? '#1e3a48' : '#1a2a32',
              edge: on ? '#7ec8ff' : 'rgba(126,200,255,0.3)',
            })
            if (on || !focus) {
              const lab = p(u.gx + u.sx / 2, u.gy + u.sy + 0.05, 11)
              ctx.fillStyle = '#8b98a1'
              ctx.font = '500 9px ui-monospace, SFMono-Regular, Menlo, monospace'
              ctx.textAlign = 'center'
              ctx.fillText(u.id, lab.x, lab.y)
              ctx.textAlign = 'left'
            }
            ctx.globalAlpha = 1
          },
        })
      }
      drawables.sort((a, b) => a.depth - b.depth).forEach((d) => d.draw())
    }

    const animationNeeded = () => !reduce && visible && !document.hidden &&
      (focusRef.current.length > 0 || hoverRef.current !== null || selectedRef.current !== null)
    const loop = (t: number) => {
      raf = 0
      draw(t)
      if (animationNeeded()) raf = requestAnimationFrame(loop)
    }
    invalidate = () => {
      if (raf === 0 && visible && !document.hidden) raf = requestAnimationFrame(loop)
    }
    draw(0)
    const ro = new ResizeObserver(() => {
      if (!userCam.current) fitted.current = ''
      invalidate()
    })
    ro.observe(canvas)
    const intersection = typeof IntersectionObserver === 'undefined' ? null : new IntersectionObserver(([entry]) => {
      visible = entry?.isIntersecting ?? false
      if (visible) invalidate()
      else if (raf !== 0) {
        cancelAnimationFrame(raf)
        raf = 0
      }
    })
    intersection?.observe(canvas)
    const onVisibility = () => {
      if (document.hidden && raf !== 0) {
        cancelAnimationFrame(raf)
        raf = 0
      } else {
        invalidate()
      }
    }
    document.addEventListener('visibilitychange', onVisibility)

    const pos = (e: PointerEvent) => {
      const r = canvas.getBoundingClientRect()
      return { x: e.clientX - r.left, y: e.clientY - r.top }
    }
    const onMove = (e: PointerEvent) => {
      if (pan.current) {
        cam.current.x = e.clientX - pan.current.x
        cam.current.y = e.clientY - pan.current.y
        invalidate()
        return
      }
      const { x, y } = pos(e)
      const id = hit(x, y)
      setHover(id)
      invalidate()
      canvas.style.cursor = id ? 'pointer' : pan.current ? 'grabbing' : 'grab'
    }
    const pan = { current: null as { x: number; y: number } | null }
    const activate = (id: string) => {
      setSelected(id)
      invalidate()
      if (!isPlaneId(id)) selectRef.current?.(id)
      if (id.startsWith('profile:')) selectAgentRef.current?.(id.slice('profile:'.length))
      if (id === 'plane:jarvis') jarvisRef.current?.()
      if (densityRef.current !== 'compact') return
      if (!isPlaneId(id)) openRef.current?.(id)
    }
    const onDown = (e: PointerEvent) => {
      const { x, y } = pos(e)
      const id = hit(x, y)
      if (id) {
        activate(id)
        return
      }
      setSelected(null)
      clearRef.current?.()
      invalidate()
      pan.current = { x: e.clientX - cam.current.x, y: e.clientY - cam.current.y }
    }
    const onUp = () => {
      pan.current = null
    }
    const onDblClick = (e: MouseEvent) => {
      const r = canvas.getBoundingClientRect()
      const id = hit(e.clientX - r.left, e.clientY - r.top)
      if (!id) return
      if (id === 'plane:jarvis') jarvisRef.current?.()
      else if (!isPlaneId(id)) openRef.current?.(id)
    }
    const onLeave = () => {
      setHover(null)
      invalidate()
    }
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const r = canvas.getBoundingClientRect()
      const factor = e.deltaY > 0 ? 1 / ZOOM_FACTOR : ZOOM_FACTOR
      zoomAt(factor, e.clientX - r.left, e.clientY - r.top)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === '+' || e.key === '=') {
        e.preventDefault()
        zoomAt(ZOOM_FACTOR)
      } else if (e.key === '-' || e.key === '_') {
        e.preventDefault()
        zoomAt(1 / ZOOM_FACTOR)
      } else if (e.key === '0') {
        e.preventDefault()
        userCam.current = false
        fitted.current = ''
        invalidate()
      } else if (e.key === 'Escape') {
        setSelected(null)
        clearRef.current?.()
        invalidate()
      }
    }
    canvas.addEventListener('pointermove', onMove)
    canvas.addEventListener('pointerdown', onDown)
    canvas.addEventListener('pointerup', onUp)
    canvas.addEventListener('pointerleave', onLeave)
    canvas.addEventListener('dblclick', onDblClick)
    canvas.addEventListener('wheel', onWheel, { passive: false })
    canvas.addEventListener('keydown', onKey)
    window.addEventListener('pointerup', onUp)
    return () => {
      cancelAnimationFrame(raf)
      ro.disconnect()
      intersection?.disconnect()
      document.removeEventListener('visibilitychange', onVisibility)
      canvas.removeEventListener('pointermove', onMove)
      canvas.removeEventListener('pointerdown', onDown)
      canvas.removeEventListener('pointerup', onUp)
      canvas.removeEventListener('pointerleave', onLeave)
      canvas.removeEventListener('dblclick', onDblClick)
      canvas.removeEventListener('wheel', onWheel)
      canvas.removeEventListener('keydown', onKey)
      window.removeEventListener('pointerup', onUp)
    }
  }, [graphKey, canvasReady])

  const jarvis = graph.plane.find((n) => n.kind === 'jarvis')

  return (
    <section
      ref={cardRef}
      className={`yf-estate${layout === 'card' ? ' fs-panel' : ' yf-estate--bare'}${density === 'compact' ? ' yf-estate--compact' : ''}${expanded ? ' yf-estate--expanded' : ''}`}
      aria-label="资产拓扑"
    >
      {layout === 'card' && (
      <div className="fs-panel-head">
        <div>
          <p className="fs-panel-title">资产拓扑</p>
          <p className="fs-panel-sub">
            PLANE
            {graph.source === 'none' ? ' · UNIT BINDING' : ` · UNIT BINDING · LABEL ${graph.source.toUpperCase()}`}
          </p>
        </div>
        <p className="yf-estate-meta">
          {graph.assets.length} 台资产 · {graph.units.length} 个单元
          {truncated ? ' · 仅前 200 台' : ''}
          {` · 贾维斯${jarvis?.live ? '在线' : '离线'}`}
          {` · Edge${plane?.edgeReady ? '就绪' : '未就绪'}`}
          {graph.source === 'none' ? ' · 无业务线/环境标签，未分组' : ` · 按 labels.${graph.source} 弱分组`}
        </p>
      </div>
      )}
      <div className="yf-estate-stage" style={stageH !== null ? { height: stageH } : undefined}>
        <canvas ref={setCanvasNode} tabIndex={0} aria-label="资产拓扑画布" />
        <div className="yf-estate-zoom" role="group" aria-label="缩放">
          <button type="button" aria-label="缩小" onClick={() => zoomApi.current.by(1 / ZOOM_FACTOR)}>
            −
          </button>
          <span className="yf-estate-zoom-pct" aria-live="polite" aria-label="当前缩放">
            {zoomPct}%
          </span>
          <button type="button" aria-label="放大" onClick={() => zoomApi.current.by(ZOOM_FACTOR)}>
            +
          </button>
          <button type="button" aria-label="复位" onClick={() => zoomApi.current.reset()}>
            <RotateCcw size={13} strokeWidth={2} aria-hidden />
          </button>
          {layout === 'card' && (
            <>
              <span className="yf-estate-zoom-split" aria-hidden />
              <button type="button" aria-label={expanded ? '收起' : '扩大'} aria-expanded={expanded} onClick={toggleExpand}>
                {expanded ? <Minimize2 size={13} strokeWidth={2} aria-hidden /> : <Maximize2 size={13} strokeWidth={2} aria-hidden />}
              </button>
            </>
          )}
        </div>
        {selectedAsset && (
          <aside className="yf-estate-ins" aria-label="选中资产">
            <p className="yf-estate-ins-kicker">
              {MODE_ZH[selectedAsset.accessMode]} · {CRIT_ZH[selectedAsset.criticality]} · {FIGURE_ZH[selectedAsset.figure]}
            </p>
            <h3>{selectedAsset.name}</h3>
            <p className="yf-estate-ins-id">{selectedAsset.id}</p>
            <p className="yf-estate-ins-row">
              <HealthBadge health={selectedAsset.health} />
              <span>绑定 {selectedAsset.unitIds.length} 个单元</span>
              <span>在役 {selectedAsset.activeReleaseCount}</span>
              {(caseStats[selectedAsset.id]?.openCount ?? 0) > 0 && <span>未结案件 {caseStats[selectedAsset.id].openCount} · 最高 {caseStats[selectedAsset.id].highestPriority}</span>}
            </p>
            {selectedAsset.unitIds.length === 0 && <p className="yf-estate-ins-mute">未绑定单元，没有保护边</p>}
            <Link to={`/assets/${selectedAsset.id}`} className="yf-estate-open">
              打开详情
            </Link>
          </aside>
        )}
        {selectedPlane && (
          <aside className="yf-estate-ins" aria-label="选中岗位">
            <p className="yf-estate-ins-kicker">
              {PLANE_ZH[selectedPlane.kind].kicker} · {selectedPlane.kind === 'managed-agent' ? (selectedPlane.live ? '已启用' : '已停用') : liveLabel(selectedPlane.live)}
            </p>
            <h3>{selectedPlane.name}</h3>
            <p className="yf-estate-ins-id">{selectedPlane.id}</p>
            <p className="yf-estate-ins-mute">{PLANE_ZH[selectedPlane.kind].blurb}</p>
            {selectedPlane.kind === 'jarvis' && (
              <Link to="/agent" className="yf-estate-open">
                打开编排
              </Link>
            )}
          </aside>
        )}
        {assets.length === 0 && <p className="yf-estate-empty yf-estate-empty--overlay">还没有登记资产，中台侧岗位仍在</p>}
      </div>
      <div className="yf-estate-rail" aria-label="可键盘选择的拓扑节点">
        {graph.plane.map((n) => (
          <button
            key={n.id}
            type="button"
            className={n.id === selected ? 'is-on' : undefined}
            aria-pressed={n.id === selected}
            aria-label={`选中${n.name}`}
            onClick={() => {
              setSelected(n.id)
              if (n.kind === 'jarvis') onOpenJarvis?.()
              if (n.kind === 'managed-agent' && n.profileId) onSelectAgent?.(n.profileId)
            }}
          >
            {PLANE_ZH[n.kind].rail}
          </button>
        ))}
        {graph.assets.map((a) => (
          <button
            key={a.id}
            type="button"
            className={a.id === selected ? 'is-on' : undefined}
            aria-pressed={a.id === selected}
            aria-label={`选中资产 ${a.name}`}
            onClick={() => {
              setSelected(a.id)
              onSelectAsset?.(a.id)
              if (density === 'compact') onOpenAsset?.(a.id)
            }}
          >
            <span>{a.id}</span>
            {(caseStats[a.id]?.openCount ?? 0) > 0 && <span className="ml-1 rounded bg-[#7f302d] px-1 text-[9px] text-white">{caseStats[a.id].openCount}</span>}
          </button>
        ))}
      </div>
    </section>
  )
}
