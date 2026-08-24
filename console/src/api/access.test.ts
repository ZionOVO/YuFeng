import { binds, canOnAsset, canOnRelease, containsBindings, emptyAccess, hasTool } from './access'

describe('生效权限范围', () => {
  const access = {
    tools: ['asset.update', 'govern.promote_canary'],
    bindings: [
      { kind: 'asset' as const, id: 'asset-01' },
      { kind: 'release' as const, id: 'release-01' },
    ],
  }

  it('空授权和空值没有任何工具或对象范围', () => {
    expect(emptyAccess()).toEqual({ tools: [], bindings: [] })
    expect(hasTool(undefined, 'asset.update')).toBe(false)
    expect(hasTool(null, 'asset.update')).toBe(false)
    expect(binds(undefined, 'asset', 'asset-01')).toBe(false)
    expect(binds(null, 'asset', 'asset-01')).toBe(false)
  })

  it('资产写入同时要求工具与对应资产绑定', () => {
    expect(hasTool(access, 'asset.update')).toBe(true)
    expect(hasTool(access, 'asset.delete')).toBe(false)
    expect(binds(access, 'asset', 'asset-01')).toBe(true)
    expect(binds(access, 'asset', 'asset-02')).toBe(false)
    expect(canOnAsset(access, 'asset.update', 'asset-01')).toBe(true)
    expect(canOnAsset(access, 'asset.update', 'asset-02')).toBe(false)
    expect(canOnAsset(access, 'asset.delete', 'asset-01')).toBe(false)
  })

  it('发布推进拒绝空范围并要求全部目标资产均已绑定', () => {
    expect(canOnRelease(access, 'govern.promote_canary', undefined)).toBe(false)
    expect(canOnRelease(access, 'govern.promote_canary', [])).toBe(false)
    expect(canOnRelease(access, 'govern.promote_canary', ['asset-01'])).toBe(true)
    expect(canOnRelease(access, 'govern.promote_canary', ['asset-01', 'asset-02'])).toBe(false)
  })

  it('档案范围必须包含每一个需要的绑定', () => {
    expect(containsBindings(access.bindings, [{ kind: 'asset', id: 'asset-01' }])).toBe(true)
    expect(containsBindings(access.bindings, [{ kind: 'asset', id: 'asset-01' }, { kind: 'release', id: 'release-01' }])).toBe(true)
    expect(containsBindings(access.bindings, [{ kind: 'asset', id: 'asset-02' }])).toBe(false)
  })
})
