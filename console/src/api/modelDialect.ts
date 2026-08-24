import type { ModelDialect } from './types'

export const MODEL_DIALECTS: { value: ModelDialect; label: string }[] = [
  { value: 'MODEL_DIALECT_OPENAI_CHAT', label: 'OpenAI Chat Completions' },
  { value: 'MODEL_DIALECT_OPENAI_RESPONSES', label: 'OpenAI Responses' },
  { value: 'MODEL_DIALECT_CLAUDE_MESSAGES', label: 'Claude Messages' },
]

export function normalizeDialect(raw: string | undefined): ModelDialect {
  if (raw === 'MODEL_DIALECT_OPENAI_RESPONSES' || raw === 'MODEL_DIALECT_CLAUDE_MESSAGES' || raw === 'MODEL_DIALECT_OPENAI_CHAT') {
    return raw
  }
  return 'MODEL_DIALECT_OPENAI_CHAT'
}
