// 模型网关管理页：引导完成后管理员改槽，并看接入主机数与近窗成功率（docs/api.md §19.4）。
// 同时只接一条模型供应商槽，不是多供应商并行路由。

import { useEffect, useState } from 'react'
import { Button, Input, Select, SelectItem, Table, TableBody, TableCell, TableColumn, TableHeader, TableRow } from '@heroui/react'
import { isApiError } from '../../api/errors'
import { MODEL_DIALECTS, normalizeDialect } from '../../api/modelDialect'
import type { ModelDialect, ModelGatewayStatus } from '../../api/types'
import { useAuth } from '../../auth/useAuth'
import { formatTime } from '../../components/format'
import { Badge, StateView } from '../../components/ui'
import { useAsyncData } from '../../components/useAsyncData'

type BadgeTone = 'green' | 'amber' | 'red' | 'mute'

const STATUS_BADGE: Record<ModelGatewayStatus, { label: string; tone: BadgeTone }> = {
  MODEL_GATEWAY_STATUS_UNSPECIFIED: { label: '未知', tone: 'mute' },
  MODEL_GATEWAY_STATUS_UNCONFIGURED: { label: '未配置', tone: 'mute' },
  MODEL_GATEWAY_STATUS_READY: { label: '已就绪', tone: 'mute' },
  MODEL_GATEWAY_STATUS_LIVE: { label: '正常', tone: 'green' },
  MODEL_GATEWAY_STATUS_DEGRADED: { label: '降级', tone: 'amber' },
  MODEL_GATEWAY_STATUS_DOWN: { label: '不可用', tone: 'red' },
}

function successRate(total: number, ok: number): string {
  if (total <= 0) return '—'
  return `${((ok / total) * 100).toFixed(1)}%`
}

function providerTone(total: number, ok: number): BadgeTone {
  if (total <= 0) return 'mute'
  if (ok === total) return 'green'
  if (ok === 0) return 'red'
  return 'amber'
}

export function ModelPage() {
  const { client } = useAuth()
  const gate = useAsyncData(() => client.getModelGateway(), [client])
  const [baseUrl, setBaseUrl] = useState('')
  const [model, setModel] = useState('')
  const [dialect, setDialect] = useState<ModelDialect>('MODEL_DIALECT_OPENAI_CHAT')
  const [secret, setSecret] = useState('')
  const [busy, setBusy] = useState<'save' | 'probe' | null>(null)
  const [formError, setFormError] = useState<string | null>(null)
  const [probeNote, setProbeNote] = useState<string | null>(null)

  useEffect(() => {
    if (gate.data === null) return
    // 服务端配置是表单首次与保存后刷新的权威快照。
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setBaseUrl(gate.data.baseUrl)
    setModel(gate.data.model)
    setDialect(normalizeDialect(gate.data.dialect))
  }, [gate.data])

  if (gate.status === 'loading' && gate.data === null) {
    return <StateView kind="loading" />
  }
  if (gate.status === 'error' || gate.data === null) {
    return (
      <StateView
        kind="error"
        message={gate.error !== null ? `${gate.error.message}（${gate.error.code}）` : undefined}
        onRetry={gate.reload}
      />
    )
  }

  const g = gate.data
  const total = Number(g.callsTotal)
  const ok = Number(g.callsOk)
  const status = STATUS_BADGE[g.status] ?? STATUS_BADGE.MODEL_GATEWAY_STATUS_UNSPECIFIED
  const windowHours = Math.max(1, Math.round(Number(g.windowSeconds) / 3600))

  const save = async () => {
    setBusy('save')
    setFormError(null)
    setProbeNote(null)
    try {
      await client.updateModelGateway({ baseUrl, secret, model, dialect })
      setSecret('')
      gate.reload()
    } catch (e) {
      setFormError(isApiError(e) ? `${e.message}（${e.code}）` : '保存失败，请重试')
    } finally {
      setBusy(null)
    }
  }

  const probe = async () => {
    setBusy('probe')
    setFormError(null)
    setProbeNote(null)
    try {
      const out = await client.probeModelGateway()
      setProbeNote(out.ok ? '探测成功，已记入近窗调用' : out.lastError)
      gate.reload()
    } catch (e) {
      setFormError(isApiError(e) ? `${e.message}（${e.code}）` : '探测失败，请重试')
      gate.reload()
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="model-gateway-page">
      <section className="fs-metrics model-gateway-metrics" aria-label="模型网关状态">
        <div className="fs-metric model-gateway-metric">
          <p className="fs-metric-label">服务状态</p>
          <p className="fs-metric-value model-gateway-metric-value">
            <Badge label={status.label} tone={status.tone} />
          </p>
          <p className="fs-metric-hint model-gateway-metric-hint" title={g.status}>{g.status}</p>
        </div>
        <div className="fs-metric model-gateway-metric">
          <p className="fs-metric-label">接入主机</p>
          <p className="fs-metric-value model-gateway-metric-value">{g.providerCount}</p>
          <p className="fs-metric-hint model-gateway-metric-hint">近 {windowHours} 小时出现过的不同端点主机（含当前槽）</p>
        </div>
        <div className={`fs-metric model-gateway-metric${g.status === 'MODEL_GATEWAY_STATUS_DEGRADED' || g.status === 'MODEL_GATEWAY_STATUS_DOWN' ? ' fs-metric--amber' : ''}`}>
          <p className="fs-metric-label">近窗成功率</p>
          <p className="fs-metric-value model-gateway-metric-value">{successRate(total, ok)}</p>
          <p className="fs-metric-hint model-gateway-metric-hint">
            {ok}/{total} 次成功
          </p>
        </div>
        <div className="fs-metric model-gateway-metric">
          <p className="fs-metric-label">最近调用</p>
          <time className="fs-metric-value model-gateway-metric-value model-gateway-metric-time" dateTime={g.lastCallAt} title={formatTime(g.lastCallAt)}>
            {formatTime(g.lastCallAt)}
          </time>
          <p className="fs-metric-hint model-gateway-metric-hint" title={g.lastError}>{g.lastError !== '' ? g.lastError : '无最近错误'}</p>
        </div>
      </section>

      <section className="fs-panel" aria-label="接入主机">
        <div className="fs-panel-head">
          <div>
            <p className="fs-panel-title">接入主机</p>
            <p className="fs-panel-sub">同时只接一条槽。下表是近窗出现过的主机，不是并行路由。</p>
          </div>
        </div>
        <div
          className="model-provider-scroll"
          role="region"
          aria-label="接入主机表格，可横向滚动"
          tabIndex={0}
          onKeyDown={(event) => {
            if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
            event.preventDefault();
            event.currentTarget.scrollLeft += event.key === "ArrowRight" ? 96 : -96;
          }}
        >
          <Table aria-label="接入主机列表" removeWrapper className="fs-table-tight model-provider-table">
            <TableHeader>
              <TableColumn>主机</TableColumn>
              <TableColumn>调用</TableColumn>
              <TableColumn>成功</TableColumn>
              <TableColumn>成功率</TableColumn>
              <TableColumn>最近</TableColumn>
            </TableHeader>
            <TableBody emptyContent="近窗没有调用记录">
              {g.providers.map((p) => {
                const pTotal = Number(p.callsTotal)
                const pOk = Number(p.callsOk)
                return (
                  <TableRow key={p.host}>
                    <TableCell className="fs-mono">{p.host || '—'}</TableCell>
                    <TableCell>{pTotal}</TableCell>
                    <TableCell>{pOk}</TableCell>
                    <TableCell>
                      <Badge label={successRate(pTotal, pOk)} tone={providerTone(pTotal, pOk)} />
                    </TableCell>
                    <TableCell className="fs-mono text-xs">{formatTime(p.lastAt)}</TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      </section>

      <section className="fs-panel" aria-label="调整配置">
        <div className="fs-panel-head">
          <div>
            <p className="fs-panel-title">调整配置</p>
            <p className="fs-panel-sub">保存只改当前槽，不退引导状态。密钥留空则保留已保存值。</p>
          </div>
        </div>
        <div className="model-config-form">
          <div className="model-config-field model-config-field--full">
            <Input label="模型端点" radius="md" value={baseUrl} onValueChange={setBaseUrl} isRequired />
          </div>
          <div className="model-config-field">
            <Input label="模型名" radius="md" value={model} onValueChange={setModel} />
          </div>
          <div className="model-config-field">
            <Select
              label="模型方言"
              radius="md"
              selectedKeys={[dialect]}
              onChange={(e) => setDialect(normalizeDialect(e.target.value))}
            >
              {MODEL_DIALECTS.map((d) => (
                <SelectItem key={d.value}>{d.label}</SelectItem>
              ))}
            </Select>
          </div>
          <div className="model-config-field model-config-field--full">
            <Input
              label="模型密钥"
              type="password"
              radius="md"
              value={secret}
              onValueChange={setSecret}
              autoComplete="off"
              description={g.hasSecret ? `已保存 ${g.secretHint}，覆盖请重新输入` : '只写不回读'}
            />
          </div>
          <div className="model-config-feedback" aria-live="polite">
            {formError !== null && <p className="text-xs text-[#ff746c]">{formError}</p>}
            {probeNote !== null && <p className="text-xs text-[#62e6a7]">{probeNote}</p>}
          </div>
          <div className="model-config-actions" role="group" aria-label="模型网关操作">
            <Button color="primary" radius="md" isLoading={busy === 'save'} isDisabled={busy !== null || baseUrl === ''} onPress={() => void save()}>
              保存配置
            </Button>
            <Button variant="bordered" radius="md" isLoading={busy === 'probe'} isDisabled={busy !== null || !g.hasSecret} onPress={() => void probe()}>
              探测连通
            </Button>
          </div>
        </div>
      </section>
    </div>
  )
}
