// 人工 Edge 接入卡只签发配置制品并展示非敏感安装坐标；不执行进程或容器操作。

import { useState } from 'react'
import { Button, Input, Modal, ModalBody, ModalContent, ModalFooter, ModalHeader, Select, SelectItem, useDisclosure } from '@heroui/react'
import type { ConsoleClient, EdgeEnrollmentInput } from '../../api/client'
import { isApiError } from '../../api/errors'
import type { EdgeEnrollment, EdgeEnrollmentStatus } from '../../api/types'
import { formatTime } from '../../components/format'
import { Badge } from '../../components/ui'

const STATUS: Record<EdgeEnrollmentStatus, { label: string; tone: 'green' | 'amber' | 'red' | 'mute' }> = {
  EDGE_ENROLLMENT_STATUS_UNSPECIFIED: { label: '未知', tone: 'mute' },
  EDGE_ENROLLMENT_STATUS_WAITING_FOR_REGISTRATION: { label: '等待注册', tone: 'amber' },
  EDGE_ENROLLMENT_STATUS_ONLINE: { label: '在线且收敛', tone: 'green' },
  EDGE_ENROLLMENT_STATUS_OUT_OF_SYNC: { label: '制品未收敛', tone: 'amber' },
  EDGE_ENROLLMENT_STATUS_OFFLINE: { label: '离线', tone: 'red' },
}

function validCIDR(value: string): boolean {
  const slash = value.lastIndexOf('/')
  if (slash <= 0 || slash === value.length - 1) return false
  const address = value.slice(0, slash)
  const prefix = Number(value.slice(slash + 1))
  if (!Number.isInteger(prefix)) return false
  const octets = address.split('.')
  if (octets.length === 4) {
    return prefix >= 0 && prefix <= 32 && octets.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255)
  }
  return address.includes(':') && !address.includes('%') && prefix >= 0 && prefix <= 128
}

function installationCommand(enrollment: EdgeEnrollment): string {
  return `sudo install -m 0755 yufeng-edge /usr/local/bin/yufeng-edge
sudo install -m 0640 edge.yaml /etc/yufeng/edge.yaml
sudo systemctl enable --now yufeng-edge
# unit: ${enrollment.unitId}
# expected listen plan: ${enrollment.expectedListenPlanVersion}
# expected generation: ${enrollment.expectedGenerationId} / ${enrollment.expectedGenerationSeq}`
}

export function EdgeEnrollmentCard({
  assetId,
  enrollments,
  canWrite,
  client,
  onRefresh,
}: {
  assetId: string
  enrollments: EdgeEnrollment[]
  canWrite: boolean
  client: ConsoleClient
  onRefresh: () => void
}) {
  const modal = useDisclosure()
  const [unitId, setUnitId] = useState('edge-1')
  const [posture, setPosture] = useState<EdgeEnrollmentInput['posture']>('INGRESS_POSTURE_REVERSE_PROXY')
  const [listenAddress, setListenAddress] = useState(':18080')
  const [upstreamUrl, setUpstreamUrl] = useState('')
  const [trafficKey, setTrafficKey] = useState('')
  const [trustedProxyCidrs, setTrustedProxyCidrs] = useState('')
  const [profileId, setProfileId] = useState('http-threat/PVM/gpvm-e9eceef3')
  const [modelGroup, setModelGroup] = useState('http-threat')
  const [modelType, setModelType] = useState('PVM')
  const [modelVersion, setModelVersion] = useState('gpvm-e9eceef3')
  const [alertThreshold, setAlertThreshold] = useState('0.9')
  const [reviewFloor, setReviewFloor] = useState('0.5')
  const [windowItems, setWindowItems] = useState('4096')
  const [windowMemoryMiB, setWindowMemoryMiB] = useState('128')
  const [windowAgeSeconds, setWindowAgeSeconds] = useState('2')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async () => {
    const normalizedUnitID = unitId.trim()
    const normalizedListen = listenAddress.trim()
    const normalizedTrafficKey = trafficKey.trim()
    const normalizedUpstream = upstreamUrl.trim()
    const cidrs = [...new Set(trustedProxyCidrs.split(/[\s,]+/).map((value) => value.trim()).filter(Boolean))].sort()
    const alert = Number(alertThreshold)
    const floor = Number(reviewFloor)
    const items = Number(windowItems)
    const memoryMiB = Number(windowMemoryMiB)
    const ageSeconds = Number(windowAgeSeconds)
    if (normalizedUnitID === '' || normalizedUnitID.length > 64 || normalizedListen === '' || normalizedTrafficKey === '') {
      setError('请完整填写 Edge 单元标识、监听地址和流量键。')
      return
    }
    if (cidrs.some((value) => !validCIDR(value))) {
      setError('可信代理必须是有效的 IPv4 或 IPv6 CIDR。')
      return
    }
    if (posture === 'INGRESS_POSTURE_REVERSE_PROXY') {
      try {
        const upstream = new URL(normalizedUpstream)
        if (!['http:', 'https:'].includes(upstream.protocol) || upstream.hostname === '' || upstream.username !== '' || upstream.password !== '' || upstream.hash !== '') {
          throw new Error('invalid upstream')
        }
      } catch {
        setError('反向代理上游必须是无用户信息、无片段的绝对 HTTP 或 HTTPS 地址。')
        return
      }
    }
    if (!Number.isFinite(alert) || !Number.isFinite(floor) || floor < 0 || floor >= alert || alert > 1) {
      setError('复核下限必须不小于 0，并小于不超过 1 的告警阈值。')
      return
    }
    if (!Number.isInteger(items) || items < 1 || items > 65536 || !Number.isInteger(memoryMiB) || memoryMiB < 1 || memoryMiB > 256 || !Number.isFinite(ageSeconds) || ageSeconds < 0.01 || ageSeconds > 300) {
      setError('缓存窗口必须在 1–65536 条、1–256 MiB 和 0.01–300 秒范围内。')
      return
    }
    setBusy(true)
    setError(null)
    try {
      await client.putEdgeEnrollment({
        assetId,
        unitId: normalizedUnitID,
        posture,
        listenAddress: normalizedListen,
        upstreamUrl: posture === 'INGRESS_POSTURE_REVERSE_PROXY' ? normalizedUpstream : '',
        trafficKey: normalizedTrafficKey,
        trustedProxyCidrs: cidrs,
        modelProfile: {
          profileId: profileId.trim(),
          modelGroup: modelGroup.trim(),
          modelType: modelType.trim(),
          modelVersion: modelVersion.trim(),
          alertThreshold: alert,
          reviewFloor: floor,
          reviewWindowSeconds: 300,
          maxReviewPerUnit: 4,
          maxReviewPerRoute: 1,
          dedupeRule: 'MODEL_DEDUPE_RULE_METHOD_ROUTE_HIGHEST_SCORE',
          allowedHeaders: ['accept', 'content-type', 'user-agent'],
          maxBodyBytes: 65536,
          reviewNewRoutes: true,
          reviewInsufficientCoverage: true,
        },
        modelIngressWindow: {
          maxItems: items,
          maxRetainedBytes: String(memoryMiB * 1024 * 1024),
          maxQueueAge: `${ageSeconds}s`,
        },
      })
      modal.onClose()
      onRefresh()
    } catch (cause) {
      setError(isApiError(cause) ? `${cause.message}（${cause.code}）` : 'Edge 接入配置签发失败，请重试')
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <section className="fs-panel" aria-label="人工 Edge 接入">
        <div className="fs-panel-head">
          <div>
            <p className="fs-panel-title">人工 Edge 接入</p>
            <p className="fs-panel-sub">Brain 只签发配置制品；技术人员安装、启动、升级和回退</p>
          </div>
          {canWrite && <Button size="sm" color="primary" radius="md" onPress={modal.onOpen}>接入 Edge</Button>}
        </div>
        {enrollments.length === 0 ? (
          <p className="px-4 py-6 text-center text-xs text-[#8b98a1]">尚未登记 Edge；资产本身已经可以独立存在。</p>
        ) : (
          <div className="space-y-4 p-4">
            {enrollments.map((enrollment) => {
              const edgeStatus = STATUS[enrollment.status]
              const modelsideStatus = STATUS[enrollment.modelsideStatus]
              return (
                <article key={enrollment.unitId} className="rounded-xl border border-[#273238] bg-[#0b1316] p-4">
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div>
                      <p className="fs-mono text-sm">{enrollment.unitId}</p>
                      <p className="mt-1 text-xs text-[#8b98a1]">{enrollment.posture} · {enrollment.listenAddress} · {enrollment.trafficKey}</p>
                    </div>
                    <div className="flex gap-2">
                      <Badge label={`Edge ${edgeStatus.label}`} tone={edgeStatus.tone} />
                      <Badge label={`ModelSide ${modelsideStatus.label}`} tone={modelsideStatus.tone} />
                    </div>
                  </div>
                  <dl className="yf-kv mt-4">
                    <dt>上游</dt><dd className="fs-mono">{enrollment.upstreamUrl || '外部授权，无业务上游'}</dd>
                    <dt>期望监听计划</dt><dd className="fs-mono">{enrollment.expectedListenPlanVersion}</dd>
                    <dt>实际监听计划</dt><dd className="fs-mono">{enrollment.currentListenPlanVersion}</dd>
                    <dt>期望资产世代</dt><dd className="fs-mono">{enrollment.expectedGenerationId} / {enrollment.expectedGenerationSeq}</dd>
                    <dt>实际资产世代</dt><dd className="fs-mono">{enrollment.currentGenerationId || '—'} / {enrollment.currentGenerationSeq}</dd>
                    <dt>ModelSide 身份</dt><dd className="fs-mono">{enrollment.modelsideId}</dd>
                    <dt>模型档案</dt><dd className="fs-mono">{enrollment.modelProfile.profileId}</dd>
                    <dt>最近 Edge 心跳</dt><dd className="fs-mono">{formatTime(enrollment.lastHeartbeatAt)}</dd>
                    <dt>最近模型结果</dt><dd className="fs-mono">{formatTime(enrollment.modelsideLastResultAt)}</dd>
                  </dl>
                  <div className="mt-4 grid gap-4 lg:grid-cols-2">
                    <div>
                      <p className="mb-2 text-xs font-semibold text-[#b8c9c7]">技术人员所需文件</p>
                      <ul className="list-disc space-y-1 pl-5 text-xs leading-5 text-[#8b98a1]">
                        <li>yufeng-edge 二进制与 edge.yaml</li>
                        <li>Brain 签名公钥与传输层安全协议根证书</li>
                        <li>单元引导令牌文件、Edge 客户端证书和私钥</li>
                        <li>来源假名密钥；如启用邻近模型，再准备 ModelSide 证书、私钥和权重清单</li>
                      </ul>
                    </div>
                    <div>
                      <p className="mb-2 text-xs font-semibold text-[#b8c9c7]">人工安装命令</p>
                      <pre className="overflow-x-auto rounded-lg bg-[#07090b] p-3 text-xs text-[#c7d2d9]">{installationCommand(enrollment)}</pre>
                    </div>
                  </div>
                </article>
              )
            })}
          </div>
        )}
      </section>

      <Modal isOpen={modal.isOpen} onClose={modal.onClose} placement="center" scrollBehavior="inside" size="2xl" radius="lg">
        <ModalContent>
          <ModalHeader>接入 Edge</ModalHeader>
          <ModalBody className="gap-3">
            <p className="text-xs leading-5 text-[#8b98a1]">页面不读取令牌、证书私钥或 Edge 管理口，也不会调用 Docker 或服务管理器。</p>
            <Input label="Edge 单元标识" value={unitId} onValueChange={setUnitId} radius="md" isRequired />
            <Select label="入口姿态" selectedKeys={[posture]} onChange={(event) => setPosture(event.target.value as EdgeEnrollmentInput['posture'])} radius="md">
              <SelectItem key="INGRESS_POSTURE_REVERSE_PROXY">反向代理</SelectItem>
              <SelectItem key="INGRESS_POSTURE_EXT_AUTHZ">Envoy 外部授权</SelectItem>
            </Select>
            <Input label="监听地址" value={listenAddress} onValueChange={setListenAddress} radius="md" isRequired />
            <Input label="流量键" value={trafficKey} onValueChange={setTrafficKey} radius="md" isRequired />
            {posture === 'INGRESS_POSTURE_REVERSE_PROXY' && <Input label="真实上游地址" value={upstreamUrl} onValueChange={setUpstreamUrl} placeholder="http://app:8080" radius="md" isRequired />}
            <Input label="可信代理网段" value={trustedProxyCidrs} onValueChange={setTrustedProxyCidrs} placeholder="10.0.0.0/8, 2001:db8::/32" radius="md" />
            <div className="grid gap-3 sm:grid-cols-2">
              <Input label="模型档案标识" value={profileId} onValueChange={setProfileId} radius="md" />
              <Input label="模型版本" value={modelVersion} onValueChange={setModelVersion} radius="md" />
              <Input label="模型组" value={modelGroup} onValueChange={setModelGroup} radius="md" />
              <Input label="模型类型" value={modelType} onValueChange={setModelType} radius="md" />
              <Input label="告警阈值" type="number" value={alertThreshold} onValueChange={setAlertThreshold} radius="md" />
              <Input label="复核下限" type="number" value={reviewFloor} onValueChange={setReviewFloor} radius="md" />
              <Input label="缓存条目数" type="number" value={windowItems} onValueChange={setWindowItems} radius="md" />
              <Input label="缓存内存（MiB）" type="number" value={windowMemoryMiB} onValueChange={setWindowMemoryMiB} radius="md" />
              <Input label="最长排队（秒）" type="number" value={windowAgeSeconds} onValueChange={setWindowAgeSeconds} radius="md" />
            </div>
            {error !== null && <p role="alert" className="text-xs text-[#ff746c]">{error}</p>}
          </ModalBody>
          <ModalFooter>
            <Button variant="light" radius="md" isDisabled={busy} onPress={modal.onClose}>取消</Button>
            <Button color="primary" radius="md" isLoading={busy} onPress={() => void submit()}>签发接入配置</Button>
          </ModalFooter>
        </ModalContent>
      </Modal>
    </>
  )
}
