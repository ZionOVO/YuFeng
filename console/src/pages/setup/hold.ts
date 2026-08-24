// 引导里的模型探测与 Edge 心跳确认若瞬间完成，界面会一闪而过。
// 停顿取 max(下限, 实际耗时)，不是再人为垫一段固定等待。

export const SetupMinHoldMs = 1500
export const SetupOkHoldMs = 1100
export const SetupModelProbeAttempts = 3
export const SetupModelProbeRetryMs = 1000

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    window.setTimeout(resolve, ms)
  })
}

export async function holdAtLeast(startedAt: number, minMs = SetupMinHoldMs): Promise<void> {
  const left = minMs - (Date.now() - startedAt)
  if (left > 0) await sleep(left)
}
