// 等距外形：近面朝左下。外形是控制台图例，接入模式另用机顶块标记。

import type { EstateAssetNode, EstateFigure, EstatePlaneNode } from './model'

export type Project = (gx: number, gy: number, gz: number) => { x: number; y: number }

export interface BoxPal {
  left: string
  right: string
  top: string
  edge: string
}

export function fillPoly(ctx: CanvasRenderingContext2D, pts: { x: number; y: number }[], fill: string, stroke?: string, width = 1) {
  ctx.beginPath()
  ctx.moveTo(pts[0].x, pts[0].y)
  for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i].x, pts[i].y)
  ctx.closePath()
  ctx.fillStyle = fill
  ctx.fill()
  if (stroke) {
    ctx.strokeStyle = stroke
    ctx.lineWidth = width
    ctx.stroke()
  }
}

export function drawBox(
  ctx: CanvasRenderingContext2D,
  P: Project,
  gx: number,
  gy: number,
  sx: number,
  sy: number,
  z0: number,
  h: number,
  pal: BoxPal,
) {
  const fy = gy + sy
  const fx = gx + sx
  const L = P(gx, fy, z0)
  const C = P(fx, fy, z0)
  const R = P(fx, gy, z0)
  const Lt = P(gx, fy, z0 + h)
  const Ct = P(fx, fy, z0 + h)
  const Rt = P(fx, gy, z0 + h)
  const Tl = P(gx, gy, z0 + h)
  fillPoly(ctx, [L, C, Ct, Lt], pal.left)
  fillPoly(ctx, [R, C, Ct, Rt], pal.right)
  fillPoly(ctx, [Tl, Rt, Ct, Lt], pal.top)
  ctx.strokeStyle = pal.edge
  ctx.lineWidth = 1
  ctx.beginPath()
  ctx.moveTo(L.x, L.y)
  ctx.lineTo(Lt.x, Lt.y)
  ctx.lineTo(Ct.x, Ct.y)
  ctx.lineTo(C.x, C.y)
  ctx.moveTo(Lt.x, Lt.y)
  ctx.lineTo(Tl.x, Tl.y)
  ctx.moveTo(Ct.x, Ct.y)
  ctx.lineTo(Rt.x, Rt.y)
  ctx.lineTo(R.x, R.y)
  ctx.stroke()
  return { fy, fx, Lt, Ct }
}

function faceFront(
  ctx: CanvasRenderingContext2D,
  P: Project,
  x0: number,
  x1: number,
  fy: number,
  z0: number,
  z1: number,
  fill: string,
  stroke?: string,
  width = 1,
) {
  fillPoly(ctx, [P(x0, fy, z0), P(x1, fy, z0), P(x1, fy, z1), P(x0, fy, z1)], fill, stroke, width)
}

function chassis(on: boolean, top: string): BoxPal {
  return {
    left: on ? '#2a241c' : '#161b21',
    right: on ? '#3a3226' : '#212830',
    top,
    edge: on ? '#e6d3a4' : 'rgba(210,200,180,0.35)',
  }
}

function hostBadge(ctx: CanvasRenderingContext2D, P: Project, gx: number, gy: number, z: number) {
  drawBox(ctx, P, gx, gy, 0.18, 0.18, z, 6, {
    left: '#1a2420',
    right: '#24332c',
    top: '#62e6a7',
    edge: 'rgba(98,230,167,0.7)',
  })
}

function drawWeb(ctx: CanvasRenderingContext2D, P: Project, ox: number, oy: number, on: boolean) {
  const chrome = on ? '#1a3a2e' : '#2a3238'
  const bx = ox + 0.04
  const bgy = oy + 0.08
  const bw = 1.2
  const bh = 36
  fillPoly(
    ctx,
    [P(bx, bgy, 0), P(bx, bgy + 0.08, 0), P(bx, bgy + 0.08, bh), P(bx, bgy, bh)],
    on ? '#2c2820' : '#1c2228',
  )
  faceFront(ctx, P, bx, bx + bw, bgy, 0, bh, '#0c1216', 'rgba(126,200,255,0.35)')
  faceFront(ctx, P, bx, bx + bw, bgy, bh - 5.2, bh, chrome)
  const dots = ['#ff746c', '#f1be5b', '#62e6a7']
  dots.forEach((fill, i) => {
    const c = P(bx + 0.1 + i * 0.11, bgy, bh - 2.6)
    ctx.beginPath()
    ctx.fillStyle = fill
    ctx.arc(c.x, c.y, 1.7, 0, Math.PI * 2)
    ctx.fill()
  })
  faceFront(ctx, P, bx + 0.42, bx + bw - 0.08, bgy, bh - 4.4, bh - 0.9, '#071014', 'rgba(126,200,255,0.22)', 0.8)
  faceFront(ctx, P, bx + 0.1, bx + bw - 0.12, bgy, 26.2, 28.2, 'rgba(98,230,167,0.45)')
  faceFront(ctx, P, bx + 0.1, bx + 0.86, bgy, 21.8, 23.6, 'rgba(126,200,255,0.22)')
  const dx = ox + 0.1
  const dgy = oy + 0.52
  const dsx = 0.56
  const fy = dgy + 0.38
  drawBox(ctx, P, dx, dgy, dsx, 0.38, 3, 14, {
    left: '#1a2026',
    right: '#222830',
    top: '#2c343c',
    edge: 'rgba(230,211,164,0.55)',
  })
  faceFront(ctx, P, dx + 0.05, dx + dsx - 0.05, fy, 13.8, 16.6, '#1c2428')
  const close = P(dx + dsx - 0.12, fy, 15.3)
  ctx.beginPath()
  ctx.fillStyle = '#ff746c'
  ctx.arc(close.x, close.y, 1.6, 0, Math.PI * 2)
  ctx.fill()
  faceFront(ctx, P, dx + 0.08, dx + 0.3, fy, 4.4, 7.2, '#245644', 'rgba(98,230,167,0.7)', 0.8)
  faceFront(ctx, P, dx + 0.34, dx + 0.5, fy, 4.4, 7.2, '#1a2024', 'rgba(139,152,161,0.55)', 0.8)
}

function drawRouter(ctx: CanvasRenderingContext2D, P: Project, ox: number, oy: number, on: boolean, top: string) {
  const gx = ox + 0.04
  const gy = oy + 0.08
  const sx = 1.32
  const sy = 0.58
  const fy = gy + sy
  drawBox(ctx, P, gx, gy, sx, sy, 0, 9, chassis(on, top))
  for (let i = 0; i < 6; i++) {
    const x = gx + 0.1 + i * 0.19
    faceFront(ctx, P, x, x + 0.13, fy, 2.2, 6.4, '#0b171c', 'rgba(126,200,255,0.45)', 0.8)
  }
  const led = on ? '#62e6a7' : '#8b98a1'
  for (let i = 0; i < 4; i++) {
    drawBox(ctx, P, gx + 0.2 + i * 0.26, fy - 0.16, 0.08, 0.1, 9, 2.4, {
      left: '#1a2420',
      right: '#24332c',
      top: led,
      edge: 'rgba(98,230,167,0.35)',
    })
  }
  const mast: BoxPal = {
    left: '#24333c',
    right: '#2c3c46',
    top: on ? '#7ec8ff' : '#8b98a1',
    edge: 'rgba(126,200,255,0.45)',
  }
  drawBox(ctx, P, gx + 0.1, fy - 0.14, 0.07, 0.07, 9, 16, mast)
  drawBox(ctx, P, gx + sx - 0.18, fy - 0.14, 0.07, 0.07, 9, 12, mast)
}

function drawServer(ctx: CanvasRenderingContext2D, P: Project, ox: number, oy: number, on: boolean, top: string) {
  const gx = ox + 0.04
  const gy = oy + 0.04
  const sx = 0.7
  const sy = 0.62
  const h = 38
  const fy = gy + sy
  drawBox(ctx, P, gx, gy, sx, sy, 0, h, chassis(on, top))
  for (let i = 0; i < 6; i++) {
    const z0 = 3.2 + i * 5.5
    faceFront(ctx, P, gx + 0.08, gx + sx - 0.16, fy, z0, z0 + 3.6, '#0b1014', 'rgba(126,200,255,0.22)', 0.7)
  }
  const led = on ? '#62e6a7' : '#8b98a1'
  for (let i = 0; i < 4; i++) {
    const c = P(gx + sx - 0.1, fy, 6 + i * 7)
    ctx.beginPath()
    ctx.fillStyle = led
    ctx.arc(c.x, c.y, 1.5, 0, Math.PI * 2)
    ctx.fill()
  }
}

function drawPc(ctx: CanvasRenderingContext2D, P: Project, ox: number, oy: number, on: boolean, top: string) {
  const pal = chassis(on, top)
  drawBox(ctx, P, ox, oy + 0.16, 0.34, 0.54, 0, 36, pal)
  for (let i = 1; i < 4; i++) {
    const z = (36 * i) / 4
    const a = P(ox, oy + 0.7, z)
    const b = P(ox + 0.34, oy + 0.7, z)
    ctx.beginPath()
    ctx.strokeStyle = 'rgba(215,197,154,0.32)'
    ctx.moveTo(a.x, a.y)
    ctx.lineTo(b.x, b.y)
    ctx.stroke()
  }
  drawBox(ctx, P, ox + 0.44, oy + 0.48, 0.74, 0.28, 0, 3.5, chassis(on, '#2a3238'))
  drawBox(ctx, P, ox + 0.7, oy + 0.54, 0.16, 0.14, 3.5, 8, chassis(on, '#2a3238'))
  drawBox(ctx, P, ox + 0.46, oy + 0.54, 0.7, 0.2, 11.5, 24, pal)
  faceFront(ctx, P, ox + 0.54, ox + 1.08, oy + 0.74, 16.8, 32.6, '#07141c', 'rgba(126,200,255,0.35)')
}

function drawAgent(ctx: CanvasRenderingContext2D, P: Project, ox: number, oy: number, on: boolean, live: boolean) {
  const helmTop = live ? '#3d8fd4' : '#4a5560'
  drawBox(ctx, P, ox + 0.26, oy + 0.28, 0.46, 0.36, 0, 4, chassis(on, '#2a3238'))
  drawBox(ctx, P, ox + 0.3, oy + 0.3, 0.38, 0.32, 4, 13, chassis(on, '#2a3238'))
  drawBox(ctx, P, ox + 0.08, oy + 0.18, 0.82, 0.5, 17, 7, chassis(on, live ? '#3a4650' : '#343c44'))
  drawBox(ctx, P, ox + 0.3, oy + 0.28, 0.38, 0.32, 24, 11, chassis(on, helmTop))
  faceFront(ctx, P, ox + 0.36, ox + 0.62, oy + 0.6, 27.4, 29.8, live ? '#7ec8ff' : '#8b98a1', live ? '#7ec8ff' : '#6a737a', 0.8)
}

function drawCitadel(ctx: CanvasRenderingContext2D, P: Project, ox: number, oy: number, on: boolean) {
  const pal = chassis(on, '#c4a574')
  pal.edge = on ? '#e6d3a4' : 'rgba(198,162,90,0.65)'
  drawBox(ctx, P, ox, oy + 0.04, 1.24, 0.82, 0, 36, pal)
  drawBox(ctx, P, ox + 0.16, oy + 0.16, 0.92, 0.58, 36, 14, pal)
  drawBox(ctx, P, ox + 0.32, oy + 0.26, 0.6, 0.4, 50, 10, {
    left: '#2a2218',
    right: '#3a2e20',
    top: on ? '#e6d3a4' : '#a88850',
    edge: 'rgba(230,210,160,0.7)',
  })
  faceFront(ctx, P, ox + 0.32, ox + 0.84, oy + 0.86, 4, 16, '#2a2218', 'rgba(230,210,160,0.35)', 0.8)
}

export function drawAssetFigure(
  ctx: CanvasRenderingContext2D,
  P: Project,
  n: EstateAssetNode,
  on: boolean,
  top: string,
) {
  const ox = n.gx
  const oy = n.gy
  const fig: EstateFigure = n.figure
  if (fig === 'web') drawWeb(ctx, P, ox, oy, on)
  else if (fig === 'router') drawRouter(ctx, P, ox, oy, on, top)
  else if (fig === 'pc') drawPc(ctx, P, ox, oy, on, top)
  else if (fig === 'agent') drawAgent(ctx, P, ox, oy, on, false)
  else drawServer(ctx, P, ox, oy, on, top)
  if (n.accessMode === 'ACCESS_MODE_EMBEDDED') {
    hostBadge(ctx, P, ox + n.sx * 0.62, oy + n.sy * 0.55, n.h)
  }
  if (n.criticality === 'CRITICALITY_P0') {
    const a = P(ox, oy + n.sy, n.h)
    const b = P(ox + n.sx, oy + n.sy, n.h)
    const c = P(ox + n.sx, oy, n.h)
    const d = P(ox, oy, n.h)
    ctx.strokeStyle = 'rgba(255,116,108,0.9)'
    ctx.lineWidth = 1.5
    ctx.beginPath()
    ctx.moveTo(a.x, a.y)
    ctx.lineTo(b.x, b.y)
    ctx.lineTo(c.x, c.y)
    ctx.lineTo(d.x, d.y)
    ctx.closePath()
    ctx.stroke()
  }
}

export function drawPlaneFigure(ctx: CanvasRenderingContext2D, P: Project, n: EstatePlaneNode, on: boolean) {
  if (n.kind === 'brain') {
    drawCitadel(ctx, P, n.gx, n.gy, on)
    return
  }
  if (n.kind === 'jarvis' || n.kind === 'managed-agent') {
    drawAgent(ctx, P, n.gx, n.gy, on, n.live === true)
    return
  }
  const top = n.kind === 'agentd' ? '#4a5560' : '#6a5840'
  drawBox(ctx, P, n.gx, n.gy, n.sx, n.sy, 0, n.h, chassis(on, top))
}
