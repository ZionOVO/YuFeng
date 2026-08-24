import type { EstateAssetNode, EstateFigure, EstatePlaneNode } from './model'
import { drawAssetFigure, drawBox, drawPlaneFigure, fillPoly, type Project } from './draw'

function recordingContext() {
  const fillStyles: string[] = []
  const strokeStyles: string[] = []
  const lineWidths: number[] = []
  let fillStyle = ''
  let strokeStyle = ''
  let lineWidth = 1
  const ctx = {
    beginPath: vi.fn(),
    moveTo: vi.fn(),
    lineTo: vi.fn(),
    closePath: vi.fn(),
    fill: vi.fn(),
    stroke: vi.fn(),
    arc: vi.fn(),
    get fillStyle() { return fillStyle },
    set fillStyle(value: string | CanvasGradient | CanvasPattern) {
      fillStyle = String(value)
      fillStyles.push(fillStyle)
    },
    get strokeStyle() { return strokeStyle },
    set strokeStyle(value: string | CanvasGradient | CanvasPattern) {
      strokeStyle = String(value)
      strokeStyles.push(strokeStyle)
    },
    get lineWidth() { return lineWidth },
    set lineWidth(value: number) {
      lineWidth = value
      lineWidths.push(value)
    },
  } as unknown as CanvasRenderingContext2D
  return { ctx, fillStyles, strokeStyles, lineWidths }
}

const project: Project = (gx, gy, gz) => ({ x: gx * 10 + gy * 3, y: gy * 7 - gz * 2 })

function assetNode(figure: EstateFigure, overrides: Partial<EstateAssetNode> = {}): EstateAssetNode {
  return {
    id: `asset-${figure}`,
    name: figure,
    accessMode: 'ACCESS_MODE_REMOTE',
    criticality: 'CRITICALITY_P2',
    health: 'UNIT_HEALTH_HEALTHY',
    unitIds: [],
    activeReleaseCount: 0,
    labels: {},
    groupKey: '',
    figure,
    gx: 2,
    gy: 3,
    sx: 1.4,
    sy: 0.9,
    h: 38,
    ...overrides,
  }
}

function planeNode(kind: EstatePlaneNode['kind'], live: boolean | null): EstatePlaneNode {
  return { id: `plane:${kind}`, kind, name: kind, live, gx: 1, gy: 2, sx: 1.2, sy: 0.8, h: 36 }
}

describe('estate canvas drawing', () => {
  it('fillPoly 按顶点顺序闭合路径并应用指定边线', () => {
    const { ctx, fillStyles, strokeStyles, lineWidths } = recordingContext()
    fillPoly(ctx, [{ x: 1, y: 2 }, { x: 3, y: 4 }, { x: 5, y: 6 }], '#fill', '#edge', 2.5)

    expect(ctx.moveTo).toHaveBeenCalledWith(1, 2)
    expect(ctx.lineTo).toHaveBeenNthCalledWith(1, 3, 4)
    expect(ctx.lineTo).toHaveBeenNthCalledWith(2, 5, 6)
    expect(ctx.closePath).toHaveBeenCalledTimes(1)
    expect(ctx.fill).toHaveBeenCalledTimes(1)
    expect(ctx.stroke).toHaveBeenCalledTimes(1)
    expect(fillStyles).toEqual(['#fill'])
    expect(strokeStyles).toEqual(['#edge'])
    expect(lineWidths).toEqual([2.5])
  })

  it('drawBox 用投影后的六个角绘制三个可见面并返回几何边界', () => {
    const { ctx, fillStyles, strokeStyles } = recordingContext()
    const P = vi.fn(project)
    const result = drawBox(ctx, P, 2, 3, 4, 5, 7, 11, {
      left: '#left', right: '#right', top: '#top', edge: '#edge',
    })

    expect(result).toEqual({ fy: 8, fx: 6, Lt: project(2, 8, 18), Ct: project(6, 8, 18) })
    expect(P).toHaveBeenCalledWith(2, 8, 7)
    expect(P).toHaveBeenCalledWith(2, 3, 18)
    expect(fillStyles).toEqual(['#left', '#right', '#top'])
    expect(strokeStyles).toContain('#edge')
    expect(ctx.stroke).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['web', true, 4],
    ['router', false, 0],
    ['pc', true, 0],
    ['agent', false, 0],
    ['server', true, 4],
  ] satisfies Array<[EstateFigure, boolean, number]>)('%s 图例执行自身绘制语义', (figure, on, minimumArcs) => {
    const { ctx, fillStyles } = recordingContext()
    drawAssetFigure(ctx, project, assetNode(figure), on, '#asset-top')

    expect(ctx.beginPath).toHaveBeenCalled()
    expect(ctx.fill).toHaveBeenCalled()
    expect(vi.mocked(ctx.arc).mock.calls.length).toBeGreaterThanOrEqual(minimumArcs)
    if (figure === 'router' || figure === 'pc' || figure === 'server') expect(fillStyles).toContain('#asset-top')
  })

  it('内嵌资产画主机标识，P0 资产画红色顶面边界', () => {
    const { ctx, fillStyles, strokeStyles, lineWidths } = recordingContext()
    drawAssetFigure(ctx, project, assetNode('server', {
      accessMode: 'ACCESS_MODE_EMBEDDED',
      criticality: 'CRITICALITY_P0',
    }), true, '#asset-top')

    expect(fillStyles).toContain('#62e6a7')
    expect(strokeStyles).toContain('rgba(255,116,108,0.9)')
    expect(lineWidths).toContain(1.5)
    expect(ctx.closePath).toHaveBeenCalled()
  })

  it.each([
    ['brain', true, true, '#c4a574'],
    ['jarvis', true, true, '#3d8fd4'],
    ['jarvis', false, false, '#4a5560'],
    ['managed-agent', true, false, '#3d8fd4'],
    ['agentd', null, true, '#4a5560'],
  ] satisfies Array<[EstatePlaneNode['kind'], boolean | null, boolean, string]>)('%s 岗位依实况选择外形与状态色', (kind, live, on, expectedFill) => {
    const { ctx, fillStyles } = recordingContext()
    drawPlaneFigure(ctx, project, planeNode(kind, live), on)

    expect(ctx.beginPath).toHaveBeenCalled()
    expect(ctx.fill).toHaveBeenCalled()
    expect(fillStyles).toContain(expectedFill)
  })
})
