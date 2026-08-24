// 全局印章：固定右下。点开展开同一套对话。编排场本页不用印章。

import { useEffect, useRef } from 'react'
import { useAuth } from '../../auth/useAuth'
import { BrandMark } from '../BrandMark'
import { useJarvisSession } from './useJarvisSession'
import { YfChat } from './YfChat'
import { useDialogFocusTrap } from '../useDialogFocusTrap'

export function JarvisDock() {
  const { user } = useAuth()
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const wasOpen = useRef(false)
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const {
    dockOpen,
    ensureSession,
    setDockOpen,
    pendingGate,
    refreshSignals,
    messages,
    contextLabel,
    thinking,
    busy,
    error,
    jarvisOnline,
    send,
  } = useJarvisSession()
  useDialogFocusTrap(dockOpen, dialogRef)

  useEffect(() => {
    if (!dockOpen) return
    void ensureSession()
  }, [dockOpen, ensureSession])

  useEffect(() => {
    if (!dockOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setDockOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [dockOpen, setDockOpen])

  useEffect(() => {
    if (dockOpen) {
      wasOpen.current = true
      document.body.classList.add('yf-jarvis-open')
      return () => document.body.classList.remove('yf-jarvis-open')
    }
    if (wasOpen.current) {
      triggerRef.current?.focus()
      wasOpen.current = false
    }
  }, [dockOpen])

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        className={`yf-fab${dockOpen ? ' is-open' : ''}`}
        aria-label="打开贾维斯"
        aria-expanded={dockOpen}
        onClick={() => setDockOpen(!dockOpen)}
      >
        <BrandMark size={28} />
        {pendingGate && <i className="yf-fab-badge" />}
      </button>
      {dockOpen && (
        <YfChat
          mode="dock"
          messages={messages}
          selfId={user?.userId ?? ''}
          contextLabel={contextLabel}
          thinking={thinking}
          busy={busy}
          error={error}
          online={jarvisOnline}
          onSend={(text) => void send(text)}
          onApprovalDecided={refreshSignals}
          onClose={() => setDockOpen(false)}
          rootRef={dialogRef}
        />
      )}
    </>
  )
}
