# 御锋数据面升级：0.2.0 收口备忘

> **非权威、不约束实现。** 本文记录数据面升级的动机、已否决方案与 0.2.0 落点；已拍板项已写入权威文档。架构以 [`architecture.md`](architecture.md) 第 4 节和架构决策记录 036 为唯一权威；网络行为以 [`api.md`](api.md) 第 21 节为准；生产入口语义以 [`design.md`](design.md) 第 4 节为准。

## 1. 升级目标

数据面必须同时满足三件事：

1. 同步确定性检查与异步深度学习按代价分层；高成本方法不能拖慢或改写当前请求。
2. 同一检测核支持反向代理、外部授权和其它已解密的超文本传输协议复制入口；入口姿态与发布状态保持正交。
3. 新检测器、模型版本和策略通过小接口与签名制品扩展，不把检测器名称分支泄漏进治理状态机。

这次重构同时收紧基础设施权限：`yufeng-edge` 的安装、启动、升级和卸载归技术人员。Brain 与贾维斯都不能创建、启动、重建或探测 Edge；Brain 只在管理员提交部署规格后确定性签发监听计划、基线资产世代和模型档案。

## 2. 冻结边界

- `Inspector` 只输出发现与检查覆盖度，`Gate` 才能依据已签名策略决定当前请求。
- Edge 启动后主动注册、拉取签名监听计划与资产世代，并在心跳中回执已装载版本。
- 原始请求只在入口附近流转。Brain、贾维斯、调查执行实例、事件账和检查票据都不能取得完整正文。
- `yufeng-modelside` 是独立 Python 服务，不进入 Edge 二进制，不拥有 Gate 权限，也不直接影响当前请求。
- 同机 Edge 与 ModelSide 优先使用 Unix 域套接字；跨主机只允许受控防御网络内的相互传输层安全协议连接。
- 请求路径不执行模型推理、不访问 Brain、不同步写文件，也不等待任何旁路消费者。
- 异步结果回到 Gate 的唯一途径是形成治理建议、通过门禁并签发新资产世代；只影响后续请求。

## 3. 0.2.0 目标拓扑

```text
已完成业务传输层安全协议终止的 HTTP 请求
                     │
                     ▼
          yufeng-edge 规范请求视图
             │                 │
             │同步             │一次有界所有权转移
             ▼                 ▼
      Inspector → Gate    Edge 有界非阻塞队列
             │                 │
             ▼                 ▼
       当前请求结束      yufeng-modelside 输入队列
                               │ TensorFlow 异步推理
                               ▼
                    MODEL_ALERT / REVIEW_SAMPLE
                               │ 独立有界结果队列
                               ▼
                 ModelResultService.UploadResults
                               │
                               ▼
                    yufeng-brain 类型化事务入账
```

本旁路处理的是已解密请求副本，不包含交换机镜像抓包、传输控制协议重组或加密流量解密。观察型入口只有在上游已经提供规范 HTTP 字段时才能接入；密文或未重组字节流不得伪报完整检查覆盖度。

## 4. 同步检查与裁决

同步接口用类型封死“新检测方法自动获得拦截权”：

```go
type Inspector interface {
    ID() string
    Inspect(ctx context.Context, view CanonicalView) (Inspection, error)
}
```

`Inspection` 只含完整检测键与逐检查面覆盖度，没有 `Action`。同一规范请求视图和同一已装载资产世代必须得到相同发现、覆盖度与 Gate 动作，这一不变量由回放对比测试锁定。

入口姿态回答“流量怎样进入核、谁写状态码”，发布状态回答“哪条签名策略在哪些单元生效”。只有入口允许拦截、发布状态允许阻断、签名策略匹配且所需检查面覆盖充分时，当前请求才可返回 403。观察入口永远不能阻断。

覆盖不足不是统一的运行时故障：

| 情况 | 反向代理 | 外部授权 | 观察入口 |
|---|---|---|---|
| 正向命中 | 已签名策略允许时 403 | 网关 403 | 只记录 `would_have_blocked` |
| 正文超过路由签名上限 | 413 | 已收到超限正文时 403 | 标记不完整，不阻断 |
| 请求不可规范化 | 400 | 已收到畸形字节时 403 | 标记检查错误 |
| 检查器自身崩溃 | 503 | 先按熔断规则处理 | 丢观察样本并计数 |
| 某检查面缺失或不支持 | 依赖该面的负向策略不参与 | 同左 | 同左 |

## 5. ModelSide 规范化流量

Edge 按签名模型档案生成 `normalized-http/v1`。至少包含请求标识、单元、资产、世代、模型档案引用、方法、无敏感值的路由模板、允许进入模型的请求头、规范查询参数、正文、内容类型、原始正文长度、截断状态和逐检查面覆盖度。

禁止字段包括 Cookie、Authorization、客户端原始地址、传输层安全协议密钥、Edge/Brain 凭据及档案未允许的请求头。正文只允许向本地模型队列进行一次有界所有权转移，不得为 Brain、智能代理、日志、检查票据或第二个消费者再次复制完整正文。

签名 `ModelProfile` 决定：

- 模型组、类型、版本和档案摘要；
- 告警阈值与复核下限；
- 五分钟复核窗口；
- 每单元每窗口最多四个复核代表；
- 每方法与路由最多一个最高风险代表；
- 新路由与检查覆盖不足是否进入复核；
- 允许的请求头和最大正文长度。

ModelSide 不得用进程内默认值替代缺失或无效档案。档案验签失败、模型版本不匹配或权重加载失败时关闭该模型旁路并计数，同步请求仍按现有 Gate 语义运行。

## 6. 双有界队列与结果语义

第一条队列位于 Edge 与 ModelSide 之间。请求路径只尝试非阻塞入队；条目或字节容量满时立即丢旁路并增加计数。后台发送器批量调用 `ModelSideIngressService.SubmitTraffic`，ModelSide 满载时逐项拒收，不让连接反向施压当前请求。

第二条队列位于 ModelSide 与 Brain 之间。分数达到告警阈值的结果作为 `MODEL_ALERT` 全量尝试上报；达到复核下限或满足签名复核条件的结果作为 `REVIEW_SAMPLE`，先执行窗口化有界去重。Brain 断连或磁盘变慢时结果队列独立重试；本地输入队列和推理循环继续工作，结果队列满时按告警优先规则丢旁路并计数。

Brain 接收 `MODEL_ALERT` 后，在同一数据库事务中幂等创建事件、追加模型推理记录、冻结检查票据、聚类或创建案件、写事务发件箱并创建或唤醒研判。`REVIEW_SAMPLE` 只进入案件聚合，不逐条唤醒贾维斯。

## 7. 人工部署形态

正式交付有三种组合：

- 控制面同置：技术人员使用 `deploy/compose.yaml` 部署 Brain 等控制面，再显式使用 `deploy/compose.edge-modelside.yaml` 部署 Edge 与 ModelSide。
- 远端 Brain：数据面 Compose 只连接受信任远端 Brain；控制面不拥有数据面 Compose 生命周期。
- 原生或混合：Edge 使用原生 Go 二进制，ModelSide 使用原生 Python 服务包；两者也可分别作为容器或独立主机运行。

控制面镜像不打包 Edge 或可选本机监督器。Edge 有独立容器镜像和原生服务材料；ModelSide 有独立 Python wheel、容器镜像和原生服务材料。`yufeng-dataplane` 如果由技术人员安装，只能读取本机状态，不持 Docker、不接受 Brain/Agent 控制，也不能创建或重建 Edge。

## 8. 容量与时延验收

请求路径的冻结预算来自 `lib/kernel/limits.go`：每秒 2000 个请求，模型旁路第 99 百分位增量不超过 1 毫秒。验收必须分别覆盖：

1. 旁路关闭；
2. ModelSide 空闲；
3. ModelSide 满载；
4. Brain 断连；
5. Brain 磁盘变慢。

每个场景都记录实际吞吐、请求路径第 99 百分位延迟、相对关闭场景的增量、两条队列深度、丢弃数和结果重试数。旁路满载或上行故障只能改变旁路计数，不能改变当前请求的同步动作。

## 9. 明确否决

- Brain、贾维斯或其它智能代理部署、启动、重建、升级、卸载或探测 Edge。
- Jarvis 持有 Docker 套接字、进程管理器、Edge 安装凭据、基线签发或流量模型权限。
- Brain 驱动本机监督器创建 Edge 容器。
- Edge 把原始流量、完整正文或聊天凭据发送给 Brain。
- ModelSide 直接连接 PostgreSQL、消息服务器、Agent 队列或 Gate。
- ModelSide 复用执行实例 `PollWork`，或者通用 worker 承载模型流量。
- 请求路径等待 ModelSide、Brain、磁盘或结果消费者。
- 用 Redis、日志消费者或旧 ModelSide 上报代码替代本版本的类型化协议。
- 将交换机镜像、传输控制协议重组或传输层安全协议解密暗含在 HTTP 旁路承诺中。
- 模型分数直接生成 403、未经治理的新正则或热补核心规则集。
- 把入口姿态写进资产世代，或让观察入口返回 403。
- 把生产能力广告当成消费授权。

## 10. 当前实现定位

| 行为 | 实现 | 主要验证 |
|---|---|---|
| 检查与裁决分离 | `lib/edgecore/inspector.go`、`release_set.go` | `TestNewInspectorCannotBlockWithoutPolicy`、回放对比测试 |
| Edge 规范化与非阻塞队列 | `lib/edgecore/model_bypass.go`、`cmd/yufeng-edge/modelside.go` | 队列容量、正文所有权和请求路径测试 |
| 独立 Python 推理服务 | `components/modelside/yufeng_modelside/` | 档案、采样、Unix 域套接字、相互传输层安全协议与结果重试测试 |
| Brain 类型化批量入账 | `proto/yufeng/modelside/v1/modelside.proto`、`lib/brain/model_result.go` | 签名档案校验、事务原子性、幂等与复核有界测试 |
| 人工 Edge 生命周期 | `deploy/compose.edge-modelside.yaml`、`deploy/edge/`、`deploy/modelside/` | Compose 闭集、镜像分离、原生服务和引导契约测试 |
| 五场景性能预检 | `lib/edgecore/model_bypass_performance_test.go`、`scripts/performance-live.sh` | 2000 请求/秒与第 99 百分位旁路延迟预算 |

0.2.0 的核心不是把“智能”搬进 Edge，而是把高成本推理放到入口附近的独立、无 Gate 权限服务，并用签名档案、双有界队列和类型化事务结果维持数据面与治理面的边界。
