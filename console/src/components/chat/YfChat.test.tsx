import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HeroUIProvider } from '@heroui/react'
import { MemoryRouter } from 'react-router-dom'
import type { ReactElement } from 'react'
import type { ChatMessage, InvestigationCase } from '../../api/types'
import { AuthProvider } from '../../auth/AuthContext'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { loginAs } from '../../test/renderApp'
import { YfChat, type YfChatProps } from './YfChat'

beforeEach(() => sessionStorage.clear())

function chatProps(overrides: Partial<YfChatProps> = {}): YfChatProps {
  return {
    mode: 'stage',
    messages: [],
    selfId: 'usr_01',
    contextLabel: '全部资产',
    thinking: false,
    busy: false,
    error: null,
    onSend: vi.fn(),
    ...overrides,
  }
}

function mount(ui: ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>)
}

function mountWithClient(ui: ReactElement, client: ConsoleClientFixture) {
  return render(
    <HeroUIProvider>
      <MemoryRouter>
        <AuthProvider client={client}>{ui}</AuthProvider>
      </MemoryRouter>
    </HeroUIProvider>,
  )
}

function message(sequence: string, sender: string, content: string, attachments?: ChatMessage['attachments']): ChatMessage {
  return { sequence, sessionId: 'session-1', sender, content, attachments }
}

describe('YfChat', () => {
  it('码头模式截断空白、忙碌和编排中提交，Enter 只发送剪裁后文本', async () => {
    const user = userEvent.setup()
    const onSend = vi.fn()
    const onClose = vi.fn()
    const props = chatProps({ mode: 'dock', online: true, onSend, onClose })
    const { rerender } = mount(<YfChat {...props} />)

    const dialog = screen.getByRole('dialog', { name: '智能体会话' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(within(dialog).getByText('在线')).toBeInTheDocument()
    const input = within(dialog).getByRole('textbox', { name: '向贾维斯发送消息' })

    fireEvent.change(input, { target: { value: '   ' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSend).not.toHaveBeenCalled()

    fireEvent.change(input, { target: { value: '  调查结算资产  ' } })
    fireEvent.keyDown(input, { key: 'Enter', shiftKey: true })
    expect(onSend).not.toHaveBeenCalled()
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSend).toHaveBeenCalledWith('调查结算资产')
    expect(input).toHaveValue('')

    rerender(<MemoryRouter><YfChat {...props} busy /></MemoryRouter>)
    const busyInput = screen.getByRole('textbox', { name: '向贾维斯发送消息' })
    fireEvent.change(busyInput, { target: { value: '不应发送' } })
    fireEvent.submit(busyInput.closest('form')!)
    expect(onSend).toHaveBeenCalledTimes(1)

    rerender(<MemoryRouter><YfChat {...props} thinking /></MemoryRouter>)
    fireEvent.change(screen.getByRole('textbox', { name: '向贾维斯发送消息' }), { target: { value: '仍不发送' } })
    fireEvent.submit(screen.getByRole('textbox', { name: '向贾维斯发送消息' }).closest('form')!)
    expect(onSend).toHaveBeenCalledTimes(1)
    await user.click(screen.getByRole('button', { name: '收起' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('区分自己与 Agent 消息，并把案件、审批、Shadow 与未知附件按契约呈现', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    const onApprovalDecided = vi.fn()
    const messages = [
      message('1', 'usr_01', '我的请求', [
        { kind: 'SESSION_ATTACHMENT_KIND_SHADOW_RELEASE', refId: 'rel_01J8VN8P', moduleId: 'traffic-interception' },
        { kind: 'SESSION_ATTACHMENT_KIND_RUN', refId: 'run-opaque', moduleId: 'traffic-interception' },
      ]),
      message('2', 'jarvis', '调查进展', [
        { kind: 'SESSION_ATTACHMENT_KIND_CASE', refId: 'case_traffic_01', moduleId: 'traffic-interception' },
        { kind: 'SESSION_ATTACHMENT_KIND_APPROVAL', refId: 'approval_evidence_01', moduleId: 'traffic-interception' },
      ]),
      message('3', '', '兼容无 sender 的自己消息'),
    ]

    mountWithClient(<YfChat {...chatProps({ messages, online: null, thinking: true, error: '会话暂不可用', onApprovalDecided })} />, client)
    const caseTitle = await screen.findByText('结算入口出现未映射请求形状')
    expect(await screen.findByRole('region', { name: '证据审批' })).toBeInTheDocument()

    expect(screen.getByRole('region', { name: '智能体会话' })).toBeInTheDocument()
    expect(screen.getByText('状态未知')).toBeInTheDocument()
    expect(screen.getAllByText('你')).toHaveLength(2)
    expect(screen.getAllByText('贾维斯')).toHaveLength(2)
    expect(screen.getByText('编排中')).toBeInTheDocument()
    expect(screen.getByText('会话暂不可用')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '查看影子防护策略' })).toHaveAttribute('href', '/releases/rel_01J8VN8P')
    expect(screen.getByText(/SESSION_ATTACHMENT_KIND_RUN/)).toHaveTextContent('run-opaque')
    await user.click(caseTitle.closest('button')!)
  })

  it('案件附件读取失败时显式呈现重试并重新发起请求', async () => {
    class FailedCaseClient extends ConsoleClientFixture {
      override async getCase(): Promise<InvestigationCase> {
        throw new Error('case unavailable')
      }
    }

    const user = userEvent.setup()
    const client = new FailedCaseClient()
    const getCase = vi.spyOn(client, 'getCase')
    await loginAs(client)
    mountWithClient(<YfChat {...chatProps({ messages: [message('1', 'jarvis', '案件附件', [
      { kind: 'SESSION_ATTACHMENT_KIND_FINDING', refId: 'case_traffic_01', moduleId: 'traffic-interception' },
    ])] })} />, client)

    expect(await screen.findByText('案件附件当前不可读。')).toBeInTheDocument()
    expect(getCase).toHaveBeenCalledTimes(1)
    await user.click(screen.getByRole('button', { name: '重试' }))
    await waitFor(() => expect(getCase).toHaveBeenCalledTimes(2))
  })
})
