import { holdAtLeast, SetupMinHoldMs } from './hold'

describe('holdAtLeast', () => {
  it('已超过下限则立刻返回', async () => {
    const t0 = Date.now() - SetupMinHoldMs - 20
    const start = Date.now()
    await holdAtLeast(t0)
    expect(Date.now() - start).toBeLessThan(80)
  })
})
