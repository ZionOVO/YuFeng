import { createContext, useContext } from 'react'
import type { ChatMessage } from '../../api/types'

export interface JarvisSessionValue {
  sessionId: string | null
  messages: ChatMessage[]
  busy: boolean
  thinking: boolean
  error: string | null
  contextLabel: string
  setContextLabel: (label: string) => void
  focusAssetIds: string[]
  dockOpen: boolean
  setDockOpen: (open: boolean) => void
  pendingGate: boolean
  jarvisOnline: boolean | null
  refreshSignals: () => void
  ensureSession: () => Promise<string | null>
  send: (text: string) => Promise<void>
}

export const JarvisSessionContext = createContext<JarvisSessionValue | null>(null)

export function useJarvisSession(): JarvisSessionValue {
  const ctx = useContext(JarvisSessionContext)
  if (ctx === null) throw new Error('useJarvisSession requires JarvisSessionProvider')
  return ctx
}
