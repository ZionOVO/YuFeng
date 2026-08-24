import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { AssetDetail, AssetPatch, TrafficReviewPolicyStatus } from '../../api/types'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../../test/renderApp'

beforeEach(() => {
  sessionStorage.clear()
})

describe('资产增删改查角色', () => {
  it('资产详情只读展示单元生产能力与健康', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/assets/asset-01', client })

    expect(await screen.findByText(/PRODUCER_OUTPUT_CRITICAL_EVENT/)).toBeInTheDocument()
    expect(screen.getByText(/SENSOR_TYPE_CORAZA/)).toBeInTheDocument()
    expect(screen.getByText(/缓冲 关键 0 \/ 普通 0/)).toBeInTheDocument()
  })

  it('流量审查只能逐级发布并等待 Edge 心跳确认', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/assets/asset-01', client })

    expect(await screen.findByRole('region', { name: '流量审查策略' })).toBeInTheDocument()
    await user.selectOptions(screen.getByLabelText('目标阶段'), 'TRAFFIC_REVIEW_MODE_STATISTICS_ONLY')
    await user.click(screen.getByRole('button', { name: '发布目标阶段' }))

    expect(await screen.findByText('等待 Edge 装载')).toBeInTheDocument()
    expect((await client.getTrafficReviewPolicy('asset-01')).policy.mode).toBe('TRAFFIC_REVIEW_MODE_STATISTICS_ONLY')
  })

  it('按完整 int64 世代序号判断 Edge 是否已经装载', async () => {
    class GenerationFixture extends ConsoleClientFixture {
      override async getAsset(assetId: string): Promise<AssetDetail> {
        const detail = await super.getAsset(assetId)
        detail.units[0].currentGenerationId = 'generation-large'
        detail.units[0].currentGenerationSeq = '9007199254740992'
        return detail
      }

      override async getTrafficReviewPolicy(assetId: string): Promise<TrafficReviewPolicyStatus> {
        const status = await super.getTrafficReviewPolicy(assetId)
        return {
          ...status,
          generationId: 'generation-large',
          generationSeq: '9007199254740993',
          edgeSupported: true,
        }
      }
    }

    const client = new GenerationFixture()
    await loginAs(client)
    renderApp({ route: '/assets/asset-01', client })

    expect(await screen.findByText('等待 Edge 装载')).toBeInTheDocument()
    expect(screen.queryByText('Edge 已生效')).toBeNull()
  })

  it('管理员可登记并删除非本机资产', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/assets', client })

    expect(await screen.findByRole('button', { name: '登记资产' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '登记资产' }))
    await user.type(screen.getByLabelText('显示名'), '旁路站点')
    await user.click(screen.getByRole('button', { name: '登记' }))

    expect(await screen.findByText('旁路站点')).toBeInTheDocument()
    const created = (await client.listAssets()).items.find((a) => a.asset.displayName === '旁路站点')
    expect(created).toBeDefined()

    const row = screen.getByText('旁路站点').closest('tr')
    expect(row).not.toBeNull()
    await user.click(row!.querySelector('button') as HTMLButtonElement)
    await user.click(await screen.findByRole('button', { name: '确认删除' }))
    await waitFor(() => {
      expect(screen.queryByText('旁路站点')).toBeNull()
    })
  })

  it('编辑资产携带当前更新时间，避免无提示覆盖并发修改', async () => {
    class VersionedAssetFixture extends ConsoleClientFixture {
      expectedUpdatedAt: string | undefined

      override async getAsset(assetId: string): Promise<AssetDetail> {
        const detail = await super.getAsset(assetId)
        detail.asset.updatedAt = '2026-08-23T01:02:03.000Z'
        return detail
      }

      override async updateAsset(assetId: string, patch: AssetPatch, expectedUpdatedAt?: string): Promise<AssetDetail> {
        this.expectedUpdatedAt = expectedUpdatedAt
        return super.updateAsset(assetId, patch, expectedUpdatedAt)
      }
    }

    const user = userEvent.setup()
    const client = new VersionedAssetFixture()
    await loginAs(client)
    renderApp({ route: '/assets/asset-01', client })

    await user.click(await screen.findByRole('button', { name: '编辑' }))
    const name = screen.getByLabelText('显示名')
    await user.clear(name)
    await user.type(name, '核心支付新名称')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(client.expectedUpdatedAt).toBe('2026-08-23T01:02:03.000Z'))
    expect((await screen.findAllByText('核心支付新名称')).length).toBeGreaterThan(0)
  })

  it('操作员不见登记与删除', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client, 'operator-chen', 'operator123456')
    renderApp({ route: '/assets', client })

    expect(await screen.findByRole('region', { name: '资产台账' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '登记资产' })).toBeNull()
    expect(screen.queryByRole('button', { name: '删除' })).toBeNull()
  })

  it('非管理员即使获授资产工具也不显示服务端角色硬门会拒绝的写入口', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client)
    await client.putGrant({
      subjectUserId: 'usr_02',
      tools: ['console.read', 'asset.create', 'asset.update', 'asset.delete', 'asset.attach', 'asset.detach'],
      bindings: [{ kind: 'asset', id: 'asset-01' }, { kind: 'asset', id: 'asset-02' }],
    })
    await loginAs(client, 'operator-chen', 'operator123456')
    const list = renderApp({ route: '/assets', client })

    expect(await screen.findByRole('region', { name: '资产台账' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '登记资产' })).toBeNull()
    expect(screen.queryByRole('button', { name: '删除' })).toBeNull()

    list.unmount()
    renderApp({ route: '/assets/asset-01', client })
    expect(await screen.findByRole('region', { name: '流量审查策略' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '编辑' })).toBeNull()
    expect(screen.queryByRole('button', { name: '绑定' })).toBeNull()
    expect(screen.queryByRole('button', { name: '解绑' })).toBeNull()
    expect(screen.getByRole('button', { name: '发布目标阶段' })).toBeDisabled()
  })

  it('操作员调用写接口被拒绝', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client, 'operator-chen', 'operator123456')
    await expect(client.createAsset({ displayName: 'x' })).rejects.toMatchObject({ code: 'permission_denied' })
    await expect(client.updateAsset('asset-01', { displayName: 'x' })).rejects.toMatchObject({ code: 'permission_denied' })
    await expect(client.deleteAsset('asset-02')).rejects.toMatchObject({ code: 'permission_denied' })
  })
})
