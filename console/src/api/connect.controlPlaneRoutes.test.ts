import { describe, expect, it, vi } from 'vitest'
import { ConnectClient } from './connect'

function responseFor(path: string): Response {
  if (path.endsWith('/GetCase')) return Response.json({ case: { caseId: 'case-1', assetId: 'asset-1' } })
  if (path.endsWith('/ResolveCase') || path.endsWith('/ReopenCase') || path.endsWith('/RecordCaseFeedback')) return Response.json({ case: { caseId: 'case-1', assetId: 'asset-1' } })
  if (path.endsWith('/PollCaseActivities')) return Response.json({ activities: [], nextAfterSequence: '0' })
  if (path.endsWith('/GetApproval')) return Response.json({ approval: { approvalId: 'approval-1', maxBytes: '4096' } })
  if (path.endsWith('/DecideApproval')) return Response.json({ approvalId: 'approval-1', state: 'approved' })
  if (path.endsWith('/CreateAgentProfile') || path.endsWith('/UpdateAgentProfile')) {
    return Response.json({ profile: { agentId: 'agent-1', displayName: '审查 Agent', tools: [], bindings: [] } })
  }
  if (path.endsWith('/DecideWorkerEnrollment')) {
    return Response.json({ enrollmentId: 'enrollment-1', state: 'approved' })
  }
  if (path.endsWith('/GetTrafficReviewPolicy') || path.endsWith('/UpdateTrafficReviewPolicy')) {
    return Response.json({ status: { policy: { mode: 'TRAFFIC_REVIEW_MODE_STATISTICS_ONLY' }, generationId: 'generation-1', generationSeq: '1' } })
  }
  if (path.endsWith('/GetModelIngressWindow') || path.endsWith('/UpdateModelIngressWindow')) {
    return Response.json({
      status: {
        assetId: 'asset-1',
        unitId: 'edge-1',
        desired: { maxItems: 4096, maxRetainedBytes: '134217728', maxQueueAge: '2s' },
        desiredListenPlanVersion: '2',
        appliedListenPlanVersion: '1',
        state: 'MODEL_INGRESS_WINDOW_STATE_CONVERGING',
      },
    })
  }
  return Response.json({})
}

describe('ConnectClient 控制面真实路由', () => {
  it('案件、审批、模块、Agent 与 worker 方法只调用 Brain 注册的服务', async () => {
    const paths: string[] = []
    const fetchFn = vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      paths.push(path)
      return responseFor(path)
    })
    const client = new ConnectClient({
      getToken: () => 'session-token',
      onUnauthenticated: () => undefined,
      fetchFn: fetchFn as unknown as typeof fetch,
    })

    await client.listCases({ assetId: 'asset-1' })
    await client.getCase('case-1')
    await client.pollCaseActivities({ caseId: 'case-1', longPollSeconds: 0 })
    await client.resolveCase({ caseId: 'case-1', resolution: 'CASE_RESOLUTION_BENIGN' })
    await client.reopenCase({ caseId: 'case-1' })
    await client.recordCaseFeedback({ caseId: 'case-1', resolution: 'CASE_RESOLUTION_FALSE_POSITIVE' })
    await client.getApproval('approval-1')
    await client.decideApproval({ approvalId: 'approval-1', approved: true })
    await client.listModules()
    await client.listAgentProfiles()
    await client.createAgentProfile({ displayName: '审查 Agent', tools: ['case.get'], bindings: [{ kind: 'asset', id: 'asset-1' }] })
    await client.updateAgentProfile({
      agentId: 'agent-1',
      displayName: '审查 Agent',
      state: 'AGENT_PROFILE_STATE_ENABLED',
      tools: ['case.get'],
      bindings: [{ kind: 'asset', id: 'asset-1' }],
    })
    await client.deleteAgentProfile('agent-1')
    await client.batchUpdateAgentProfiles({ agentIds: ['agent-1'], tools: ['case.get'], bindings: [{ kind: 'asset', id: 'asset-1' }] })
    await client.listWorkers()
    await client.listWorkerEnrollments('pending')
    await client.decideWorkerEnrollment({ enrollmentId: 'enrollment-1', approved: true, bindings: ['asset-1'] })
    await client.getTrafficReviewPolicy('asset-1')
    await client.updateTrafficReviewPolicy('asset-1', 'TRAFFIC_REVIEW_MODE_STATISTICS_ONLY', 'generation-0')
    await client.getModelIngressWindow('asset-1', 'edge-1')
    await client.updateModelIngressWindow(
      'asset-1',
      'edge-1',
      { maxItems: 4096, maxRetainedBytes: '134217728', maxQueueAge: '2s' },
      '1',
    )

    expect(paths).toEqual([
      '/yufeng.case.v1.CaseService/ListCases',
      '/yufeng.case.v1.CaseService/GetCase',
      '/yufeng.case.v1.CaseService/PollCaseActivities',
      '/yufeng.case.v1.CaseService/ResolveCase',
      '/yufeng.case.v1.CaseService/ReopenCase',
      '/yufeng.case.v1.CaseService/RecordCaseFeedback',
      '/yufeng.agent.v1.AgentInteractionService/GetApproval',
      '/yufeng.agent.v1.AgentInteractionService/DecideApproval',
      '/yufeng.module.v1.ModuleCatalogService/ListModules',
      '/yufeng.agent.v1.AgentProfileService/ListAgentProfiles',
      '/yufeng.agent.v1.AgentProfileService/CreateAgentProfile',
      '/yufeng.agent.v1.AgentProfileService/UpdateAgentProfile',
      '/yufeng.agent.v1.AgentProfileService/DeleteAgentProfile',
      '/yufeng.agent.v1.AgentProfileService/BatchUpdateAgentProfiles',
      '/yufeng.worker.v1.WorkerService/ListWorkers',
      '/yufeng.worker.v1.WorkerService/ListWorkerEnrollments',
      '/yufeng.worker.v1.WorkerService/DecideWorkerEnrollment',
      '/yufeng.asset.v1.AssetService/GetTrafficReviewPolicy',
      '/yufeng.asset.v1.AssetService/UpdateTrafficReviewPolicy',
      '/yufeng.asset.v1.AssetService/GetModelIngressWindow',
      '/yufeng.asset.v1.AssetService/UpdateModelIngressWindow',
    ])
  })
})
