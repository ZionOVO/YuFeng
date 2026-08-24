// 生效权限判断：与 docs/api.md §6.1 / §17.2 同一张工具名表。
// 只做 UX；服务端仍是唯一鉴权。

import type { BindingRef, EffectiveAccess } from './types'

export function emptyAccess(): EffectiveAccess {
  return { tools: [], bindings: [] }
}

export function hasTool(access: EffectiveAccess | null | undefined, tool: string): boolean {
  return access?.tools.includes(tool) ?? false
}

export function binds(access: EffectiveAccess | null | undefined, kind: BindingRef['kind'], id: string): boolean {
  if (access === null || access === undefined) return false
  return access.bindings.some((b) => b.kind === kind && b.id === id)
}

/** 写某个资产：既有工具，资产又在 Bindings 里。 */
export function canOnAsset(access: EffectiveAccess | null | undefined, tool: string, assetId: string): boolean {
  return hasTool(access, tool) && binds(access, 'asset', assetId)
}

/** 写某个发布：工具具备，且 scope.assetIds 全部落在 Bindings 内。 */
export function canOnRelease(
  access: EffectiveAccess | null | undefined,
  tool: string,
  assetIds: string[] | undefined,
): boolean {
  if (assetIds === undefined || assetIds.length === 0) return false
  return assetIds.every((id) => canOnAsset(access, tool, id))
}

/** 工作项 Bindings 必须是档案的子集。 */
export function containsBindings(archive: BindingRef[], needed: BindingRef[]): boolean {
  return needed.every((n) => archive.some((a) => a.kind === n.kind && a.id === n.id))
}
