import { probeModelWithRetry } from './probe'

describe('probeModelWithRetry', () => {
  it('第一次成功则不再重试', async () => {
    const calls: number[] = []
    const ok = await probeModelWithRetry(async () => {
      calls.push(1)
    })
    expect(ok).toBe(true)
    expect(calls).toHaveLength(1)
  })

  it('前两次失败、第三次成功', async () => {
    let n = 0
    const waits: number[] = []
    const ok = await probeModelWithRetry(
      async () => {
        n += 1
        if (n < 3) throw new Error(`fail ${n}`)
      },
      { sleep: async (ms) => { waits.push(ms) } },
    )
    expect(ok).toBe(true)
    expect(n).toBe(3)
    expect(waits).toEqual([1000, 1000])
  })

  it('三次皆失败则抛最后一次错误', async () => {
    let n = 0
    await expect(
      probeModelWithRetry(
        async () => {
          n += 1
          throw new Error(`fail ${n}`)
        },
        { sleep: async () => undefined },
      ),
    ).rejects.toThrow('fail 3')
    expect(n).toBe(3)
  })

  it('中途取消则返回 false 且不抛', async () => {
    let n = 0
    const ok = await probeModelWithRetry(
      async () => {
        n += 1
        throw new Error('fail')
      },
      { alive: () => n < 1, sleep: async () => undefined },
    )
    expect(ok).toBe(false)
    expect(n).toBe(1)
  })
})
