import type { ApprovalView, ChatMessage, InvestigationCase } from '../../api/types'
import { loadSessionSignals } from './jarvisSignals'

const messages: ChatMessage[] = [
  {
    sequence: '1',
    sessionId: 'session-1',
    sender: 'jarvis-1',
    content: '案件等待审批',
    attachments: [
      { kind: 'SESSION_ATTACHMENT_KIND_CASE', refId: 'case-1', moduleId: 'traffic-interception' },
      { kind: 'SESSION_ATTACHMENT_KIND_APPROVAL', refId: 'approval-1', moduleId: 'traffic-interception' },
      { kind: 'SESSION_ATTACHMENT_KIND_CASE', refId: 'case-denied', moduleId: 'traffic-interception' },
    ],
  },
]

describe('贾维斯会话真实状态信号', () => {
  it('从案件和审批引用重新读取资产焦点与待审批状态', async () => {
    const signals = await loadSessionSignals(
      {
        getCase: async (caseId) => {
          if (caseId === 'case-denied') throw new Error('permission denied')
          return { caseId, assetId: 'asset-1' } as InvestigationCase
        },
        getApproval: async (approvalId) => ({ approvalId, assetId: 'asset-2', state: 'pending' }) as ApprovalView,
        listCases: async () => ({ items: [], nextPageToken: '' }),
      },
      messages,
    )

    expect(signals).toEqual({ focusAssetIds: ['asset-1', 'asset-2'], pendingGate: true })
  })

  it('审批决定后不再显示待审批徽标', async () => {
    const signals = await loadSessionSignals(
      {
        getCase: async (caseId) => ({ caseId, assetId: 'asset-1' }) as InvestigationCase,
        getApproval: async (approvalId) => ({ approvalId, assetId: 'asset-1', state: 'approved' }) as ApprovalView,
        listCases: async () => ({ items: [], nextPageToken: '' }),
      },
      messages,
    )

    expect(signals.pendingGate).toBe(false)
  })

  it('新会话也能从案件列表发现已有待审批', async () => {
    const signals = await loadSessionSignals(
      {
        getCase: async (caseId) => ({ caseId, assetId: 'asset-1' }) as InvestigationCase,
        getApproval: async (approvalId) => ({ approvalId, assetId: 'asset-1', state: 'approved' }) as ApprovalView,
        listCases: async () => ({ items: [{ caseId: 'existing' } as InvestigationCase], nextPageToken: '' }),
      },
      [],
    )

    expect(signals.pendingGate).toBe(true)
  })

  it('有界并发回读较多案件附件', async () => {
    let active = 0
    let peak = 0
    const manyCases: ChatMessage[] = [{
      sequence: '2',
      sessionId: 'session-1',
      sender: 'jarvis-1',
      content: '案件汇总',
      attachments: Array.from({ length: 12 }, (_, index) => ({
        kind: 'SESSION_ATTACHMENT_KIND_CASE',
        refId: `case-${index}`,
        moduleId: 'traffic-interception',
      })),
    }]

    const signals = await loadSessionSignals(
      {
        getCase: async (caseId) => {
          active++
          peak = Math.max(peak, active)
          await new Promise((resolve) => window.setTimeout(resolve, 1))
          active--
          return { caseId, assetId: `asset-${caseId}` } as InvestigationCase
        },
        getApproval: async (approvalId) => ({ approvalId, state: 'approved' }) as ApprovalView,
        listCases: async () => ({ items: [], nextPageToken: '' }),
      },
      manyCases,
    )

    expect(peak).toBeLessThanOrEqual(4)
    expect(signals.focusAssetIds).toHaveLength(12)
  })
})
