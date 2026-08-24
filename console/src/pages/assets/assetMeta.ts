import type { AccessMode, Criticality, Tier } from '../../api/types'

export const ACCESS_MODE_LABEL: Record<AccessMode, string> = {
  ACCESS_MODE_UNSPECIFIED: '未知',
  ACCESS_MODE_EMBEDDED: '在机',
  ACCESS_MODE_REMOTE: '远程',
  ACCESS_MODE_NETWORK: '旁路',
}

export const CRITICALITY_BADGE: Record<Criticality, { label: string; tone: 'red' | 'amber' | 'mute' }> = {
  CRITICALITY_UNSPECIFIED: { label: '未知', tone: 'mute' },
  CRITICALITY_P0: { label: 'P0', tone: 'red' },
  CRITICALITY_P1: { label: 'P1', tone: 'amber' },
  CRITICALITY_P2: { label: 'P2', tone: 'mute' },
}

export const TIER_LABEL: Record<Tier, string> = {
  TIER_UNSPECIFIED: '未知',
  TIER_L0_REPORT: '仅报告 L0',
  TIER_L1_TRAFFIC: '流量拦截 L1',
  TIER_L2_RUNTIME: '运行时约束 L2',
  TIER_L3_COLD_PATCH: '冷补丁 L3',
}

export const EDITABLE_CRITICALITIES: Criticality[] = ['CRITICALITY_P0', 'CRITICALITY_P1', 'CRITICALITY_P2']
export const EDITABLE_TIERS: Tier[] = ['TIER_L0_REPORT', 'TIER_L1_TRAFFIC', 'TIER_L2_RUNTIME', 'TIER_L3_COLD_PATCH']
export const EDITABLE_ACCESS_MODES: AccessMode[] = ['ACCESS_MODE_NETWORK', 'ACCESS_MODE_REMOTE', 'ACCESS_MODE_EMBEDDED']
