// 解析 Connect details 中的 GateResult 与 OnboardingGate。

import { describe, expect, it } from 'vitest'
import { apiErrorFromResponse } from './errors'

describe('Connect details 解析', () => {
  it('failed_precondition + GateResult.debug 抽出 gateChecks', async () => {
    const res = new Response(
      JSON.stringify({
        code: 'failed_precondition',
        message: 'promotion gates not satisfied',
        details: [
          {
            type: 'type.googleapis.com/yufeng.common.v1.GateResult',
            debug: {
              gates: [{ gateKey: 'shadow_min_requests', passed: false, required: '>= 100', actual: '12', message: '影子阶段请求数不足' }],
            },
          },
        ],
      }),
      { status: 400, headers: { 'Content-Type': 'application/json' } },
    )
    const err = await apiErrorFromResponse(res)
    expect(err.gateChecks?.[0]?.gateKey).toBe('shadow_min_requests')
    expect(err.gateChecks?.[0]?.passed).toBe(false)
  })

  it('OnboardingGate.debug 抽出 missingPredicates', async () => {
    const res = new Response(
      JSON.stringify({
        code: 'failed_precondition',
        message: 'onboarding predicates not met',
        details: [
          {
            type: 'type.googleapis.com/yufeng.onboarding.v1.OnboardingGate',
            debug: { missingPredicates: [4, 1] },
          },
        ],
      }),
      { status: 400, headers: { 'Content-Type': 'application/json' } },
    )
    const err = await apiErrorFromResponse(res)
    expect(err.missingPredicates).toEqual([1, 4])
  })
})
