import { encodeSessionTurn, latestTurnAssetIds, parseSessionTurn, turnAssetIds } from './sessionTurn'

describe('会话回合信封', () => {
  it('纯文本原样返回', () => {
    expect(parseSessionTurn('hello jarvis')).toEqual({ text: 'hello jarvis' })
  })

  it('往返保留文字、思考、工具与资产', () => {
    const raw = encodeSessionTurn({
      text: '调查回来了。',
      thinking: '先取形状。',
      tools: [{ name: 'event.list', state: 'done', assetId: 'asset-01', kv: [['绑定', 'asset-01']] }],
      gate: { title: '关 22', status: 'open', assetId: 'asset-01', releaseId: 'rel_01' },
    })
    const turn = parseSessionTurn(raw)
    expect(turn.text).toBe('调查回来了。')
    expect(turn.thinking).toBe('先取形状。')
    expect(turn.tools?.[0]?.name).toBe('event.list')
    expect(turnAssetIds(turn)).toEqual(['asset-01'])
  })

  it('坏 JSON 当纯文本', () => {
    expect(parseSessionTurn('YF/1\n{')).toEqual({ text: 'YF/1\n{' })
  })

  it('最近一次点名覆盖更早的资产', () => {
    const a = encodeSessionTurn({ tools: [{ name: 'event.get', state: 'done', assetId: 'asset-01' }] })
    const b = encodeSessionTurn({ tools: [{ name: 'event.get', state: 'done', assetId: 'asset-02' }] })
    expect(latestTurnAssetIds([a, b])).toEqual(['asset-02'])
  })
})
