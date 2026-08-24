// Connect 协议错误解析（docs/api.md §0.2、§13、§17.7）。
// 一律先读响应体 code；failed_precondition 的 details 尽力提取 GateResult。

import type { GateCheck } from './types'

/** docs/api.md §13 目录内的错误码；目录外的未知码归为 'unknown'。 */
export type ConnectErrorCode =
  | 'unauthenticated'
  | 'permission_denied'
  | 'already_exists'
  | 'not_found'
  | 'invalid_argument'
  | 'failed_precondition'
  | 'resource_exhausted'
  | 'unavailable'
  | 'deadline_exceeded'
  | 'unknown'

const KNOWN_CODES: ReadonlySet<string> = new Set([
  'unauthenticated',
  'permission_denied',
  'already_exists',
  'not_found',
  'invalid_argument',
  'failed_precondition',
  'resource_exhausted',
  'unavailable',
  'deadline_exceeded',
])

/** Connect 标准 JSON 错误体中 details 条目的线格式。 */
interface ErrorDetailWire {
  type?: string
  /** base64 编码的 proto 二进制；无生成代码时不可解码。 */
  value?: string
  /** 部分服务端附带 protojson 调试表示。 */
  debug?: Record<string, unknown>
}

interface ConnectErrorWire {
  code?: string
  message?: string
  details?: ErrorDetailWire[]
}

/** API 调用失败：code 来自响应体（docs/api.md §13），HTTP 状态码只作参考。 */
export class ApiError extends Error {
  readonly code: ConnectErrorCode
  readonly httpStatus: number
  /** failed_precondition details 中的结构化原因键（如 release_state_conflict / gate_not_satisfied）。 */
  readonly reasonKey?: string
  /** 推进门槛逐项结果（details 携带 GateResult 且带 debug 表示时填充）。 */
  readonly gateChecks?: GateCheck[]
  /** CompleteOnboarding 失败时 OnboardingGate.missing_predicates（1–4）。 */
  readonly missingPredicates?: number[]

  constructor(init: {
    code: ConnectErrorCode
    message: string
    httpStatus: number
    reasonKey?: string
    gateChecks?: GateCheck[]
    missingPredicates?: number[]
  }) {
    super(init.message)
    this.name = 'ApiError'
    this.code = init.code
    this.httpStatus = init.httpStatus
    this.reasonKey = init.reasonKey
    this.gateChecks = init.gateChecks
    this.missingPredicates = init.missingPredicates
  }
}

export function isApiError(err: unknown): err is ApiError {
  return err instanceof ApiError
}

export function hasCode(err: unknown, code: ConnectErrorCode): boolean {
  return isApiError(err) && err.code === code
}

/** 从 OnboardingGate.debug 取出 missing_predicates。 */
function extractMissingPredicates(detail: ErrorDetailWire): number[] | undefined {
  const raw = detail.debug?.missingPredicates ?? detail.debug?.missing_predicates
  if (!Array.isArray(raw)) return undefined
  const nums = raw.map((n) => Number(n)).filter((n) => Number.isInteger(n) && n >= 1 && n <= 4)
  return nums.length > 0 ? [...new Set(nums)].sort((a, b) => a - b) : undefined
}

/** 把 GateResult 条目的 debug 表示映射为 GateCheck 数组；debug 缺失时返回 undefined。 */
function extractGateChecks(detail: ErrorDetailWire): GateCheck[] | undefined {
  const gates = detail.debug?.gates
  if (!Array.isArray(gates)) return undefined
  return gates.map((g) => {
    const rec = g as Record<string, unknown>
    return {
      gateKey: String(rec.gateKey ?? ''),
      passed: rec.passed === true,
      required: String(rec.required ?? ''),
      actual: String(rec.actual ?? ''),
      message: String(rec.message ?? ''),
    }
  })
}

/**
 * 从 HTTP 响应构造 ApiError。响应体不是合法 Connect 错误 JSON 时按 unavailable 处理；
 * 网络层失败（fetch reject）由调用方包装为 unavailable。
 */
export async function apiErrorFromResponse(res: Response): Promise<ApiError> {
  let wire: ConnectErrorWire | null = null
  try {
    wire = (await res.json()) as ConnectErrorWire
  } catch {
    // 非 JSON 错误体（网关 HTML 等）：落到通用不可用
  }
  const rawCode = typeof wire?.code === 'string' ? wire.code : ''
  const code: ConnectErrorCode = (KNOWN_CODES.has(rawCode) ? rawCode : res.status === 401 ? 'unauthenticated' : 'unknown') as ConnectErrorCode
  const message = typeof wire?.message === 'string' && wire.message !== '' ? wire.message : `request failed with status ${res.status}`

  let reasonKey: string | undefined
  let gateChecks: GateCheck[] | undefined
  let missingPredicates: number[] | undefined
  for (const d of wire?.details ?? []) {
    const type = d.type ?? ''
    if (type.endsWith('GateResult')) {
      gateChecks = extractGateChecks(d)
    } else if (type.endsWith('OnboardingGate')) {
      missingPredicates = extractMissingPredicates(d)
    } else if (type !== '') {
      // 结构化原因键（release_state_conflict / cursor_expired / user_disabled …）
      reasonKey = type.includes('/') ? type.split('/').pop() : type
      if (typeof d.debug?.reason === 'string') reasonKey = d.debug.reason
    }
  }
  return new ApiError({ code, message, httpStatus: res.status, reasonKey, gateChecks, missingPredicates })
}

/** 网络层失败（连接拒绝、超时、跨域拦截）统一包装为 unavailable。 */
export function networkError(err: unknown): ApiError {
  const message = err instanceof Error && err.message !== '' ? err.message : 'network request failed'
  return new ApiError({ code: 'unavailable', message, httpStatus: 0 })
}
