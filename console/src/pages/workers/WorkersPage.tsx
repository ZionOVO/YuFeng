import { Button, Select, SelectItem } from '@heroui/react'
import { useCallback, useMemo, useState } from 'react'
import { PageSizeMax } from '../../api/limits'
import { useAuth } from '../../auth/useAuth'
import { WorkerEnrollmentCard, WorkerRecordCard } from '../../components/cases/CaseCards'
import { StateView } from '../../components/ui'
import { useCursorCollection, type CursorCollection } from '../../components/useCursorCollection'

function ContinueLoading({ label, collection }: { label: string; collection: Pick<CursorCollection<unknown>, 'items' | 'nextPageToken' | 'loadingMore' | 'loadMoreError' | 'loadMore'> }) {
  const actionLabel = label === 'Worker' ? ` ${label}` : label
  if (collection.nextPageToken === '') {
    if (collection.loadMoreError !== null) {
      return <p className="px-4 pb-4 text-center text-xs text-[#ff8c81]" role="alert">{collection.loadMoreError.message}</p>
    }
    return collection.items.length > 0
      ? <p className="px-4 pb-4 text-center text-xs text-[#71888c]">已加载全部 {label}（{collection.items.length} 个）</p>
      : null
  }
  return (
    <div className="flex flex-col items-center gap-2 px-4 pb-4">
      {collection.loadMoreError !== null && <p className="text-xs text-[#ff8c81]" role="alert">{collection.loadMoreError.message}</p>}
      <Button size="sm" variant="bordered" isLoading={collection.loadingMore} isDisabled={collection.loadingMore} onPress={collection.loadMore}>
        {collection.loadMoreError === null ? `继续加载${actionLabel}` : `重试加载${actionLabel}`}
      </Button>
    </div>
  )
}

export function WorkersPage() {
  const { client } = useAuth()
  const [assetId, setAssetId] = useState('')
  const loadAssets = useCallback((pageToken: string) => client.listAssets({}, { pageSize: PageSizeMax, pageToken }), [client])
  const loadWorkers = useCallback((pageToken: string) => client.listWorkers({ pageSize: PageSizeMax, pageToken }), [client])
  const loadEnrollments = useCallback(
    (pageToken: string) => client.listWorkerEnrollments('pending', { pageSize: PageSizeMax, pageToken }),
    [client],
  )
  const assets = useCursorCollection(loadAssets)
  const workers = useCursorCollection(loadWorkers)
  const enrollments = useCursorCollection(loadEnrollments)
  const assetOptions = useMemo(() => assets.items.map((item) => item.asset), [assets.items])
  const bindings = assetId === '' ? [] : [`asset:${assetId}`]

  if (assets.status === 'error') return <StateView kind="error" message={assets.error?.message ?? '资产读取失败'} onRetry={assets.reload} />
  return (
    <div className="space-y-4">
      <section className="fs-panel" aria-label="Worker 注册说明">
        <div className="fs-panel-head"><div><p className="fs-panel-title">调查执行进程</p><p className="fs-panel-sub">客户端主动申请 · 管理员核对 · 加密激活</p></div></div>
        <div className="grid gap-3 p-4 lg:grid-cols-[minmax(0,1fr)_minmax(16rem,0.6fr)]">
          <p className="text-sm leading-6 text-[#9bb0b2]">外部 agentd 在本机生成身份密钥和 X25519 激活密钥后主动申请。管理员只核对不可变主机清单；批准后，证书和一次性引导令牌只存在于客户端公钥加密的激活包中，控制台不读取明文。</p>
          <Select
            items={assetOptions}
            label="批准时绑定的初始资产"
            placeholder="先选择一个精确资产"
            selectedKeys={assetId === '' ? [] : [assetId]}
            onSelectionChange={(keys) => setAssetId(String([...keys][0] ?? ''))}
            size="sm"
            radius="md"
          >
            {(asset) => <SelectItem key={asset.id}>{asset.displayName}</SelectItem>}
          </Select>
        </div>
        <ContinueLoading label="资产" collection={assets} />
      </section>

      <section className="fs-panel" aria-label="待核对 Worker">
        <div className="fs-panel-head"><div><p className="fs-panel-title">待核对申请</p><p className="fs-panel-sub">{enrollments.items.length} 个</p></div></div>
        <div className="grid gap-3 p-4 xl:grid-cols-2">
          {enrollments.status === 'error' ? <StateView kind="error" message={enrollments.error?.message ?? '注册申请读取失败'} onRetry={enrollments.reload} />
            : enrollments.status === 'loading' ? <StateView kind="loading" /> : enrollments.items.length === 0
            ? <StateView kind="empty" title="暂无待核对申请" message="客户端提交申请后会出现在这里。" />
            : enrollments.items.map((enrollment) => <WorkerEnrollmentCard key={enrollment.enrollmentId} enrollment={enrollment} bindings={bindings} onDecided={enrollments.reload} />)}
        </div>
        {enrollments.status === 'ok' && <ContinueLoading label="登记申请" collection={enrollments} />}
      </section>

      <section className="fs-panel" aria-label="已登记 Worker">
        <div className="fs-panel-head"><div><p className="fs-panel-title">已登记执行池</p><p className="fs-panel-sub">沙箱挑战决定调查资格</p></div></div>
        <div className="grid gap-3 p-4 xl:grid-cols-2">
          {workers.status === 'error' ? <StateView kind="error" message={workers.error?.message ?? '执行池读取失败'} onRetry={workers.reload} />
            : workers.status === 'loading' ? <StateView kind="loading" /> : workers.items.length === 0
            ? <StateView kind="empty" title="暂无执行进程" message="标准部署会登记一个中央 agentd。" />
            : workers.items.map((worker) => <WorkerRecordCard key={worker.workerId} worker={worker} />)}
        </div>
        {workers.status === 'ok' && <ContinueLoading label="Worker" collection={workers} />}
      </section>
    </div>
  )
}
