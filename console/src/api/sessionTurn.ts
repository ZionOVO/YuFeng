// 会话正文可选信封的解析器。SessionService 仍只传 UTF-8（docs/api.md §18.5）。
// 操作员对话台不把本信封当成工具/实例/待盖印事实；解析仅供单测与剥离正文。

export const SESSION_TURN_HEAD = 'YF/1\n'

export type ToolCallState = 'running' | 'done' | 'wait' | 'fail'

export interface SessionToolCall {
  name: string
  state: ToolCallState
  note?: string
  kv?: [string, string][]
  assetId?: string
  releaseId?: string
}

export interface SessionRunTicket {
  id: string
  role: string
  state: string
  note?: string
}

export interface SessionGateCard {
  title: string
  status: 'open' | 'sealed' | 'denied'
  kv?: [string, string][]
  releaseId?: string
  assetId?: string
}

export interface SessionTurn {
  text?: string
  thinking?: string
  tools?: SessionToolCall[]
  runs?: SessionRunTicket[]
  gate?: SessionGateCard
}

function asKv(raw: unknown): [string, string][] | undefined {
  if (!Array.isArray(raw)) return undefined
  const out: [string, string][] = []
  for (const row of raw) {
    if (!Array.isArray(row) || row.length < 2) continue
    out.push([String(row[0]), String(row[1])])
  }
  return out.length > 0 ? out : undefined
}

function asTools(raw: unknown): SessionToolCall[] | undefined {
  if (!Array.isArray(raw)) return undefined
  const out: SessionToolCall[] = []
  for (const item of raw) {
    if (item === null || typeof item !== 'object') continue
    const rec = item as Record<string, unknown>
    if (typeof rec.name !== 'string' || rec.name === '') continue
    const state = rec.state
    out.push({
      name: rec.name,
      state: state === 'running' || state === 'wait' || state === 'fail' ? state : 'done',
      note: typeof rec.note === 'string' ? rec.note : undefined,
      kv: asKv(rec.kv),
      assetId: typeof rec.assetId === 'string' ? rec.assetId : undefined,
      releaseId: typeof rec.releaseId === 'string' ? rec.releaseId : undefined,
    })
  }
  return out.length > 0 ? out : undefined
}

function asRuns(raw: unknown): SessionRunTicket[] | undefined {
  if (!Array.isArray(raw)) return undefined
  const out: SessionRunTicket[] = []
  for (const item of raw) {
    if (item === null || typeof item !== 'object') continue
    const rec = item as Record<string, unknown>
    if (typeof rec.id !== 'string' || rec.id === '') continue
    out.push({
      id: rec.id,
      role: typeof rec.role === 'string' ? rec.role : '',
      state: typeof rec.state === 'string' ? rec.state : '',
      note: typeof rec.note === 'string' ? rec.note : undefined,
    })
  }
  return out.length > 0 ? out : undefined
}

function asGate(raw: unknown): SessionGateCard | undefined {
  if (raw === null || typeof raw !== 'object') return undefined
  const rec = raw as Record<string, unknown>
  if (typeof rec.title !== 'string' || rec.title === '') return undefined
  const status = rec.status === 'sealed' || rec.status === 'denied' ? rec.status : 'open'
  return {
    title: rec.title,
    status,
    kv: asKv(rec.kv),
    releaseId: typeof rec.releaseId === 'string' ? rec.releaseId : undefined,
    assetId: typeof rec.assetId === 'string' ? rec.assetId : undefined,
  }
}

/** 把回合编进会话 content。超长由调用方负责（上限 8192 字节）。 */
export function encodeSessionTurn(turn: SessionTurn): string {
  return SESSION_TURN_HEAD + JSON.stringify({ yf: 1, ...turn })
}

/** 拆会话 content。不是信封就当纯文本。 */
export function parseSessionTurn(content: string): SessionTurn {
  if (!content.startsWith(SESSION_TURN_HEAD)) return { text: content }
  try {
    const raw: unknown = JSON.parse(content.slice(SESSION_TURN_HEAD.length))
    if (raw === null || typeof raw !== 'object') return { text: content }
    const rec = raw as Record<string, unknown>
    if (rec.yf !== 1) return { text: content }
    return {
      text: typeof rec.text === 'string' ? rec.text : undefined,
      thinking: typeof rec.thinking === 'string' ? rec.thinking : undefined,
      tools: asTools(rec.tools),
      runs: asRuns(rec.runs),
      gate: asGate(rec.gate),
    }
  } catch {
    return { text: content }
  }
}

/** 本回合点名的资产，供拓扑高亮。 */
export function turnAssetIds(turn: SessionTurn): string[] {
  const ids = new Set<string>()
  for (const t of turn.tools ?? []) {
    if (t.assetId !== undefined && t.assetId !== '') ids.add(t.assetId)
  }
  if (turn.gate?.assetId !== undefined && turn.gate.assetId !== '') ids.add(turn.gate.assetId)
  return [...ids]
}

/** 从新到旧取最近一次点名的资产。 */
export function latestTurnAssetIds(contents: string[]): string[] {
  for (let i = contents.length - 1; i >= 0; i--) {
    const ids = turnAssetIds(parseSessionTurn(contents[i] ?? ''))
    if (ids.length > 0) return ids
  }
  return []
}
