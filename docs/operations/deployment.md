# 部署与上线

> 本文给出部署选型、边界和验收口径。架构与技术选型以 [`architecture.md`](../architecture.md) 为准，网络行为以 [`api.md`](../api.md) 为准，线上字段以 `proto/` 为准；本文不另立协议。仓库声明版本只读取根目录 [`VERSION`](../../VERSION)，已发布状态只读取 [GitHub Releases](https://github.com/ZionOVO/YuFeng/releases/latest)。客户现场仍须填写真实上游、精确代理网段、证书与网络核对结果，以及切换、回退、密钥轮换和备份恢复责任人。

## 1. 交付结论

首个可交付场景是：**单个企业站点、客户已有入口、入口终止业务传输层安全协议（Transport Layer Security，TLS）、御锋处理已解密的超文本传输协议（Hypertext Transfer Protocol，HTTP）流量、一个 Brain、一个 Edge、人工批准策略生效。**

两条同步入口按下列顺序交付：

1. 反向代理：客户入口可调整回源地址时首选；
2. Envoy 外部授权：客户已有兼容网关时使用，网关继续负责 TLS、路由和高可用。

深度学习检测只走异步旁路：Edge 从上述已解密入口取得请求副本，规范化后交给邻近 ModelSide；不包含交换机镜像抓包、传输控制协议重组或加密流量解密。原始请求不进入 Brain、贾维斯、执行实例、数据库、消息服务器或日志。

## 2. 进程生命周期与交付形态

| 进程 | 生命周期负责人 | 原生交付 | 容器交付 |
|---|---|---|---|
| `yufeng-brain`、`yufeng-jarvis`、`yufeng-agentd` | 中台技术人员 | Go 二进制 | `deploy/compose.yaml` |
| `yufeng-edge` | 防御网络技术人员，必须人工安装、启动、升级、回退和卸载 | 发布包中的 Go 二进制与 `deploy/edge/` | `deploy/edge.Dockerfile` 与 `deploy/compose.edge-modelside.yaml` |
| `yufeng-modelside` | 防御网络技术人员，独立于 Edge 和 Brain | `components/modelside` Python 服务包与 `deploy/modelside/` | `components/modelside/Dockerfile` 与 `deploy/compose.edge-modelside.yaml` |
| `yufeng-dataplane`（可选） | 仅技术人员 | Go 二进制 | 不进入正式 Compose |

可选 `yufeng-dataplane` 只读取本机 Edge 服务状态，不能持 Docker、创建或重建容器、安装进程、接收 Brain 控制或探测业务路由。Brain、贾维斯、agentd 与智能代理工具都没有 Edge 生命周期权限。

管理员在控制台提交部署规格后，Brain 在同一数据库事务中确定性创建或复用资产，预声明 ModelSide 身份，签发监听计划、基线资产世代和模型档案。Edge 启动后主动注册、拉取签名制品并用心跳回执已装载坐标；Brain 只据此计算 `edge_ready`，不反向拨号 Edge 管理口。

## 3. 支持的部署拓扑

### 3.1 同一物理节点

```text
客户入口 ──已解密 HTTP──▶ yufeng-edge ──▶ 业务应用
                               │
                               └──Unix 域套接字──▶ yufeng-modelside
                                                        │
                     签名制品/脱敏事件                   └──无原文结果
                               │                                  │
                               └────────▶ yufeng-brain ◀──────────┘
```

技术人员先启动 `deploy/compose.yaml` 控制面，再把 `deploy/compose.edge-modelside.yaml` 合并到同一 Docker Compose 项目并显式启动 `edge modelside`。Edge 与 ModelSide 共享权限受限的 Unix 域套接字；Brain 和贾维斯不调用 Docker。具体命令见 [`deploy/README.md`](../../deploy/README.md)。

### 3.2 Edge 与 ModelSide 独立主机，连接远端 Brain

```text
入口主机: yufeng-edge ──相互 TLS──▶ 模型主机: yufeng-modelside
     │                                      │
     └──相互 TLS──▶ 远端 yufeng-brain ◀─────┘
```

三台主机必须位于同一受控防御网络。Edge 到 ModelSide 与 ModelSide 到 Brain 使用两套独立相互传输层安全协议身份，服务端名称和证书用途必须覆盖实际地址；防火墙只开放精确源、目标和端口。Brain 仍不接收原始头、查询参数或正文。

### 3.3 Edge 与 ModelSide 独立进程或独立容器

两者可任意组合为原生进程或容器。只要同机，就优先使用 Unix 域套接字；跨主机才使用相互传输层安全协议。ModelSide 不进入 Edge 二进制，Edge 停止旁路也不影响同步 Inspect 与 Gate。

## 4. 入口合同

### 4.1 反向代理

```text
客户端 --HTTPS--> 客户入口 --HTTP--> yufeng-edge --HTTP/HTTPS--> 业务应用
```

客户入口保留原始 `Host`，追加 `X-Forwarded-For`，透传升级连接；入口地址所属精确网段写入签名监听计划。生产回源不得使用内置演示上游。回退只恢复客户入口上一版回源池配置，不调用 Brain、ModelSide 或智能代理。参考配置见 [`deploy/reverse-proxy/`](../../deploy/reverse-proxy/README.md)。

### 4.2 Envoy 外部授权

```text
客户端 --HTTPS--> Envoy/兼容网关 ----------------------> 业务应用
                         │
                         └--HTTP 外部授权检查--> yufeng-edge
```

网关提供方法、路径、查询、允许的请求头和约定范围内的请求体。未转发检查面必须标为覆盖不足，不能当作无发现。网关关闭故障放行；边缘明确拒绝或熔断时按签名策略和固定入口语义处理。参考配置见 [`deploy/envoy/`](../../deploy/envoy/README.md)。

## 5. 异步模型数据边界

Edge 规范流量至少含请求标识、单元、资产、世代、方法、路由、签名档案允许的请求头、查询参数、有界正文、内容类型、原始正文长度、截断状态和检查覆盖度。正文所有权只向模型输入缓存窗口转移一次；不得为了 Brain、贾维斯或案件采样再复制完整正文。

两条队列相互独立：

1. Edge 到 ModelSide：按条数、实际保留字节和排队年龄三重有界，满时以单次最多 32 项的有界工作淘汰最旧可排队项、优先保留新流量；预算用尽仍无法准入时丢新项，请求路径不等待；
2. ModelSide 到 Brain：有界批量结果队列；Brain 断连时继续本地推理并退避重试，满时优先保留告警、丢弃复核代表并计数。

中央期望值随签名监听计划下发，初始默认 4096 条、128 MiB、2 秒；本机启动配置是不可被中央放大的硬上限，默认 16384 条、256 MiB、5 分钟。原生参数为 `--model-ingress-window-max-items`、`--model-ingress-window-max-bytes`、`--model-ingress-window-max-age`，对应环境变量为 `YUFENG_MODEL_INGRESS_WINDOW_MAX_ITEMS`、`YUFENG_MODEL_INGRESS_WINDOW_MAX_BYTES`、`YUFENG_MODEL_INGRESS_WINDOW_MAX_AGE`。中央期望超过本机上限时 Edge 逐项收窄并在心跳与控制台报告降级原因；缩容不批量清空已有项，而由新流量淘汰、后台发送和过期清理自然收敛。

ModelSide 的模型版本、告警阈值、复核下限、窗口、每单元上限、每路由上限与去重规则全部来自签名模型档案。它没有 Gate、工具、Agent、PostgreSQL、Redis 或 NATS 消息服务器权限，结果永不改变当前请求。

## 6. 故障与回退

| 场景 | 当前请求 | 异步旁路 | 运维动作 |
|---|---|---|---|
| 旁路关闭 | 只执行同步 Inspect 与 Gate | 不构造或不入队 | 核对关闭计数与基线延迟 |
| ModelSide 空闲 | 不等待推理 | 后台及时消费 | 核对正常队列深度与批量上报 |
| ModelSide 满载 | 不等待队列 | Edge 队列满时丢旁路并计数 | 扩容或降低签名正文预算，不改变请求路径 |
| Brain 断连 | Edge 继续使用最近已验签世代 | ModelSide 继续推理，结果队列有界重试 | 恢复后批量上报；允许进程崩溃丢失未上报旁路结果 |
| Brain 磁盘变慢 | 不同步写 Brain 或本地文件 | 上报变慢只消耗结果队列预算 | 修复 Brain 存储；不得让结果队列反压 Edge |
| Edge 进程退出 | 反向代理路径不可用；外部授权按网关失败合同处理 | ModelSide 不接收新流量 | 技术人员按运行手册重启或回滚；Brain 与贾维斯不代操作 |
| 坏签名或倒退世代 | 保留上一份已验证制品 | 不使用坏模型档案 | 在 Brain 修正并重新签发，不热改 Edge 文件 |

## 7. 安全检查

- Brain、贾维斯、agentd 和可选本机监督器均不挂 Docker 套接字；
- 贾维斯不持 Edge 安装、服务管理、容器、模型推理或网络探测权限；
- Edge 只取得单元引导凭据、客户端证书、制品验签公钥和来源假名密钥；
- ModelSide 只取得专用结果令牌、客户端证书、权重和本地套接字；部署规格预声明 `${unit_id}-modelside`，首次合法上报固定证书指纹；
- Brain 接收的 ModelResult 不含请求头、查询参数或正文，且服务端重新校验身份、世代签名、档案摘要、模型坐标、阈值、窗口和去重；
- `MODEL_ALERT` 的事件、推理、检查票据、案件、事务发件箱和研判唤醒在同一 PostgreSQL 事务提交；`REVIEW_SAMPLE` 不逐条唤醒贾维斯；
- 客户业务 TLS 私钥始终归客户入口，不能复用控制面证书参数。

## 8. 容量与交付验收

发布候选必须在固定硬件、Go 与 Python 版本、真实 Coraza 核心规则集、模型权重、请求分布和上游延迟下，由 64 个并发发生器以每秒 2000 个请求交叉验证旁路关闭、ModelSide 空闲、稳定消费、满载和不可达，以及小正文、接近检查上限正文、默认窗口和本机默认硬上限窗口。每个组合预热后连续测量至少 60 秒并重复三次，记录计划请求、发生器丢失、吞吐、第 50 / 95 / 99 百分位延迟、Edge 中央处理器时间与常驻内存、窗口排队/在途条数和字节、最老年龄与各原因丢弃。发生器丢失必须为零；相对关闭旁路基线，第 99 百分位延迟增量不得超过 1 毫秒、中央处理器占用增量不得超过 5 个百分点，Edge 常驻内存不得超过 512 MiB；任一门槛失败就停止进程内扩大窗口路线，转入外置消息队列设计。

软件发布门禁及制品集合只由[软件发布与交付证据](release-and-delivery.md)维护，本文不重复发布清单。客户环境上线必须使用已公开且复核通过的 Release，并填写[现场变更记录](../../deploy/pilot-change-record.md)，关闭网络、证书、切换、回退、轮换与恢复记录。

## 9. 不在当前范围

- 御锋直接终止业务 TLS、托管站点私钥或热轮换业务证书；
- 交换机镜像抓包、网络测试接入点、传输控制协议重组、加密流量解密或非 HTTP 检测；
- Kubernetes 作为运行前提、多活 Brain、多租户或跨站点自动故障切换；
- ModelSide 训练、同步 Gate、原始流量上报、Redis 消费或直连数据库与消息服务器；
- Brain、贾维斯或智能代理自动部署、重建、升级、卸载或探测 Edge。
