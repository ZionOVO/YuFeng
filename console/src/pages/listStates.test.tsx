// 列表页四态（loading/empty/error/数据）与游标分页、筛选的行为测试，覆盖 AssetsPage 与 EventsPage。
// 慢响应/瞬时故障用包装 ConsoleClientFixture 的 Proxy 实现，不改动 ConsoleClientFixture 本体。

import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ConsoleClient } from '../api/client'
import { networkError } from '../api/errors'
import { fakeHash, FIXTURE_BLOCK_EVENT_ID } from '../test/fixtures/data'
import { ConsoleClientFixture } from '../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../test/renderApp'

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms))

/** 给全部方法加固定延迟的包装，用于观察 loading 态先于数据出现。 */
function withDelay(client: ConsoleClientFixture, ms: number): ConsoleClient {
  return new Proxy(client, {
    get(target, prop, receiver) {
      const value: unknown = Reflect.get(target, prop, receiver)
      if (typeof value !== 'function') return value
      const fn = value as (...args: unknown[]) => unknown
      return async (...args: unknown[]) => {
        await sleep(ms)
        return Reflect.apply(fn, target, args)
      }
    },
  })
}

/** listAssets 首次抛网络错误、之后委托给 ConsoleClientFixture：模拟瞬时网络故障 + 重试恢复。 */
function failFirstListAssets(client: ConsoleClientFixture): { client: ConsoleClient; listCalls: () => number } {
  let calls = 0
  const proxy = new Proxy(client, {
    get(target, prop, receiver) {
      if (prop !== 'listAssets') return Reflect.get(target, prop, receiver)
      return async (...args: Parameters<ConsoleClient['listAssets']>) => {
        calls += 1
        if (calls === 1) throw networkError(new Error('connection refused'))
        return target.listAssets(...args)
      }
    },
  })
  return { client: proxy, listCalls: () => calls }
}

beforeEach(() => {
  sessionStorage.clear()
})

describe('列表四态（AssetsPage）', () => {
  it('loading 先于数据出现', async () => {
    const fixture = new ConsoleClientFixture()
    await loginAs(fixture)
    renderApp({ route: '/assets', client: withDelay(fixture, 50) })

    // 数据就绪前不出现资产行；先出现 loading，再出现数据
    expect(screen.queryByText('core-payments')).toBeNull()
    expect(await screen.findByText('加载中')).toBeInTheDocument()
    expect(screen.queryByText('core-payments')).toBeNull()
    expect(await screen.findByText('core-payments')).toBeInTheDocument()
    expect(screen.queryByText('加载中')).toBeNull()
  })

  it('空态：筛选必然无匹配时出现空态文案', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client)
    renderApp({ route: '/assets', client })
    await screen.findByText('core-payments')

    // AssetsPage 筛选变化会整页回 loading（搜索框随整页重挂载，逐键输入会丢焦点），
    // 这里用一次性 change 把关键词改到位
    fireEvent.change(screen.getByLabelText('搜索资产'), { target: { value: 'zzz-no-match' } })

    expect(await screen.findByText('没有符合条件的资产')).toBeInTheDocument()
    expect(screen.queryByText('core-payments')).toBeNull()
  })

  it('网络错误：错误态 + 重试按钮，重试后恢复列表', async () => {
    const user = userEvent.setup()
    const fixture = new ConsoleClientFixture()
    await loginAs(fixture)
    const { client, listCalls } = failFirstListAssets(fixture)
    renderApp({ route: '/assets', client })

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('加载失败')
    expect(alert).toHaveTextContent('connection refused')

    await user.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByText('core-payments')).toBeInTheDocument()
    // 台账与拓扑各打一次 ListAssets；第一次台账失败、拓扑成功，重试只补台账
    expect(listCalls()).toBe(3)
  })
})

describe('事件列表分页与筛选（EventsPage）', () => {
  it('资产详情入口通过查询参数预置资产筛选', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client)
    const listEvents = vi.spyOn(client, 'listEvents')

    renderApp({ route: '/events?assetId=asset-02', client })

    await waitFor(() => {
      expect(listEvents).toHaveBeenCalledWith(expect.objectContaining({ assetId: 'asset-02' }), expect.any(Object))
    })
  })

  it('游标分页：下一页出现与第一页不同的事件，上一页可点回', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    // 期望事件 id 取自 client 的真实分页（EventsPage 的 PAGE_SIZE 为 25）
    const page1 = await client.listEvents({}, { pageSize: 25 })
    const page2 = await client.listEvents({}, { pageSize: 25, pageToken: page1.nextPageToken })
    expect(page1.items.length).toBeGreaterThan(0)
    expect(page2.items.length).toBeGreaterThan(0)

    renderApp({ route: '/events', client })
    await screen.findByLabelText(`查看事件 ${page1.items[0].id}`)
    expect(screen.getByText('第 1 页')).toBeInTheDocument()
    expect(screen.queryByLabelText(`查看事件 ${page2.items[0].id}`)).toBeNull()

    await user.click(screen.getByRole('button', { name: '下一页' }))
    await screen.findByLabelText(`查看事件 ${page2.items[0].id}`)
    expect(screen.getByText('第 2 页')).toBeInTheDocument()
    expect(screen.queryByLabelText(`查看事件 ${page1.items[0].id}`)).toBeNull()

    await user.click(screen.getByRole('button', { name: '上一页' }))
    await screen.findByLabelText(`查看事件 ${page1.items[0].id}`)
    expect(screen.getByText('第 1 页')).toBeInTheDocument()
  })

  it('按判定筛选 BLOCK：只剩 client 判定的 BLOCK 事件集合', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client)
    const expected = await client.listEvents({ verdict: 'VERDICT_BLOCK' }, { pageSize: 200 })
    const firstPage = await client.listEvents({}, { pageSize: 25 })

    renderApp({ route: '/events', client })
    await screen.findByLabelText(`查看事件 ${firstPage.items[0].id}`)

    // HeroUI Select：触发器是 button，选项在 portal 的 listbox 里
    await user.click(screen.getByRole('button', { name: /按判定筛选/ }))
    await user.click(await screen.findByRole('option', { name: 'BLOCK' }))

    await waitFor(() => {
      expect(screen.getAllByLabelText(/^查看事件 /)).toHaveLength(expected.items.length)
    })
    expect(screen.getByLabelText(`查看事件 ${FIXTURE_BLOCK_EVENT_ID}`)).toBeInTheDocument()
    // i=1 的事件是 ALLOW，筛选后不应出现
    expect(screen.queryByLabelText(`查看事件 evt_${fakeHash('event-1')}`)).toBeNull()
  })

  it('protojson 省略 detections 时事件列表仍渲染行', async () => {
    const fixture = new ConsoleClientFixture()
    await loginAs(fixture)
    const first = (await fixture.listEvents({}, { pageSize: 25 })).items[0]
    const client = new Proxy(fixture, {
      get(target, prop, receiver) {
        if (prop !== 'listEvents') return Reflect.get(target, prop, receiver)
        return async (...args: Parameters<ConsoleClient['listEvents']>) => {
          const page = await target.listEvents(...args)
          return {
            ...page,
            items: page.items.map((e) => {
              const copy = { ...e }
              delete (copy as { detections?: unknown }).detections
              return copy
            }),
          }
        }
      },
    })
    renderApp({ route: '/events', client })
    expect(await screen.findByLabelText(`查看事件 ${first.id}`)).toBeInTheDocument()
    expect(screen.getByLabelText('事件列表')).toBeInTheDocument()
  })
})

describe('资产列表 protojson 省略字段', () => {
  it('省略 unitIds 时资产列表仍渲染行', async () => {
    const fixture = new ConsoleClientFixture()
    await loginAs(fixture)
    const client = new Proxy(fixture, {
      get(target, prop, receiver) {
        if (prop !== 'listAssets') return Reflect.get(target, prop, receiver)
        return async (...args: Parameters<ConsoleClient['listAssets']>) => {
          const page = await target.listAssets(...args)
          return {
            ...page,
            items: page.items.map((item) => {
              const copy = { ...item, asset: { ...item.asset } }
              delete (copy as { unitIds?: unknown }).unitIds
              delete (copy.asset as { labels?: unknown }).labels
              return copy
            }),
          }
        }
      },
    })
    renderApp({ route: '/assets', client })
    expect(await screen.findByText('core-payments')).toBeInTheDocument()
    expect(screen.getByLabelText('资产列表')).toBeInTheDocument()
  })
})
