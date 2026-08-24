import type { ConsoleClient } from '../../api/client'
import type { ChatMessage } from '../../api/types'

const SessionSignalReferenceLimit = 32
const SessionSignalReadConcurrency = 4

type SessionSignalClient = Pick<ConsoleClient, 'getCase' | 'getApproval' | 'listCases'>

export interface SessionSignals {
  focusAssetIds: string[]
  pendingGate: boolean
}

// loadSessionSignals 重新读取会话附件引用的当前授权状态。
// 单类引用有界去重；不可读对象不泄露存在性，也不阻断其它有效信号。
export async function loadSessionSignals(client: SessionSignalClient, messages: ChatMessage[]): Promise<SessionSignals> {
  const caseRefs: string[] = []
  const approvalRefs: string[] = []
  const seenCases = new Set<string>()
  const seenApprovals = new Set<string>()
  for (let index = messages.length - 1; index >= 0; index--) {
    for (const attachment of messages[index].attachments ?? []) {
      if (
        caseRefs.length < SessionSignalReferenceLimit &&
        (attachment.kind === 'SESSION_ATTACHMENT_KIND_CASE' || attachment.kind === 'SESSION_ATTACHMENT_KIND_FINDING') &&
        !seenCases.has(attachment.refId)
      ) {
        seenCases.add(attachment.refId)
        caseRefs.push(attachment.refId)
      }
      if (
        approvalRefs.length < SessionSignalReferenceLimit &&
        (attachment.kind === 'SESSION_ATTACHMENT_KIND_APPROVAL' || attachment.kind === 'SESSION_ATTACHMENT_KIND_WORKER_CAPACITY') &&
        !seenApprovals.has(attachment.refId)
      ) {
        seenApprovals.add(attachment.refId)
        approvalRefs.push(attachment.refId)
      }
    }
    if (caseRefs.length >= SessionSignalReferenceLimit && approvalRefs.length >= SessionSignalReferenceLimit) break
  }

  const [cases, approvals, pendingCases] = await Promise.all([
    readBounded(caseRefs, (caseId) => client.getCase(caseId)),
    readBounded(approvalRefs, (approvalId) => client.getApproval(approvalId)),
    client.listCases({ state: 'INVESTIGATION_CASE_STATE_WAITING_EVIDENCE_APPROVAL' }, { pageSize: 1 }).catch(() => ({ items: [], nextPageToken: '' })),
  ])
  const focusAssetIds = new Set<string>()
  for (const item of cases) if (item.assetId !== '') focusAssetIds.add(item.assetId)
  for (const item of approvals) if (item.assetId !== '') focusAssetIds.add(item.assetId)
  return {
    focusAssetIds: [...focusAssetIds].sort(),
    pendingGate: pendingCases.items.length > 0 || approvals.some((approval) => approval.state === 'pending'),
  }
}

async function readBounded<Input, Output>(items: Input[], read: (item: Input) => Promise<Output>): Promise<Output[]> {
  const results: Array<Output | undefined> = new Array(items.length)
  let nextIndex = 0
  const workers = Array.from({ length: Math.min(SessionSignalReadConcurrency, items.length) }, async () => {
    while (nextIndex < items.length) {
      const index = nextIndex++
      try {
        results[index] = await read(items[index])
      } catch {
        // 不可读对象不泄露存在性，也不阻断同一批其它附件。
      }
    }
  })
  await Promise.all(workers)
  return results.filter((item): item is Output => item !== undefined)
}
