// 贾维斯会话：创建 / 发送 / 长轮询。全局坞与编排台面共用一份消息。
// 这些 RPC 只认 Login.token + 属主，不查授予（docs/api.md §18.5）。

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { SessionLongPollDefault } from '../../api/limits'
import type { ChatMessage } from '../../api/types'
import { isApiError } from '../../api/errors'
import { useAuth } from '../../auth/useAuth'
import { loadSessionSignals, type SessionSignals } from './jarvisSignals'
import { JarvisSessionContext, type JarvisSessionValue } from './useJarvisSession'

const SessionAttentionRefreshMilliseconds = 30_000

export function JarvisSessionProvider({ children }: { children: ReactNode }) {
	const { client, user, onboarding, refreshOnboarding } = useAuth()
  const [sessionId, setSessionId] = useState<string | null>(null)
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [busy, setBusy] = useState(false)
  const [thinking, setThinking] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [contextLabel, setContextLabel] = useState('中台')
  const [dockOpen, setDockOpen] = useState(false)
	const [signals, setSignals] = useState<SessionSignals>({ focusAssetIds: [], pendingGate: false })
	const [jarvisHealthUnknown, setJarvisHealthUnknown] = useState(false)
	const [signalRevision, setSignalRevision] = useState(0)
	const cursorRef = useRef('0')
	const creating = useRef<Promise<string | null> | null>(null)
	const pendingSequence = useRef<string | null>(null)
	const replyTimer = useRef<number | null>(null)

	const clearReplyTimer = useCallback(() => {
		if (replyTimer.current !== null) {
			window.clearTimeout(replyTimer.current)
			replyTimer.current = null
		}
	}, [])

	const finishThinking = useCallback(() => {
		clearReplyTimer()
		pendingSequence.current = null
		setThinking(false)
	}, [clearReplyTimer])

  const ensureSession = useCallback(async (): Promise<string | null> => {
    if (sessionId !== null) return sessionId
    if (creating.current !== null) return creating.current
    creating.current = (async () => {
      try {
        const created = await client.createSession({ title: 'console' })
        setSessionId(created.sessionId)
        setMessages([])
        cursorRef.current = '0'
        return created.sessionId
      } catch (e) {
        setError(isApiError(e) ? `${e.message}（${e.code}）` : '创建会话失败')
        return null
      } finally {
        creating.current = null
      }
    })()
    return creating.current
  }, [client, sessionId])

	const send = useCallback(
		async (text: string) => {
      const sid = await ensureSession()
      if (sid === null || text.trim() === '') return
      setBusy(true)
      setThinking(true)
			setError(null)
			try {
				const sent = await client.sendMessage({ sessionId: sid, content: text.trim() })
				pendingSequence.current = sent.messageSequence
				const listed = await client.listMessages({ sessionId: sid }, { pageSize: 200 })
				const ordered = [...listed.items].reverse()
				setMessages(ordered)
				const replied = ordered.some(
					(m) => m.sender !== '' && m.sender !== user?.userId && sequenceAfter(m.sequence, sent.messageSequence),
				)
				if (replied) {
					finishThinking()
				} else {
					clearReplyTimer()
					replyTimer.current = window.setTimeout(() => {
						pendingSequence.current = null
						setThinking(false)
						setError('贾维斯暂未回复，可以稍后重试')
					}, (SessionLongPollDefault * 2 + 10) * 1000)
				}
			} catch (e) {
				finishThinking()
				setError(isApiError(e) ? `${e.message}（${e.code}）` : '发送失败')
			} finally {
				setBusy(false)
			}
		},
		[clearReplyTimer, client, ensureSession, finishThinking, user?.userId],
	)

  const poll = useCallback(async () => {
    if (sessionId === null) return
    try {
      const got = await client.pollMessages({
        sessionId,
        cursor: cursorRef.current,
        longPollSeconds: SessionLongPollDefault,
      })
			if (got.messages.length > 0) {
        setMessages((prev) => {
          const seen = new Set(prev.map((m) => m.sequence))
          const extra = got.messages.filter((m) => !seen.has(m.sequence))
          return extra.length === 0 ? prev : [...prev, ...extra]
        })
				const pending = pendingSequence.current
				if (
					pending !== null &&
					got.messages.some((m) => m.sender !== '' && m.sender !== user?.userId && sequenceAfter(m.sequence, pending))
				) {
					finishThinking()
				}
      }
      if (got.nextCursor !== '') cursorRef.current = got.nextCursor
    } catch {
      // 长轮询失败不打断输入
    }
	}, [client, finishThinking, sessionId, user?.userId])

	useEffect(() => {
    if (sessionId === null) return
    let cancelled = false
    const loop = async () => {
      while (!cancelled) {
        const before = cursorRef.current
        await poll()
        if (cancelled) break
        if (cursorRef.current === before) {
          await new Promise((r) => setTimeout(r, 1000))
        }
      }
    }
    void loop()
    return () => {
      cancelled = true
    }
	}, [sessionId, poll])

	useEffect(() => clearReplyTimer, [clearReplyTimer])

	const refreshSignals = useCallback(() => setSignalRevision((current) => current + 1), [])

	useEffect(() => {
		const timer = window.setInterval(refreshSignals, SessionAttentionRefreshMilliseconds)
		return () => window.clearInterval(timer)
	}, [refreshSignals])

	useEffect(() => {
		let cancelled = false
		const refresh = () => {
			void refreshOnboarding()
				.then(() => {
					if (!cancelled) setJarvisHealthUnknown(false)
				})
				.catch(() => {
					if (!cancelled) setJarvisHealthUnknown(true)
				})
		}
		const timer = window.setInterval(refresh, SessionAttentionRefreshMilliseconds)
		return () => {
			cancelled = true
			window.clearInterval(timer)
		}
	}, [refreshOnboarding])

	useEffect(() => {
		let cancelled = false
		void loadSessionSignals(client, messages).then((next) => {
			if (!cancelled) setSignals(next)
		})
		return () => {
			cancelled = true
		}
	}, [client, messages, signalRevision])

  // 资产焦点与待批准徽标只来自会话附件引用指向的当前 RPC 状态，不能从会话正文猜。

  const value = useMemo<JarvisSessionValue>(
    () => ({
      sessionId,
      messages,
      busy,
      thinking,
      error,
      contextLabel,
      setContextLabel,
      focusAssetIds: signals.focusAssetIds,
      dockOpen,
      setDockOpen,
      pendingGate: signals.pendingGate,
      jarvisOnline: jarvisHealthUnknown ? null : (onboarding?.jarvisOnline ?? null),
      refreshSignals,
      ensureSession,
      send,
    }),
    [sessionId, messages, busy, thinking, error, contextLabel, signals, jarvisHealthUnknown, onboarding?.jarvisOnline, dockOpen, ensureSession, refreshSignals, send],
  )

  return <JarvisSessionContext.Provider value={value}>{children}</JarvisSessionContext.Provider>
}

function sequenceAfter(candidate: string, base: string): boolean {
	try {
		return BigInt(candidate) > BigInt(base)
	} catch {
		return candidate !== base
	}
}
