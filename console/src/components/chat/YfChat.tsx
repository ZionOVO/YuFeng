// 贾维斯对话台：只画 SessionService 的 UTF-8 content。
// 两种形态共用：dock 全局展开 / stage 拓扑台面。不把未登记信封当成工具/实例/待盖印。

import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent, type Ref } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import type { ChatMessage, SessionAttachment } from '../../api/types'
import { useAuth } from '../../auth/useAuth'
import { CaseCard, EvidenceApprovalCard } from '../cases/CaseCards'
import { useAsyncData } from '../useAsyncData'

export type YfChatMode = 'dock' | 'stage'

export interface YfChatProps {
  mode: YfChatMode
  messages: ChatMessage[]
  selfId: string
  contextLabel: string
  thinking: boolean
  busy: boolean
  error: string | null
  online?: boolean | null
  onSend: (text: string) => void
  onClose?: () => void
  onApprovalDecided?: () => void
  rootRef?: Ref<HTMLDivElement>
}

function CaseAttachment({ caseId }: { caseId: string }) {
  const { client } = useAuth()
  const navigate = useNavigate()
  const item = useAsyncData(() => client.getCase(caseId), [client, caseId], false)
  if (item.status === 'error') return <div className="rounded-lg border border-[#704044] p-3 text-xs text-[#ff8c81]">案件附件当前不可读。<button type="button" className="ml-2 underline" onClick={item.reload}>重试</button></div>
  if (item.data === null) return <div className="rounded-lg border border-[#294147] p-3 text-xs text-[#8fa2a5]">正在读取案件附件…</div>
  return <CaseCard item={item.data} onSelect={() => navigate(`/cases?caseId=${caseId}`)} />
}

function MessageAttachments({ attachments = [], onApprovalDecided }: { attachments?: SessionAttachment[]; onApprovalDecided?: () => void }) {
  if (attachments.length === 0) return null
  return (
    <div className="mt-2 grid gap-2">
      {attachments.map((attachment) => {
        const key = `${attachment.kind}:${attachment.refId}`
        if (attachment.kind === 'SESSION_ATTACHMENT_KIND_CASE' || attachment.kind === 'SESSION_ATTACHMENT_KIND_FINDING') return <CaseAttachment key={key} caseId={attachment.refId} />
        if (attachment.kind === 'SESSION_ATTACHMENT_KIND_APPROVAL' || attachment.kind === 'SESSION_ATTACHMENT_KIND_WORKER_CAPACITY') return <EvidenceApprovalCard key={key} approvalId={attachment.refId} onDecided={onApprovalDecided} />
        if (attachment.kind === 'SESSION_ATTACHMENT_KIND_SHADOW_RELEASE') return <Link key={key} className="rounded-lg border border-[#294147] p-3 text-xs text-[#62e6a7] underline" to={`/releases/${attachment.refId}`}>查看影子防护策略</Link>
        return <div key={key} className="rounded-lg border border-[#294147] p-3 text-xs text-[#8fa2a5]">{attachment.kind} · <span className="fs-mono">{attachment.refId}</span></div>
      })}
    </div>
  )
}

function AgentTurn({ message, onApprovalDecided }: { message: ChatMessage; onApprovalDecided?: () => void }) {
  return (
    <div className="yf-turn yf-turn--agent">
      <article className="yf-msg yf-msg--agent">
        <div className="yf-msg-meta">贾维斯</div>
        <div className="yf-bubble">{message.content}<MessageAttachments attachments={message.attachments} onApprovalDecided={onApprovalDecided} /></div>
      </article>
    </div>
  )
}

export function YfChat({ mode, messages, selfId, contextLabel, thinking, busy, error, online = null, onSend, onClose, onApprovalDecided, rootRef }: YfChatProps) {
  const [draft, setDraft] = useState('')
  const threadRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    const el = threadRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [messages, thinking])

  const submit = (e?: FormEvent) => {
    e?.preventDefault()
    const q = draft.trim()
    if (q === '' || busy || thinking) return
    onSend(q)
    setDraft('')
  }

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      submit()
    }
  }

  return (
    <div ref={rootRef} tabIndex={mode === 'dock' ? -1 : undefined} className={`yf-chat yf-chat--${mode}`} role={mode === 'dock' ? 'dialog' : 'region'} aria-modal={mode === 'dock' ? true : undefined} aria-label="智能体会话">
      <header className="yf-chat-head">
        <div className="yf-chat-who">
          <p className="yf-chat-kicker">{mode === 'stage' ? '编排' : '全局'}</p>
          <div className="yf-chat-name-row">
            <p className="yf-chat-name">贾维斯</p>
            <i className={`yf-live yf-live--${online === true ? 'online' : online === false ? 'offline' : 'unknown'}`} aria-hidden />
            <span className="sr-only">{online === true ? '在线' : online === false ? '离线' : '状态未知'}</span>
          </div>
          <p className="yf-chat-sub">只连中台 · 不登录资产</p>
          <p className="yf-chip">
            <i />
            看着 {contextLabel}
          </p>
        </div>
        {mode === 'dock' && onClose !== undefined && (
          <button type="button" className="yf-icon-btn" aria-label="收起" onClick={onClose}>
            ×
          </button>
        )}
      </header>
      <div className="yf-thread" id="yf-thread" ref={threadRef} aria-label="会话消息">
        {messages.map((m) =>
          m.sender === selfId || m.sender === '' ? (
            <article key={m.sequence} className="yf-turn yf-msg yf-msg--human">
              <div className="yf-msg-meta">你</div>
              <div className="yf-bubble">{m.content}<MessageAttachments attachments={m.attachments} onApprovalDecided={onApprovalDecided} /></div>
            </article>
          ) : (
            <AgentTurn key={m.sequence} message={m} onApprovalDecided={onApprovalDecided} />
          ),
        )}
        {thinking && (
          <div className="yf-think-live" aria-live="polite">
            <span className="yf-dots" aria-hidden>
              <i />
              <i />
              <i />
            </span>
            编排中
          </div>
        )}
      </div>
      <form className="yf-composer" onSubmit={submit}>
        <div className="yf-box">
          <textarea
            aria-label="向贾维斯发送消息"
            placeholder="对贾维斯说资产、意图，不要下发 shell…"
            rows={2}
            autoFocus={mode === 'dock'}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={onKey}
          />
          <div className="yf-composer-bar">
            <p className="yf-hint">它只编排。登录机器的是批准后才出生的执行实例。</p>
            <button className="yf-send" type="submit" aria-label="发送" disabled={busy || thinking || draft.trim() === ''}>
              ↑
            </button>
          </div>
        </div>
        {error !== null && <p className="yf-chat-error">{error}</p>}
      </form>
    </div>
  )
}
