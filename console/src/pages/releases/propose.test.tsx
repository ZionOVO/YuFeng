// 防护策略页提交提案意图；生产不收正则。

import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ConsoleClientFixture } from '../../test/fixtures/consoleClient'
import { loginAs, renderApp } from '../../test/renderApp'

beforeEach(() => {
  sessionStorage.clear()
})

describe('提案意图', () => {
  it('防护策略页可提交 policy 意图，页面没有正则 / KIND_RULE 输入', async () => {
    const user = userEvent.setup()
    const client = new ConsoleClientFixture()
    await loginAs(client, 'operator-chen', 'operator123456')
    renderApp({ route: '/releases', client })

    await screen.findByRole('region', { name: '防护策略列表' })
    expect(screen.queryByLabelText(/正则/)).toBeNull()
    expect(screen.queryByLabelText(/KIND_RULE/)).toBeNull()
    expect(screen.queryByLabelText(/rules\/v1/)).toBeNull()

    await user.click(screen.getByRole('button', { name: '提交提案' }))
    await user.selectOptions(screen.getByLabelText('意图种类'), 'PROPOSAL_KIND_POLICY')
    await user.type(screen.getByLabelText('聚类标识'), 'clu_test')
    await user.type(screen.getByLabelText('检测键规则号'), '942100')
    await user.click(screen.getAllByLabelText('资产 asset-01')[0])
    await user.click(screen.getByRole('button', { name: '提出' }))

    await waitFor(async () => {
      const page = await client.listReleases({}, { pageSize: 200 })
      expect(page.items.some((r) => r.state === 'RELEASE_STATE_DRAFT' && r.artifact?.payloadSchema === 'policy/v1')).toBe(true)
    })
  })

  it('测试夹具生产路径无 intent 则 failed_precondition，零 draft 增量', async () => {
    const client = new ConsoleClientFixture()
    await loginAs(client, 'operator-chen', 'operator123456')
    const before = (await client.listReleases({}, { pageSize: 200 })).items.length
    await expect(
      client.proposeArtifact({
        intent: { kind: 'PROPOSAL_KIND_UNSPECIFIED' },
        scope: { assetIds: ['asset-01'], routeSelector: '' },
      }),
    ).rejects.toMatchObject({ code: 'failed_precondition' })
    const after = (await client.listReleases({}, { pageSize: 200 })).items.length
    expect(after).toBe(before)
  })
})
