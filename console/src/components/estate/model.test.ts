import { describe, expect, it } from 'vitest'
import type { AssetDetail } from '../../api/types'
import {
  MIN_NODE_GAP,
  boxesSeparated,
  buildEstate,
  estateBoxes,
  groupKeyOf,
  pickAssetFigure,
  pickWeakGroupSource,
} from './model'

function asset(partial: {
  id: string
  name: string
  mode?: AssetDetail['asset']['accessMode']
  labels?: Record<string, string>
  unitIds?: string[]
  health?: string
}): AssetDetail {
  return {
    asset: {
      id: partial.id,
      displayName: partial.name,
      accessMode: partial.mode ?? 'ACCESS_MODE_NETWORK',
      transports: [],
      criticality: 'CRITICALITY_P2',
      maxAutoTier: 'TIER_L0_REPORT',
      labels: partial.labels ?? {},
    },
    unitIds: partial.unitIds ?? [],
    units: [],
    health: partial.health ?? 'UNIT_HEALTH_UNSPECIFIED',
    activeReleaseCount: 0,
  }
}

describe('boxesSeparated', () => {
  const a = { id: 'a', gx: 0, gy: 0, sx: 1, sy: 1 }

  it('贴边或重叠不算隔开', () => {
    expect(boxesSeparated(a, { id: 'b', gx: 1, gy: 0, sx: 1, sy: 1 })).toBe(false)
    expect(boxesSeparated(a, { id: 'b', gx: 0.2, gy: 0.2, sx: 1, sy: 1 })).toBe(false)
  })

  it('轴向空隙达到最小间距才算隔开', () => {
    expect(boxesSeparated(a, { id: 'b', gx: 1 + MIN_NODE_GAP, gy: 0, sx: 1, sy: 1 })).toBe(true)
    expect(boxesSeparated(a, { id: 'b', gx: 0, gy: 1 + MIN_NODE_GAP, sx: 1, sy: 1 })).toBe(true)
  })
})

describe('pickWeakGroupSource', () => {
  it('有 biz 就按业务线，不看 env', () => {
    expect(
      pickWeakGroupSource([
        asset({ id: 'a', name: 'a', labels: { env: 'prod', biz: 'pay' } }),
        asset({ id: 'b', name: 'b', labels: { env: 'prod' } }),
      ]),
    ).toBe('biz')
  })

  it('没有 biz、有 env 时按环境', () => {
    expect(pickWeakGroupSource([asset({ id: 'a', name: 'a', labels: { env: 'staging' } })])).toBe('env')
  })

  it('标签都空则不分组', () => {
    expect(pickWeakGroupSource([asset({ id: 'a', name: 'a' })])).toBe('none')
  })
})

describe('buildEstate', () => {
  const sample = [
    asset({ id: 'asset-01', name: 'core-payments', mode: 'ACCESS_MODE_EMBEDDED', labels: { env: 'prod', biz: 'payments' }, unitIds: ['unit-edge-01'], health: 'UNIT_HEALTH_HEALTHY' }),
    asset({ id: 'asset-02', name: 'mall-gateway', mode: 'ACCESS_MODE_EMBEDDED', labels: { env: 'prod', biz: 'mall' }, unitIds: ['unit-edge-02'] }),
    asset({ id: 'asset-03', name: 'legacy-erp', labels: { env: 'staging', biz: 'erp' }, unitIds: [] }),
  ]

  it('绑定边只来自 unitIds，未绑定资产没有保护边', () => {
    const g = buildEstate(sample)
    const binds = g.edges.filter((e) => e.kind === 'bind').sort((a, b) => (a.assetId ?? '').localeCompare(b.assetId ?? ''))
    expect(binds).toEqual([
      { fromId: 'unit-edge-01', toId: 'asset-01', kind: 'bind', unitId: 'unit-edge-01', assetId: 'asset-01' },
      { fromId: 'unit-edge-02', toId: 'asset-02', kind: 'bind', unitId: 'unit-edge-02', assetId: 'asset-02' },
    ])
    expect(g.units.map((u) => u.id).sort()).toEqual(['unit-edge-01', 'unit-edge-02'])
  })

  it('弱分组用 labels.biz 的字面值，不发明城市岛名', () => {
    const g = buildEstate(sample)
    expect(g.source).toBe('biz')
    expect(g.groups.map((x) => x.key)).toEqual(['erp', 'mall', 'payments'])
    expect(g.groups.some((x) => x.label.includes('域') || x.label.includes('岛'))).toBe(false)
  })

  it('同一输入坐标稳定', () => {
    const a = buildEstate(sample)
    const b = buildEstate(sample)
    expect(a.assets.map((n) => [n.id, n.gx, n.gy])).toEqual(b.assets.map((n) => [n.id, n.gx, n.gy]))
  })

  it('空资产仍保留中台与 Agent 岗位', () => {
    const g = buildEstate([])
    expect(g.assets).toEqual([])
    expect(g.units).toEqual([])
    expect(g.source).toBe('none')
    expect(g.plane.map((p) => p.kind)).toEqual(['jarvis', 'brain'])
    expect(g.edges.filter((e) => e.kind === 'bind')).toEqual([])
    expect(g.edges.filter((e) => e.kind === 'control').length).toBe(1)
  })

  it('贾维斯与其它 Agent 只连中台，不连资产', () => {
    const g = buildEstate(sample, {
      jarvisOnline: true,
      edgeReady: true,
      workers: [{
        workerId: 'agentd-central',
        workerKind: 'WORKER_KIND_INVESTIGATION',
        version: 'v1',
        operatingSystem: 'linux',
        architecture: 'amd64',
        sandboxCapabilities: ['landlock', 'seccomp'],
        investigationEligible: false,
        missingSandboxCapabilities: ['resource_limits'],
        maxConcurrency: 1,
      }],
    })
    const jarvis = g.plane.find((p) => p.kind === 'jarvis')
    const brain = g.plane.find((p) => p.kind === 'brain')
    expect(jarvis?.live).toBe(true)
    expect(brain?.live).toBe(true)
    expect(g.plane.find((p) => p.id === 'worker:agentd-central')?.live).toBeNull()
    expect(g.plane.some((p) => p.kind === 'agentd')).toBe(true)
    const fromJarvis = g.edges.filter((e) => e.fromId === 'plane:jarvis' || e.toId === 'plane:jarvis')
    expect(fromJarvis).toEqual([{ fromId: 'plane:jarvis', toId: 'plane:brain', kind: 'control' }])
    expect(g.edges.some((e) => e.kind === 'control' && (e.toId.startsWith('asset-') || e.fromId.startsWith('asset-')))).toBe(false)
    expect(g.edges.filter((e) => e.kind === 'register').map((e) => e.fromId).sort()).toEqual(['unit-edge-01', 'unit-edge-02'])
    expect(g.edges.filter((e) => e.kind === 'local')).toEqual([])
  })

  it('未探测时贾维斯标离线且不虚构本机监督器边', () => {
    const g = buildEstate(sample)
    expect(g.plane.find((p) => p.kind === 'jarvis')?.live).toBe(false)
    expect(g.edges.filter((e) => e.kind === 'local')).toEqual([])
  })

  it('无标签时整图一组，key 为空', () => {
    const g = buildEstate([asset({ id: 'x', name: 'solo' })])
    expect(g.source).toBe('none')
    expect(g.groups).toEqual([{ key: '', label: '全部', assetIds: ['x'] }])
    expect(groupKeyOf(asset({ id: 'x', name: 'solo' }), 'none')).toBe('')
  })

  it('资产、单元与岗位的包围盒至少隔开最小间距', () => {
    const many = Array.from({ length: 8 }, (_, i) =>
      asset({
        id: `asset-${String(i + 1).padStart(2, '0')}`,
        name: i % 3 === 0 ? `mall-gateway-${i}` : i % 3 === 1 ? `edge-router-${i}` : `core-${i}`,
        labels: { biz: `line-${i % 3}` },
        unitIds: [`unit-${i}`],
      }),
    )
    const g = buildEstate(many)
    const boxes = estateBoxes(g)
    expect(boxes.length).toBeGreaterThan(8)
    for (let i = 0; i < boxes.length; i++) {
      for (let j = i + 1; j < boxes.length; j++) {
        expect(boxesSeparated(boxes[i], boxes[j], MIN_NODE_GAP), `${boxes[i].id} 贴着 ${boxes[j].id}`).toBe(true)
      }
    }
  })

  it('名称或 labels.figure 决定图例外形，不是账本种类', () => {
    expect(pickAssetFigure(asset({ id: 'a', name: 'mall-gateway' }))).toBe('web')
    expect(pickAssetFigure(asset({ id: 'b', name: 'edge-router-01' }))).toBe('router')
    expect(pickAssetFigure(asset({ id: 'c', name: 'dev-laptop' }))).toBe('pc')
    expect(pickAssetFigure(asset({ id: 'd', name: 'jarvis-assistant' }))).toBe('agent')
    expect(pickAssetFigure(asset({ id: 'e', name: 'core-payments' }))).toBe('server')
    expect(pickAssetFigure(asset({ id: 'f', name: 'anything', labels: { figure: 'pc' } }))).toBe('pc')
  })
})
