import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '../../api/errors'
import type { EventDetail } from '../../api/types'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { createEvents, FIXTURE_BLOCK_EVENT_ID, FIXTURE_BLOCK_EVENT_RELEASE_ID } from '../../test/fixtures/data'
import { loginAs, renderApp } from '../../test/renderApp'

beforeEach(() => sessionStorage.clear())

describe('事件详情状态与误报门禁', () => {
  it.each([
    ['permission_denied', '没有权限'],
    ['not_found', '事件不存在'],
    ['unavailable', 'event service unavailable'],
  ] as const)('GetEvent 返回 %s 时区分详情状态', async (code, expected) => {
    class FailedEventClient extends ConsoleClientFixture {
      override async getEvent(): Promise<EventDetail> {
        throw new ApiError({ code, message: 'event service unavailable', httpStatus: code === 'not_found' ? 404 : 503 })
      }
    }

    const client = new FailedEventClient()
    await loginAs(client)
    renderApp({ route: '/events/missing-event', client })
    expect(await screen.findByText(expected)).toBeInTheDocument()
  })

  it('完整呈现脱敏负载、检查覆盖度、检测键与发布轨迹', async () => {
    const rich = structuredClone(createEvents()[0])
    rich.kind = 'KIND_INTEL'
    rich.verdict = 'VERDICT_OBSERVE'
    rich.labels = { environment: 'production' }
    rich.generationId = ''
    rich.generationSeq = '9007199254740993'
    rich.trafficKey = ''
    rich.wouldHaveBlocked = false
    rich.http = {
      method: 'POST',
      path: '/v1/checkout',
      queryRedacted: 'token=%5BREDACTED%5D',
      headersRedacted: { authorization: '[REDACTED]' },
      bodyRedacted: 'eyJyZWRhY3RlZCI6dHJ1ZX0=',
      srcPseudonym: 'source-pseudonym',
      dst: '10.0.0.8:443',
      statusCode: 403,
      latencyMicros: '12500',
    }
    rich.ai = {
      provider: 'model-gateway',
      model: 'security-model',
      roleCounts: { system: 1, user: 2 },
      toolCalls: [{ name: 'case.get', argsDigest: 'sha256:args' }],
    }
    rich.coverage = [
      { target: 'INSPECTION_SURFACE_QUERY', status: 'COVERAGE_STATUS_FULL', inspectedBytes: '24', totalBytesKnown: '24' },
      { target: 'INSPECTION_SURFACE_BODY', status: 'COVERAGE_STATUS_PARTIAL', inspectedBytes: '1024', totalBytesKnown: '' },
    ]
    rich.detections = [{
      detectorId: 'coraza',
      ruleId: '942100',
      confidence: 0.875,
      message: '匹配注入形状',
      tier: 'TIER_UNSPECIFIED',
      attackClass: 'ATTACK_CLASS_SQLI',
      taxonomyVersion: 'owasp-crs-4',
      matchedVariable: 'ARGS:id',
      evidenceSpan: '12:24',
      inspectionCoverageRef: 'INSPECTION_SURFACE_QUERY',
      rawTags: ['attack-sqli', 'paranoia-level/1'],
    }]
    rich.releaseTraces = [{
      releaseId: 'release-unknown-mode',
      artifactId: 'sha256:artifact',
      mode: 'RELEASE_MODE_UNSPECIFIED',
      canaryPercent: 0,
      canarySelected: false,
      matched: false,
    }]

    class RichEventClient extends ConsoleClientFixture {
      override async getEvent(): Promise<EventDetail> {
        return structuredClone({
          event: rich,
          modelInferences: [{
            inferenceId: 'inference-rich',
            eventId: rich.id,
            modelGroup: 'http-threat',
            modelType: 'PVM',
            modelVersion: 'gpvm-e9eceef3',
            threshold: 0.9,
            score: 0.9731,
            attackClass: 'ATTACK_CLASS_SQLI',
            taxonomyVersion: 'http-threat/v1',
            recordedAt: rich.occurredAt,
            modelProfileDigest: 'sha256:model-profile',
            requestId: rich.requestId,
            resultKind: 'MODEL_RESULT_KIND_ALERT',
          }],
          triageDeliveries: [{
            caseId: 'case_traffic_01',
            instructionId: 'instruction-rich',
            handlerId: 'jarvis',
            kind: 'INSTRUCTION_KIND_EVENT_TRIAGE',
            status: 'INSTRUCTION_STATUS_ACKNOWLEDGED',
            createdAt: rich.occurredAt,
            acknowledgedAt: rich.occurredAt,
          }],
        })
      }
    }

    const client = new RichEventClient()
    await loginAs(client)
    renderApp({ route: `/events/${rich.id}`, client })

    expect(await screen.findByRole('region', { name: 'HTTP 载荷' })).toBeInTheDocument()
    expect(screen.getByText('token=%5BREDACTED%5D')).toBeInTheDocument()
    expect(screen.getByText('eyJyZWRhY3RlZCI6dHJ1ZX0=')).toBeInTheDocument()
    expect(screen.getByRole('table', { name: '脱敏请求头' })).toHaveTextContent('authorization')
    expect(screen.getByRole('region', { name: '人工智能载荷' })).toHaveTextContent('system=1')
    expect(screen.getByText('case.get')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: '检查覆盖度' })).toHaveTextContent('1024 / 未知 bytes')
    expect(screen.getByRole('region', { name: '检测结论' })).toHaveTextContent('taxonomy=owasp-crs-4')
    expect(screen.getByText('tags=attack-sqli,paranoia-level/1')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: '发布轨迹' })).toHaveTextContent('未选中')
    expect(screen.getByRole('region', { name: '模型推理' })).toHaveTextContent('gpvm-e9eceef3')
    expect(screen.getByRole('region', { name: '模型推理' })).toHaveTextContent('0.9731 / 0.9000')
    expect(screen.getByRole('region', { name: '研判交付' })).toHaveTextContent('instruction-rich')
    expect(screen.getByRole('link', { name: 'case_traffic_01' })).toHaveAttribute('href', '/cases?caseId=case_traffic_01')
    expect(screen.queryByRole('button', { name: '举报误报' })).toBeNull()
  })

  it('没有资产级误报授权时不渲染举报入口', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: `/events/${FIXTURE_BLOCK_EVENT_ID}`, client })

    expect(await screen.findByRole('region', { name: '发布轨迹' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '举报误报' })).toBeNull()
    expect(screen.getByText(/BLOCK 事件可举报误报/)).toBeInTheDocument()
  })

  it('具备资产级授权时校验说明长度，失败留在原对话框并可重试成功', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client, 'promoter-wu', 'promoter123456')
    const denyFeedback = vi.spyOn(client, 'denyFeedback').mockRejectedValueOnce(
      new ApiError({ code: 'unavailable', message: 'feedback service unavailable', httpStatus: 503 }),
    )
    renderApp({ route: `/events/${FIXTURE_BLOCK_EVENT_ID}`, client })

    await user.click(await screen.findByRole('button', { name: '举报误报' }))
    const dialog = screen.getByRole('dialog')
    const submit = within(dialog).getByRole('button', { name: '确认举报' })
    expect(submit).toBeDisabled()

    const note = within(dialog).getByRole('textbox', { name: '举报说明' })
    fireEvent.change(note, { target: { value: 'x'.repeat(2001) } })
    expect(await within(dialog).findByText('说明不能超过 2000 字')).toBeInTheDocument()
    expect(submit).toBeDisabled()

    fireEvent.change(note, { target: { value: '  规则误伤正常请求  ' } })
    await user.click(submit)
    expect(await within(dialog).findByText('feedback service unavailable（unavailable）')).toBeInTheDocument()
    expect(denyFeedback).toHaveBeenLastCalledWith(FIXTURE_BLOCK_EVENT_RELEASE_ID, FIXTURE_BLOCK_EVENT_ID, '规则误伤正常请求')

    await user.click(submit)
    expect(await within(dialog).findByText('已提交举报，将计入该发布的误报计数')).toBeInTheDocument()
    expect(denyFeedback).toHaveBeenCalledTimes(2)
    await user.click(within(dialog).getByRole('button', { name: '关闭' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })
})
