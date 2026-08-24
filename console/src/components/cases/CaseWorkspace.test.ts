import type { CaseActivity } from '../../api/types'
import { mergeCaseActivities } from './caseActivity'

function activity(sequence: string, summary: string): CaseActivity {
  return { sequence, caseId: 'case-1', kind: 'CASE_ACTIVITY_KIND_STATE_CHANGED', refId: '', summary }
}

describe('案件活动增量合并', () => {
  it('跨页追加、去重并按数值序列排序', () => {
    const current = [activity('99', '旧'), activity('100', '重复旧值')]
    const next = [activity('100', '重复新值'), activity('101', '新')]

    expect(mergeCaseActivities(current, next).map((item) => [item.sequence, item.summary])).toEqual([
      ['99', '旧'],
      ['100', '重复新值'],
      ['101', '新'],
    ])
  })
})
