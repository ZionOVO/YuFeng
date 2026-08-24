// protojson 省略空字段时，normalize 必须补齐页面按必有字段读取的数组 / map / 枚举。

import { describe, expect, it } from 'vitest'
import {
  normalizeAssetDetail,
  normalizeDashboard,
  normalizeEvent,
  normalizeManagedAgentProfile,
  normalizeRelease,
  normalizeReleaseStats,
} from './normalize'

describe('normalizeEvent', () => {
  it('补齐省略的 detections / labels / releaseTraces / http 子字段', () => {
    const e = normalizeEvent({
      id: 'e1',
      occurredAt: '2026-08-18T00:00:00Z',
      assetId: 'local-1',
      source: 'yufeng-edge',
      kind: 'KIND_TRAFFIC',
      verdict: 'VERDICT_ALLOW',
      http: { method: 'GET', path: '/api/items', queryRedacted: 'id=' },
    })
    expect(e.detections).toEqual([])
    expect(e.labels).toEqual({})
    expect(e.releaseTraces).toEqual([])
    expect(e.http?.headersRedacted).toEqual({})
    expect(e.http?.bodyRedacted).toBe('')
    expect(e.http?.path).toBe('/api/items')
    expect(e.coverage).toEqual([])
    expect(e.triageReason).toBe('TRIAGE_REASON_UNSPECIFIED')
    expect(e.generationSeq).toBe('0')
    expect(e.wouldHaveBlocked).toBe(false)
  })

  it('空输入不抛，枚举回落到 UNSPECIFIED', () => {
    const e = normalizeEvent(undefined)
    expect(e.kind).toBe('KIND_UNSPECIFIED')
    expect(e.verdict).toBe('VERDICT_UNSPECIFIED')
    expect(e.detections).toEqual([])
  })

  it('保留检测键之外的证据与分类字段', () => {
	const e = normalizeEvent({
	  detections: [
		{
		  detectorId: 'crs',
		  ruleId: '942100',
		  rawTags: ['attack-sqli'],
		  taxonomyVersion: 'taxonomy-v2',
		  matchedVariable: 'ARGS:id',
		  evidenceSpan: 'query:3+8',
		  inspectionCoverageRef: 'query',
		},
	  ],
	})
	expect(e.detections[0]).toMatchObject({
	  rawTags: ['attack-sqli'],
	  taxonomyVersion: 'taxonomy-v2',
	  matchedVariable: 'ARGS:id',
	  evidenceSpan: 'query:3+8',
	  inspectionCoverageRef: 'query',
	})
  })
})

describe('normalizeAssetDetail', () => {
  it('补齐省略的 unitIds / labels / capabilities.packageManagers', () => {
    const d = normalizeAssetDetail({
      asset: {
        id: 'local-1',
        displayName: 'local-1',
        accessMode: 'ACCESS_MODE_NETWORK',
        capabilities: {},
        criticality: 'CRITICALITY_P2',
        maxAutoTier: 'TIER_L1_TRAFFIC',
      },
      activeReleaseCount: 4,
    })
    expect(d.unitIds).toEqual([])
    expect(d.units).toEqual([])
    expect(d.health).toBe('')
    expect(d.asset.labels).toEqual({})
    expect(d.asset.transports).toEqual([])
    expect(d.asset.capabilities?.packageManagers).toEqual([])
    expect(d.asset.criticality).toBe('CRITICALITY_P2')
  })

  it('保留边缘节点当前装载世代且补齐省略的序号', () => {
    const loaded = normalizeAssetDetail({
      units: [{ unitId: 'edge-1', kind: 'edge', currentGenerationId: 'generation-9', currentGenerationSeq: '9007199254740993' }],
    })
    const omitted = normalizeAssetDetail({ units: [{ unitId: 'edge-2', kind: 'edge' }] })

    expect(loaded.units[0]).toMatchObject({
      currentGenerationId: 'generation-9',
      currentGenerationSeq: '9007199254740993',
    })
    expect(omitted.units[0].currentGenerationSeq).toBe('0')
  })
})

describe('normalizeManagedAgentProfile', () => {
  it('保留服务端可管理判据并补齐裁剪响应中的 repeated 字段', () => {
    const profile = normalizeManagedAgentProfile({ agentId: 'profile-1', canManage: true })

    expect(profile.canManage).toBe(true)
    expect(profile.tools).toEqual([])
    expect(profile.bindings).toEqual([])
    expect(profile.state).toBe('AGENT_PROFILE_STATE_UNSPECIFIED')
  })
})

describe('normalizeRelease', () => {
  it('补齐省略的 retireReason 与 evidenceRefs', () => {
    const r = normalizeRelease({
      releaseId: 'rel_1',
      state: 'RELEASE_STATE_SHADOW',
      createdBy: 'jarvis-1',
      artifact: { id: 'sha256:ab', kind: 'KIND_UNSPECIFIED', payloadSchema: 'policy/v1' },
    })
    expect(r.retireReason).toBe('RETIRE_REASON_UNSPECIFIED')
    expect(r.artifact?.evidenceRefs).toEqual([])
    expect(r.artifact?.supersedes).toBe('')
  })
})

describe('normalizeReleaseStats', () => {
  it('补齐 protojson 省略的窗口计数和守护原因', () => {
    const stats = normalizeReleaseStats({
      releaseId: 'release-1',
      state: 'RELEASE_STATE_SHADOW',
      shadow: { duration: '300s', requests: '12' },
      guard: {},
    })

    expect(stats.shadow).toEqual({
      duration: '300s',
      requests: '12',
      blocks: '0',
      observes: '0',
      canarySelected: '0',
      denyFeedbackTotal: '0',
      upstream5xx: '0',
      p99Micros: '0',
    })
    expect(stats.guard?.lastBadReasons).toEqual([])
  })
})

describe('normalizeDashboard', () => {
  it('补齐省略的计数与状态分布', () => {
    const d = normalizeDashboard({ assetsTotal: '1', events24hTotal: '95' })
    expect(d.degradedUnits).toBe('0')
    expect(d.pendingRetireSoon).toBe('0')
    expect(d.releasesByState).toEqual({})
    expect(d.events24hBlocked).toBe('0')
  })
})
