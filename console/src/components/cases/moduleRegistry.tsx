import type { InvestigationCase } from '../../api/types'
import type { ReactNode } from 'react'
import { TrafficFindingCard } from './CaseCards'

type ModuleRenderer = { finding?: (item: InvestigationCase) => ReactNode }

const MODULE_RENDERERS: Record<string, ModuleRenderer> = {
  'traffic-interception': {
    finding: (item) => item.finding === undefined ? null : <TrafficFindingCard finding={item.finding} />,
  },
}

export function moduleRenderer(moduleId: string): ModuleRenderer | undefined {
  return MODULE_RENDERERS[moduleId]
}
