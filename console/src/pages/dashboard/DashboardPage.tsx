// 仪表盘：关键指标卡 + 资产拓扑 + 防护策略状态分布 + 最近事件。
// 数据来自 ConsoleService.Dashboard、ListAssets（拓扑，pageSize=上限）、ListEvents（pageSize=8）
// 与 GetOnboarding（仅贾维斯在线）。int64 字段是 string，一律 Number() 后再用。

import { Spinner } from '@heroui/react'
import { Link, useNavigate } from 'react-router-dom'
import { hasCode } from '../../api/errors'
import { PageSizeMax } from '../../api/limits'
import type { ReleaseState } from '../../api/types'
import { useAuth } from '../../auth/useAuth'
import { useJarvisSession } from '../../components/chat/useJarvisSession'
import { AssetEstateMap } from '../../components/estate/AssetEstateMap'
import { formatTime } from '../../components/format'
import { ReleaseStateBadge, StateView, VerdictBadge } from '../../components/ui'
import { useAsyncData } from '../../components/useAsyncData'

/* 发布生命周期六态管道顺序：draft → signed → shadow → canary → enforce → retired */
const PIPELINE: { state: ReleaseState; name: string }[] = [
  { state: 'RELEASE_STATE_DRAFT', name: '草稿' },
  { state: 'RELEASE_STATE_SIGNED', name: '已签名' },
  { state: 'RELEASE_STATE_SHADOW', name: '影子' },
  { state: 'RELEASE_STATE_CANARY', name: '小比例' },
  { state: 'RELEASE_STATE_ENFORCE', name: '全量' },
  { state: 'RELEASE_STATE_RETIRED', name: '已退休' },
]

/** 指标卡；amber 时整卡换琥珀警示色（fs-metric--amber）。 */
function Metric({ label, value, hint, amber = false }: { label: string; value: number; hint?: string; amber?: boolean }) {
  return (
    <div className={`fs-metric${amber ? ' fs-metric--amber' : ''}`}>
      <p className="fs-metric-label">{label}</p>
      <p className="fs-metric-value">{value}</p>
      {hint !== undefined && <p className="fs-metric-hint">{hint}</p>}
    </div>
  )
}

export function DashboardPage() {
  const { client, onboarding } = useAuth()
  const jarvis = useJarvisSession()
  const navigate = useNavigate()
  const dash = useAsyncData(() => client.dashboard(), [client])
  const events = useAsyncData(() => client.listEvents({}, { pageSize: 8 }), [client])
  const estate = useAsyncData(() => client.listAssets({}, { pageSize: PageSizeMax }), [client])

  if (dash.status === 'loading') {
    return <StateView kind="loading" />
  }
  if (dash.status === 'error' || dash.data === null) {
    const err = dash.error
    return (
      <StateView
        kind={hasCode(err, 'permission_denied') ? 'denied' : 'error'}
        message={err !== null ? `${err.message}（${err.code}）` : undefined}
        onRetry={() => {
          dash.reload()
          events.reload()
        }}
      />
    )
  }

  const d = dash.data
  const eventsTotal = Number(d.events24hTotal)
  const eventsBlocked = Number(d.events24hBlocked)
  const degradedUnits = Number(d.degradedUnits)

  return (
    <>
      <section className="fs-metrics" aria-label="关键指标">
        <Metric label="资产总数" value={Number(d.assetsTotal)} hint="含旁路登记资产" />
        <Metric label="降级单元" value={degradedUnits} amber={degradedUnits > 0} />
        <Metric label="24H 事件" value={eventsTotal} />
        <Metric
          label="24H 拦截"
          value={eventsBlocked}
          hint={eventsTotal > 0 ? `阻断率 ${((eventsBlocked / eventsTotal) * 100).toFixed(1)}%` : undefined}
        />
        <Metric label="24H 模型告警" value={Number(d.modelAlerts24h)} amber={Number(d.modelAlerts24h) > 0} />
        <Metric label="将到期策略" value={Number(d.pendingRetireSoon)} />
      </section>

      {estate.status === 'error' && estate.error !== null ? (
        <StateView
          kind={hasCode(estate.error, 'permission_denied') ? 'denied' : 'error'}
          message={`${estate.error.message}（${estate.error.code}）`}
          onRetry={estate.reload}
        />
      ) : estate.data !== null ? (
        <AssetEstateMap
          assets={estate.data.items}
          plane={{
            jarvisOnline: onboarding?.jarvisOnline,
          }}
          truncated={estate.data.nextPageToken !== ''}
          density="compact"
          focusAssetIds={jarvis.focusAssetIds}
          onOpenAsset={(id) => navigate(`/assets/${id}`)}
          onOpenJarvis={() => jarvis.setDockOpen(true)}
          onSelectAsset={(id) => jarvis.setContextLabel(id)}
        />
      ) : (
        <section className="fs-panel flex items-center justify-center py-16" aria-label="资产拓扑">
          <Spinner size="lg" aria-label="拓扑加载中" />
        </section>
      )}

      <section className="fs-grid2">
        <div className="fs-panel" aria-label="防护策略状态分布">
          <div className="fs-panel-head">
            <p className="fs-panel-title">防护策略状态分布</p>
            <p className="fs-panel-sub">RELEASE PIPELINE</p>
          </div>
          <div>
            {PIPELINE.map(({ state, name }) => (
              <button
                key={state}
                type="button"
                className="fs-row w-full cursor-pointer text-left transition-colors hover:bg-[#161c21]"
                onClick={() => navigate('/releases')}
              >
                <ReleaseStateBadge state={state} />
                <span className="fs-row-note">{name}</span>
                <span className="fs-row-count">{Number(d.releasesByState[state] ?? '0')}</span>
              </button>
            ))}
          </div>
        </div>

        {events.status === 'ok' && events.data !== null ? (
          <div className="fs-panel" aria-label="最近事件">
            <div className="fs-panel-head">
              <p className="fs-panel-title">最近事件</p>
              <p className="fs-panel-sub">LATEST EVENTS</p>
            </div>
            <div>
              {events.data.items.length === 0 && <p className="fs-row text-[#8b98a1]">暂无事件</p>}
              {events.data.items.map((e) => (
                <div key={e.id} className="fs-row">
                  <span className="fs-row-time">{formatTime(e.occurredAt)}</span>
                  <span className="fs-mono text-[11px]">{e.id}</span>
                  <span className="fs-row-note">{e.assetId}</span>
                  <VerdictBadge verdict={e.verdict} />
                  <Link to={`/events/${e.id}`} className="shrink-0 text-xs text-[#62e6a7] hover:underline">
                    查看
                  </Link>
                </div>
              ))}
            </div>
          </div>
        ) : events.status === 'loading' ? (
          <div className="fs-panel flex items-center justify-center py-16" aria-label="最近事件">
            <Spinner size="lg" aria-label="事件加载中" />
          </div>
        ) : (
          <StateView
            kind={hasCode(events.error, 'permission_denied') ? 'denied' : 'error'}
            message={events.error !== null ? `${events.error.message}（${events.error.code}）` : undefined}
            onRetry={events.reload}
          />
        )}
      </section>
    </>
  )
}
