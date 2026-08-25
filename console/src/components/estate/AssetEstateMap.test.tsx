import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HeroUIProvider } from '@heroui/react'
import type { ReactElement } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { createAssets } from '../../test/fixtures/data'
import { AssetEstateMap } from './AssetEstateMap'

function mount(ui: ReactElement) {
  return render(
    <HeroUIProvider>
      <MemoryRouter>
        <div className="fusionr">{ui}</div>
      </MemoryRouter>
    </HeroUIProvider>,
  )
}

describe('AssetEstateMap', () => {
  it('空列表仍画出中台岗位', () => {
    mount(<AssetEstateMap assets={[]} />)
    expect(screen.getByRole('region', { name: '资产拓扑' })).toBeInTheDocument()
    expect(screen.getByText(/还没有登记资产/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '选中中台' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '选中贾维斯' })).toBeInTheDocument()
    expect(screen.getByText(/贾维斯离线/)).toBeInTheDocument()
  })

  it('用 unitIds 画绑定，未绑定资产可选中并提示无边', async () => {
    const user = userEvent.setup()
    mount(<AssetEstateMap assets={createAssets()} />)

    expect(screen.getByLabelText('可键盘选择的拓扑节点')).toBeInTheDocument()
    expect(screen.getByText(/按 labels.biz 弱分组/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '选中资产 legacy-erp' }))
    expect(screen.getByLabelText('选中资产')).toBeInTheDocument()
    expect(screen.getByText('未绑定单元，没有保护边')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '打开详情' })).toHaveAttribute('href', '/assets/asset-03')
  })

  it('紧凑密度点击轨道会打开资产', async () => {
    const user = userEvent.setup()
    const onOpen = vi.fn()
    mount(<AssetEstateMap assets={createAssets()} density="compact" onOpenAsset={onOpen} />)
    await user.click(screen.getByRole('button', { name: '选中资产 core-payments' }))
    expect(onOpen).toHaveBeenCalledWith('asset-01')
  })

  it('点选贾维斯说明只连中台，不把它画成资产', async () => {
    const user = userEvent.setup()
    const onJarvis = vi.fn()
    mount(
      <AssetEstateMap
        assets={createAssets()}
        plane={{ jarvisOnline: true }}
        onOpenJarvis={onJarvis}
      />,
    )
    expect(screen.getByText(/贾维斯在线/)).toBeInTheDocument()
    expect(screen.getByText(/Edge 0\/0 在线/)).toBeInTheDocument()
    expect(screen.getByText(/Edge 未登记 2/)).toBeInTheDocument()
    expect(screen.getByText(/ModelSide 0\/0 在线/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '选中贾维斯' }))
    expect(screen.getByLabelText('选中岗位')).toBeInTheDocument()
    expect(screen.getByText(/只连中台/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '打开编排' })).toHaveAttribute('href', '/agent')
  })

  it('Host 必须同时健康且心跳未过期才显示在线', async () => {
    const user = userEvent.setup()
    const assets = createAssets()
    const unit = assets[0].units[0]
    unit.kind = 'host'
    unit.lastHeartbeatAt = '2020-01-01T00:00:00Z'
    mount(<AssetEstateMap assets={assets} />)

    expect(screen.getByText(/Host 0\/1 在线/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '选中资产 core-payments' }))
    expect(screen.getByText(/Host unit-edge-01：离线/)).toBeInTheDocument()
  })

  it('工具链点名的资产会进选中轨道', () => {
    mount(<AssetEstateMap assets={createAssets()} focusAssetIds={['asset-01']} />)
    expect(screen.getByRole('button', { name: '选中资产 core-payments' })).toBeInTheDocument()
  })

  it('选中资产时展示图例外形名', async () => {
    const user = userEvent.setup()
    mount(<AssetEstateMap assets={createAssets()} />)
    await user.click(screen.getByRole('button', { name: '选中资产 mall-gateway' }))
    expect(screen.getByText(/Web 服务/)).toBeInTheDocument()
  })

  it('点击画布空白会取消实体选中', async () => {
    const user = userEvent.setup()
    const onClear = vi.fn()
    mount(<AssetEstateMap assets={createAssets()} onClearSelection={onClear} />)
    await user.click(screen.getByRole('button', { name: '选中资产 mall-gateway' }))
    expect(screen.getByLabelText('选中资产')).toBeInTheDocument()
    fireEvent.pointerDown(screen.getByLabelText('资产拓扑画布'), { clientX: 9999, clientY: 9999 })
    expect(screen.queryByLabelText('选中资产')).toBeNull()
    expect(onClear).toHaveBeenCalledTimes(1)
  })

  it('受管 Agent 在中台侧显示并把档案标识回传给管理页', async () => {
    const user = userEvent.setup()
    const onSelectAgent = vi.fn()
    mount(
      <AssetEstateMap
        assets={createAssets()}
        plane={{ managedAgents: [{ agentId: 'profile-review', displayName: '流量审查员', enabled: true }] }}
        onSelectAgent={onSelectAgent}
      />,
    )
    await user.click(screen.getByRole('button', { name: '选中流量审查员' }))
    expect(onSelectAgent).toHaveBeenCalledWith('profile-review')
    expect(screen.getByText(/受管 Agent/)).toBeInTheDocument()
  })

  it('缩放按钮会改当前比例，复位后回到适配', async () => {
    const user = userEvent.setup()
    mount(<AssetEstateMap assets={createAssets()} />)
    const label = screen.getByLabelText('当前缩放')
    await user.click(screen.getByRole('button', { name: '放大' }))
    await user.click(screen.getByRole('button', { name: '放大' }))
    const zoomed = Number.parseInt(label.textContent ?? '0', 10)
    expect(zoomed).toBeGreaterThan(0)
    await user.click(screen.getByRole('button', { name: '缩小' }))
    const shrunk = Number.parseInt(label.textContent ?? '0', 10)
    expect(shrunk).toBeLessThan(zoomed)
    await user.click(screen.getByRole('button', { name: '复位' }))
    await waitFor(() => {
      const reset = Number.parseInt(label.textContent ?? '0', 10)
      expect(reset).not.toBe(zoomed)
    })
  })

  it('扩大后舞台接近窗口高度并缓动居中，收起恢复', async () => {
    const user = userEvent.setup()
    vi.stubGlobal('innerHeight', 900)
    const scrollTo = vi.fn()
    vi.stubGlobal('scrollTo', scrollTo)
    mount(<AssetEstateMap assets={createAssets()} />)
    const expand = screen.getByRole('button', { name: '扩大' })
    expect(expand).toHaveAttribute('aria-expanded', 'false')
    await user.click(expand)
    const collapse = screen.getByRole('button', { name: '收起' })
    expect(collapse).toHaveAttribute('aria-expanded', 'true')
    expect(document.querySelector('.yf-estate--expanded')).toBeInTheDocument()
    const stage = document.querySelector('.yf-estate-stage') as HTMLElement
    expect(Number.parseInt(stage.style.height, 10)).toBeGreaterThanOrEqual(900 - 32 - 120)
    await waitFor(() => {
      expect(scrollTo).toHaveBeenCalled()
    })
    expect(scrollTo.mock.calls[0]?.[0]).toEqual(expect.objectContaining({ behavior: 'smooth' }))
    await user.click(collapse)
    expect(screen.getByRole('button', { name: '扩大' })).toHaveAttribute('aria-expanded', 'false')
    expect(document.querySelector('.yf-estate--expanded')).toBeNull()
    expect((document.querySelector('.yf-estate-stage') as HTMLElement).style.height).toBe('')
    vi.unstubAllGlobals()
  })
})
