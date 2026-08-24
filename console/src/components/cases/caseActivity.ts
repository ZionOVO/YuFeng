import type { CaseActivity } from '../../api/types'

export function mergeCaseActivities(current: CaseActivity[], incoming: CaseActivity[]): CaseActivity[] {
  const bySequence = new Map(current.map((activity) => [activity.sequence, activity]))
  for (const activity of incoming) bySequence.set(activity.sequence, activity)
  return [...bySequence.values()].sort((a, b) => {
    try {
      const left = BigInt(a.sequence)
      const right = BigInt(b.sequence)
      return left < right ? -1 : left > right ? 1 : 0
    } catch {
      return a.sequence.localeCompare(b.sequence)
    }
  })
}
