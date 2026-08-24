// 授予页：主体不能是自己；Tools 多选；Bindings 从 ListAssets 勾选；禁止 *（docs/api.md §17.4.2）。

import { useMemo, useState } from 'react'
import { Button, Checkbox, Select, SelectItem } from '@heroui/react'
import { useSearchParams } from 'react-router-dom'
import { isApiError } from '../../api/errors'
import { useAuth } from '../../auth/useAuth'
import { useAsyncData } from '../../components/useAsyncData'
import { StateView } from '../../components/ui'
import { ToolTree } from '../../components/tools/ToolTree'
import { grantNeedsAssetBinding } from '../../components/tools/catalog'

export function GrantsPage() {
  const { client, user, hasTool } = useAuth()
  const [params] = useSearchParams()
  const preset = params.get('subject') ?? ''

  const usersQuery = useAsyncData(() => client.listUsers({}, { pageSize: 200 }), [])
  const assetsQuery = useAsyncData(() => client.listAssets({}, { pageSize: 200 }), [])
  const [subjectUserId, setSubjectUserId] = useState(preset)
  const [tools, setTools] = useState<string[]>([])
  const [assetIds, setAssetIds] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  const others = useMemo(() => {
    const items = usersQuery.data?.items ?? []
    const filtered = items.filter((u) => u.userId !== user?.userId && u.state !== 'USER_STATE_DELETED')
    if (preset !== '' && !filtered.some((u) => u.userId === preset)) {
      return [{ userId: preset, username: preset, displayName: '', role: 'USER_ROLE_OPERATOR' as const, state: 'USER_STATE_ACTIVE' as const }, ...filtered]
    }
    return filtered
  }, [usersQuery.data, user?.userId, preset])

  const toggleTool = (name: string) => {
    setTools((prev) => (prev.includes(name) ? prev.filter((t) => t !== name) : [...prev, name]))
  }
  const toggleAsset = (id: string) => {
    setAssetIds((prev) => (prev.includes(id) ? prev.filter((a) => a !== id) : [...prev, id]))
  }

  const submit = async () => {
    setBusy(true)
    setError(null)
    setSaved(false)
    try {
      await client.putGrant({
        subjectUserId,
        tools,
        bindings: assetIds.map((id) => ({ kind: 'asset' as const, id })),
      })
      setSaved(true)
    } catch (e) {
      setError(isApiError(e) ? `${e.message}（${e.reasonKey ?? e.code}）` : '保存失败')
    } finally {
      setBusy(false)
    }
  }

  if (!hasTool('grant.write')) {
    return <StateView kind="denied" message="授予页需要 grant.write" />
  }

  const assets = assetsQuery.data?.items ?? []
  const subjectName = others.find((u) => u.userId === subjectUserId)?.username ?? subjectUserId
  const needsAssetBinding = grantNeedsAssetBinding(tools)

  return (
    <div className="flex flex-col gap-4">
      <section className="fs-panel" aria-label="授予">
        <div className="fs-panel-head">
          <p className="fs-panel-title">授予</p>
          <p className="fs-panel-sub">{preset !== '' ? '补授' : 'PUT GRANT'}</p>
        </div>
        {preset !== '' && (
          <p className="px-4 pt-3 text-xs text-[#8b98a1]">
            补授 <span className="fs-mono">{subjectName}</span>
          </p>
        )}
        <div className="flex flex-col gap-4 px-4 py-3">
          <Select
            label="主体"
            radius="md"
            selectedKeys={subjectUserId === '' ? [] : [subjectUserId]}
            onChange={(e) => setSubjectUserId(e.target.value)}
            isDisabled={preset !== ''}
          >
            {others.map((u) => (
              <SelectItem key={u.userId}>{u.username}</SelectItem>
            ))}
          </Select>

          <fieldset>
            <legend className="mb-2 text-xs text-[#8b98a1]">Tools</legend>
            <ToolTree mode="select" selected={tools} onToggle={toggleTool} />
          </fieldset>

          <fieldset>
            <legend className="mb-2 text-xs text-[#8b98a1]">Bindings（资产类工具必填；账户类工具可空）</legend>
            {assets.length === 0 && <p className="text-xs text-[#8b98a1]">当前可见资产为空</p>}
            {assets.map((a) => (
              <Checkbox
                key={a.asset.id}
                isSelected={assetIds.includes(a.asset.id)}
                onValueChange={() => toggleAsset(a.asset.id)}
                aria-label={`资产 ${a.asset.id}`}
              >
                <span className="fs-mono">{a.asset.id}</span> {a.asset.displayName}
              </Checkbox>
            ))}
          </fieldset>

          {error !== null && <p className="text-xs text-[#ff746c]">{error}</p>}
          {saved && <p className="text-xs text-[#62e6a7]">已保存</p>}
          <Button
            color="primary"
            size="sm"
            radius="md"
            isLoading={busy}
            isDisabled={subjectUserId === '' || tools.length === 0 || (needsAssetBinding && assetIds.length === 0)}
            onPress={() => void submit()}
          >
            保存授予
          </Button>
        </div>
      </section>
    </div>
  )
}
