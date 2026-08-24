// 引导探测：失败则再试，合计 SetupModelProbeAttempts 次，间隔 SetupModelProbeRetryMs。
// 取消时返回 false，不把取消当成探测成功。

import { SetupModelProbeAttempts, SetupModelProbeRetryMs, sleep } from './hold'

export async function probeModelWithRetry(
  test: () => Promise<void>,
  opts: {
    attempts?: number
    gapMs?: number
    sleep?: (ms: number) => Promise<void>
    alive?: () => boolean
  } = {},
): Promise<boolean> {
  const attempts = opts.attempts ?? SetupModelProbeAttempts
  const gapMs = opts.gapMs ?? SetupModelProbeRetryMs
  const wait = opts.sleep ?? sleep
  const alive = opts.alive ?? (() => true)
  let last: unknown
  for (let i = 0; i < attempts; i++) {
    if (!alive()) return false
    try {
      await test()
      return true
    } catch (e) {
      last = e
      if (i + 1 < attempts) await wait(gapMs)
    }
  }
  throw last
}
