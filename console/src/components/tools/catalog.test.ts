import { findToolLeaf, grantableTools, grantNeedsAssetBinding } from './catalog'

describe('工具授予目录', () => {
  it('只向人员授予可授予工具，且可按稳定工具名查找', () => {
    const tools = grantableTools()

    expect(tools.length).toBeGreaterThan(0)
    expect(tools.every((tool) => tool.grantable)).toBe(true)
    expect(findToolLeaf('asset.create')).toMatchObject({ name: 'asset.create', grantable: true })
    expect(findToolLeaf('session.reply')).toMatchObject({ name: 'session.reply', grantable: false })
    expect(findToolLeaf('unknown.tool')).toBeUndefined()
  })

  it('账户级工具不要求资产绑定，其他工具必须受资产范围约束', () => {
    expect(grantNeedsAssetBinding(['user.admin', 'grant.write', 'catalog.manage', 'worker.enroll', 'worker.capacity.approve'])).toBe(false)
    expect(grantNeedsAssetBinding(['console.read'])).toBe(true)
    expect(grantNeedsAssetBinding(['worker.enroll', 'case.manage'])).toBe(true)
  })
})
