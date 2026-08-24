// 工具名分类：用户授予表是 docs/api.md §6.1；编排原语只给 Agent 令牌，不进授予多选。

import { TOOL } from '../../api/types'

export interface ToolLeaf {
  name: string
  blurb: string
  grantable: boolean
}

export interface ToolBranch {
  id: string
  label: string
  blurb: string
  grantable: boolean
  tools: ToolLeaf[]
}

export const TOOL_BRANCHES: ToolBranch[] = [
  {
    id: 'read',
    label: '读',
    blurb: '控制台可见范围，仍受 Bindings 裁剪',
    grantable: true,
    tools: [{ name: TOOL.consoleRead, blurb: '读仪表盘 / 事件 / 发布 / 资产 / 审计', grantable: true }],
  },
  {
    id: 'govern',
    label: '治理',
    blurb: '发布生命周期。推进不能授给自己',
    grantable: true,
    tools: [
      { name: TOOL.governPropose, blurb: '提出制品', grantable: true },
      { name: TOOL.governGate, blurb: '门禁回放', grantable: true },
      { name: TOOL.governStartShadow, blurb: '进入影子', grantable: true },
      { name: TOOL.governPromoteCanary, blurb: '小比例推进', grantable: true },
      { name: TOOL.governPromoteEnforce, blurb: '全量生效', grantable: true },
      { name: TOOL.governRollback, blurb: '回滚防护策略', grantable: true },
      { name: TOOL.governRetire, blurb: '退休防护策略', grantable: true },
      { name: TOOL.governDenyFeedback, blurb: '否认误报', grantable: true },
    ],
  },
  {
    id: 'asset',
    label: '资产',
    blurb: '台账写侧另加管理员硬门',
    grantable: true,
    tools: [
      { name: TOOL.assetCreate, blurb: '登记资产', grantable: true },
      { name: TOOL.assetUpdate, blurb: '改资产属性', grantable: true },
      { name: TOOL.assetDelete, blurb: '删除资产', grantable: true },
      { name: TOOL.assetAttach, blurb: '绑定单元', grantable: true },
      { name: TOOL.assetDetach, blurb: '解绑单元', grantable: true },
    ],
  },
  {
    id: 'case',
    label: '案件',
    blurb: '案件、证据与调查流，仍受资产 Bindings 裁剪',
    grantable: true,
    tools: [
      { name: TOOL.caseRead, blurb: '读取授权资产的案件与活动', grantable: true },
      { name: TOOL.caseManage, blurb: '编排和完成授权资产的案件', grantable: true },
      { name: TOOL.evidenceApprove, blurb: '批准一次性案件证据访问', grantable: true },
    ],
  },
  {
    id: 'run',
    label: '执行',
    blurb: '短命 run，做完即焚',
    grantable: true,
    tools: [{ name: TOOL.runCreate, blurb: '创建执行实例', grantable: true }],
  },
  {
    id: 'worker',
    label: '执行池',
    blurb: '中台级注册与容量审批，不要求资产 Binding',
    grantable: true,
    tools: [
      { name: TOOL.workerEnroll, blurb: '登记和批准外部执行进程', grantable: true },
      { name: TOOL.workerCapacityApprove, blurb: '批准中央调查池临时扩容', grantable: true },
    ],
  },
  {
    id: 'agent',
    label: 'Agent',
    blurb: '管理流量审查岗位，资产范围仍受 Bindings 裁剪',
    grantable: true,
    tools: [{ name: TOOL.agentManage, blurb: '配置受管 Agent 的工具与维护资产', grantable: true }],
  },
  {
    id: 'account',
    label: '账户',
    blurb: '不针对资产，Bindings 可空',
    grantable: true,
    tools: [
      { name: TOOL.grantWrite, blurb: '写授予', grantable: true },
      { name: TOOL.userAdmin, blurb: '用户管理', grantable: true },
      { name: TOOL.catalogManage, blurb: '管理工具和技能目录', grantable: true },
    ],
  },
  {
    id: 'orchestrate',
    label: '编排原语',
    blurb: '只出现在 Agent 能力令牌，不进授予表',
    grantable: false,
    tools: [
      { name: 'session.reply', blurb: '贾维斯回写会话', grantable: false },
      { name: 'event.get', blurb: '读单条事件', grantable: false },
      { name: 'event.list', blurb: '列事件', grantable: false },
      { name: 'release.list', blurb: '列发布', grantable: false },
    ],
  },
]

export function grantableTools(): ToolLeaf[] {
  return TOOL_BRANCHES.flatMap((b) => b.tools.filter((t) => t.grantable))
}

export function findToolLeaf(name: string): ToolLeaf | undefined {
  for (const b of TOOL_BRANCHES) {
    const hit = b.tools.find((t) => t.name === name)
    if (hit !== undefined) return hit
  }
  return undefined
}

const ACCOUNT_ONLY_TOOLS = new Set<string>([
  TOOL.userAdmin,
  TOOL.grantWrite,
  TOOL.catalogManage,
  TOOL.workerEnroll,
  TOOL.workerCapacityApprove,
])

export function grantNeedsAssetBinding(tools: string[]): boolean {
  return tools.some((tool) => !ACCOUNT_ONLY_TOOLS.has(tool))
}
