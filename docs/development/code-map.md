# 代码地图

> 目的：按架构概念快速找到代码位置。每完成一个里程碑更新本文件。
> 术语含义见[术语表](../glossary.md)；架构全貌见[架构设计](../architecture.md)。
> 仓库声明版本只读取根目录 [`VERSION`](../../VERSION)，对外已发布状态只读取 [GitHub Releases](https://github.com/ZionOVO/YuFeng/releases/latest)；只有同名非草稿 Release 的精确提交证据资产通过复核，才可宣称对应单站点企业试点软件版本和机器验收闭环。客户现场仍须记录真实上游、精确代理网段、证书核对、切换与回退责任人。发布与部署证据边界见[软件发布与交付证据](../operations/release-and-delivery.md)。
>
> **本表所有路径都只是检索入口，不是修改闭集。** 开发时必须从协议消息、服务方法、数据库列、命令行参数和术语出发，用 `rg` 地毯式追踪生产者、消费者、生成代码、迁移、测试、部署与文档；不得只改表中列出的文件。

## 概念 → 代码对照表

| 架构概念 | 代码位置 | 状态 |
|---|---|---|
| 共享枚举（修复层级 / 修复面 / 接入模式 / 生效语义 / 发布状态） | `proto/yufeng/common/v1/v1.proto` | 已建（字段级） |
| 事件契约（超文本传输协议与人工智能流量的统一容器，含关键事件发布轨迹） | `proto/yufeng/event/v1/v1.proto` | 已建（字段级） |
| 资产契约（资产账结构，含自动执行上限） | `proto/yufeng/asset/v1/v1.proto` | 已建（字段级） |
| 单元生产能力广告 | 权威语义 `docs/api.md` §2.1；协议 `proto/yufeng/unit/v1/unit.proto` 与 `proto/yufeng/registry/v1/registry.proto`；持久化与兼容门禁 `lib/brain/registry.go`、`artifact_service.go`；只读投影 `AssetDetail.units` | 已建：区分关键事件、普通样本、票据特征、投影版本、姿态、传感、容量与生产健康；不兼容世代不下发，广告不产生授权 |
| 制品契约（内容寻址身份、单指针取代、签名、回放门禁） | `proto/yufeng/artifact/v1/v1.proto` | 已建（字段级） |
| 签名采样、流量审查与转发策略 | 权威语义 `docs/api.md` §21.4、§21.5；协议入口 `proto/yufeng/artifact/v1` 与资产世代装载；实现入口 `lib/brain/asset_traffic_review.go`、`lib/edgecore/traffic_review.go`、`lib/kernel/limits.go` | 流量审查模式已是签名世代成员：支持该策略的 Edge 停止普通流量逐条随机入账，改为完整计数与有界代表；不支持新策略的旧 Edge 才保留 `NoDetectionSampleRate=1%` 兼容行为 |
| 修复计划契约（多层修复动作组合） | `proto/yufeng/plan/v1/v1.proto` | 已建（字段级） |
| 修复程序契约（信封强类型） | `proto/yufeng/procedure/v1/v1.proto` | 已建（字段级） |
| 工具描述契约（智能代理工具定义） | `proto/yufeng/tool/v1/v1.proto` | 已建（字段级） |
| 修复程序步骤体的 JavaScript 对象表示法模式 | `procedures/steps.schema.json` | 已建 |
| 智能代理控制面契约（独立进程、指令轮询、执行实例、工作进程、工具网关、会话和执行单元指令） | 权威语义 `docs/api.md` §18；现有服务端 `lib/brain/agent*.go` `session.go` `command.go`；进程 `cmd/yufeng-jarvis` `cmd/yufeng-agentd` `cmd/yufeng-run` `cmd/yufeng-host`；运行时 `agents/runtime`（含本地监督代理） | 已建案件审批与受管 Agent 委派：案件冻结 Agent 身份、工具、资产范围与配置摘要，Jarvis 只编排，agentd 启动短命 run；注册、长轮询、租约代次、会话、认知账本、模型生成、持久预算和可恢复工具副作用均已建，能力令牌不进子进程环境 |
| 工作进程的工作负载身份 | 权威语义 `docs/api.md` §18.3；身份入口 `lib/brain/worker_identity.go`、工作进程档案 `lib/brain/agent_run.go`、监督入口 `cmd/yufeng-agentd`、迁移 `lib/store/migrations/00028_analysis_workers.sql` | 已建：`WorkerService` 只向执行实例监督进程签发绑定工作进程标识、`RUN_SUPERVISOR` 种类与公钥或证书的独立身份；`ANALYSIS_SCORER` 及其远程过程调用仅为线缆兼容保留并固定退役，ModelSide 使用 §21.5 的独立身份和结果协议 |
| 智能代理认知账本与状态机 | 权威契约 `docs/api.md` §18.10；实现 `agents/runtime`、`lib/brain/turn_runtime.go`、`agent_turn_service.go`、`model_generate.go`、`run_budget.go`、`agent_run_saga.go`；迁移 `lib/store/migrations/00029_agent_turn_runtime.sql` 至 `00032_agent_audit_ledger.sql` | **恢复、预算、补偿与审计已建**：三类来源创建不可变认知回合；输入序列与账本序列分列；让出执行权时原子写检查点并释放租约，唤醒后按新代次恢复；逻辑生成只接受一个响应，物理尝试全量对账；执行实例的步骤、模型和工具预算先预留后结算；工具意图、副作用边界、结算、补偿回执与哈希链可恢复重建。子执行实例等待、审批交互与上下文压缩只有契约边界 |
| 检测器接口（进程内，微秒级同步档） | `lib/edgecore/inspector.go` + `release_set.go` `Gate` | 活路径是 `Inspector`（无 `Action`）+ `Gate`。`Detector` 只作演示 KIND_RULE 匹配器 |
| 能力令牌声明结构 | `lib/kernel/claims.go` | 已建（字段契约） |
| 生成的 Go 契约代码 | `proto/gen/`（`make generate` 产出，含 Connect service，随仓库提交） | 已生成 |
| 检测器实现 | 规则检测器：`lib/edgecore/rule.go`；Coraza DetectionOnly：`lib/edgecore/coraza.go`（自维护发布 `v3.7.0-zion.1`，核心规则集 4.25.0，实验性 `SecRxPreFilter On`）；规范化视图与覆盖度已建。**画像核未装载** | 部分已建 |
| 初次配置与人工数据面接入 | 引导契约 `docs/api.md` §19；资产接入契约 §9；协议 `proto/yufeng/onboarding/v1` 与 `proto/yufeng/asset/v1`；库表 `deployment_onboarding`、`credential_slots`、`edge_enrollments`；服务 `lib/brain/onboarding_service.go`、`asset_edge_enrollment.go` | 引导只配置/探测模型网关并确认贾维斯在线；旧部署远程过程调用固定退役。主控制台按资产签发 Edge 监听计划、保留既有策略的新世代和 ModelSide 身份，不创建数据面进程或容器 |
| Edge 人工接入 | 契约 `docs/api.md` §9 与 §19.3；Brain 签发 `lib/brain/asset_edge_enrollment.go`、`baseline_generation.go`；迁移 `lib/store/migrations/00046_manual_edge_enrollments.sql`；原生交付 `deploy/edge/`；容器交付 `deploy/edge.Dockerfile`、`deploy/compose.edge-modelside.yaml` | 管理员先登记资产，再由 `PutEdgeEnrollment` 创建或更新监听计划、保留已有非相关策略的新资产世代、模型档案和预声明 ModelSide 身份；技术人员手动安装、启动、升级和卸载 Edge，Edge 主动注册、拉取与回执；旧引导部署方法固定返回 `unimplemented` |
| 可选本机 Edge 监督器 | 入口 `cmd/yufeng-dataplane`；只读状态库 `lib/dataplane` | 仅供技术人员手动启动，读取本机服务状态；不持 Docker、不创建或重建 Edge，不接受 Brain 或 Agent 控制 |
| 控制台托管 | `cmd/yufeng-brain` 托管 `/app`（`lib/brain` 的 `ConsoleHandler`）；开发与交付运行时只装配真实 `ConnectClient`，测试夹具只在 `console/src/test` | `/app/setup` 只完成模型网关和贾维斯确认；资产页提供登记与 Edge 人工接入；事件详情展示模型推理、案件和贾维斯交付；其它治理、案件、Agent 与 Worker 页面继续使用真实服务 |
| 软件发布与部署资格证据 | 发布构建 `scripts/build-release-assets.sh`；制品封存和复核 `scripts/release-artifacts.py`、`scripts/verify-release-assets.sh`；必需持续集成 `.github/workflows/ci.yml`；开发平台兼容性 `.github/workflows/compatibility.yml`；一次构建发布 `.github/workflows/release.yml`；部署诊断入口 `scripts/delivery-evidence.sh`；合同 `docs/operations/release-and-delivery.md` | 软件发布固定为 11 个归档、清单和校验和；实际文件通过复核后先存为不可变工作流制品，再提升为公开 Release，失败只从原制品恢复。合并门禁使用固定 Linux 环境；较旧 Linux、Windows 与 Intel macOS 的低并发 Go 运行只在排查平台差异时人工触发。客户部署资格另行验证，不反向修改软件发布状态 |
| 数据面请求链 | `lib/edgecore/proxy.go`、`release_proxy.go`、`release_set.go`、`status.go`、`listen.go`、`external_authorization.go` 与 `generation.go` | 活路径由四种入口姿态的外壳、检查器与闸门构成。边缘只有持有已验证世代和监听计划才绑定业务服务器；反向代理与 Envoy 外部授权均有真实入口协议回归 |
| 客户来源与边缘假名 | 策略 `UnitListenPlan.client_source`；解析 `lib/edgecore/source.go`；检测 `lib/edgecore/inspector.go` `coraza.go`；事件编译 `lib/edgecore/telemetry.go` `cmd/yufeng-edge/brain.go` | 已建：默认使用直接对端；处于可信代理网段时，按右向左顺序解析 `X-Forwarded-For`；Coraza 使用解析后的来源，上行事件只填部署作用域内的基于哈希的消息认证码假名 |
| 中台资源投影与路由 | 权威语义 `docs/architecture.md` §4.4、`docs/api.md` §21.5；事件入口 `lib/brain/telemetry.go`，投影与冻结 `lib/brain/check_ticket.go`、`lib/edgecore/digest.go`，发件箱 `lib/brain/outbox.go` | 已建：中台按已接受事件与钉死历史世代单点冻结不可变检查票据；事件、票据和完整票据消息同事务提交，缺材料按闭集原因隔离；消费者不再按事件标识回库补票或采用默认车道 |
| Edge 邻近异步模型旁路 | 权威语义 `docs/api.md` §21.5；协议 `proto/yufeng/modelside/v1/modelside.proto`；Edge 队列与规范化 `lib/edgecore/model_bypass.go`、`cmd/yufeng-edge/modelside.go`；Python 服务 `components/modelside/yufeng_modelside`；Brain 入账 `lib/brain/model_result.go` | 已建两个独立有界队列、签名档案阈值与采样、TensorFlow 权重加载、Unix 域套接字或相互传输层安全协议、专用批量结果协议，以及告警的事件/推理/票据/案件/发件箱/研判同事务入账；原始流量不进 Brain |
| 制品签发 / 验签 / 装载 | 制品与监听计划签名 `lib/kernel/artifact.go` `listen_plan.go`；世代装载 `lib/edgecore/generation.go` `release_set.go`；中台下发 `lib/brain/artifact_service.go` + `lib/edgeclient` | 资产世代与单元监听计划均已接入；边缘按单元拉取、验签、缓存并以单调版本驱动业务绑定，坏签名、错单元或倒退版本保留旧计划 |
| 制品生命周期类型状态机 | `lib/kernel/release.go` | 已建。常见路径是草稿、已签名、仅记录、金丝雀、全量生效和退休；单机单元不足时允许从仅记录直接进入全量生效（`docs/api.md` §7.6） |
| PostgreSQL 连接池 / goose 迁移 / sqlc | `lib/store/store.go`、`lib/store/traffic_role.go`、`lib/store/migrations/`、`lib/store/sqlc/` | 治理池、最大四连接的隔离流量池与 46 个迁移已建；生产启动用 `ValidateRestrictedTrafficRole` 确认 `yufeng_traffic` 只有规定流量表的 `SELECT` / `INSERT` 且不能访问治理表；两个池仍在同一中台进程；sqlc 生成层无业务调用方 |
| 智能代理专用任务表与租约查询 | `lib/store/migrations/00004_agent_runtime.sql` `00006_sessions.sql` `00008_commands.sql` `00021_lease_budget.sql` `00029_agent_turn_runtime.sql` 至 `00034_investigation_receipts.sql`；`lib/brain/agent*.go` `session.go` `command.go` `run_budget.go` | 现有智能代理指令、执行实例工作项和资产侧命令三条长轮询及租约代次栅栏已建；认知检查点、等待释放租约、输入唤醒、执行预算、可恢复补偿、只追加权威审计、目录钉死与调查回执均已持久化。继续复用现有三张任务表，禁止第四条智能代理轮询；模型流量不进入这些任务表 |
| 中台服务端（健康、认证、用户、授权、注册、遥测、制品、治理、资产、控制台、审计、智能代理、执行实例、工作进程、工具、会话和指令） | `lib/brain/` `cmd/yufeng-brain/main.go`；授权契约 `proto/yufeng/grant/v1/grant.proto` | `GrantService` 已装配；生产写路径使用访问令牌和能力令牌双重授权 |
| 三本账持久化、内嵌或外部消息服务器与调度器 | `lib/store/` `lib/eventbus/` `lib/brain/scheduler.go` | JetStream 事务发件箱已建；调度使用复核时间和强制到期时间；控制台静态托管见上表 |
| 资产侧指令轮询与逐步回执 | `lib/brain/command.go` `cmd/yufeng-host/executor.go` `journal.go` | Linux/OpenWrt 只执行六个本机白名单原语；命令带租约和代次，步骤记录意图、副作用、结算与补偿；路径穿越、符号链接、非白名单服务、未知原语和重启后的不明结果均失败关闭 |
| 统一智能代理运行时 / 工具网关 | `agents/runtime`；入口 `cmd/yufeng-jarvis` `cmd/yufeng-agentd` `cmd/yufeng-run`；`lib/brain/toolgateway.go` `agent_catalog.go` `case_delegation.go` `agent_run_saga.go` | 生产贾维斯的持久 `Generate` → 工具 → 确认循环已建，案件审批与短命 Agent 委派也已建；租约轮换不丢能力令牌，检查点随重新领取恢复；执行实例工具按意图、效果和结算三个阶段执行可恢复补偿事务，结果未知时失败关闭；`DescribeTool`、签名技能渐进披露和只读调查执行实例已建 |
| 执行实例本地监督代理 | `agents/runtime/supervise.go`、`sandbox_*.go`、`broker_socket_*.go`、`supervisor_watch.go`、`environment.go` 与 `cmd/yufeng-agentd`、`cmd/yufeng-run`；协议见 `docs/api.md` 第 18.3、18.7 节 | 监督进程以独立工作负载身份代持双令牌，经 Unix 本地套接字或 Windows 命名管道转发工具、进度与终态回执；丢租约、墙钟到期或监督进程被杀均终止完整进程树。Linux 使用架构校验、Landlock 与 seccomp 允许列表；macOS 默认拒绝用户数据读取、写入、联网和派生执行；Windows 缺 AppContainer 时能力挑战失败并禁止领取调查 |
| 技能与工具目录 | `agents/skills`、`agents/tools`、`lib/brain/agent_catalog.go`、迁移 `00033_agent_catalog_pins.sql`；契约 `docs/api.md` §18.10.5 | 已建：工具描述与技能走签名制品生命周期；`ListTools`、`DescribeTool`、`ListSkills` 和 `LoadSkill` 负责渐进披露，目录钉死到当前认知回合，只能绑定已注册服务端实现或已验签修复程序；签名技能不执行脚本、不补权 |
| 模型上下文协议连接器 | 目标是中台内的模型上下文协议连接器桥；契约 `docs/api.md` §18.10.8 | 只有安全边界，尚无生产连接器桥；只读调查沙箱的实现状态见“执行实例本地监督代理”，危险修复程序仍要求更强的 Linux 控制组硬边界，缺失时必须失败关闭 |
| 生产模型网关 | 契约 `proto/yufeng/model/v1`（`Generate`、迁移用 `CompleteChat`、槽管理与探测）；实现 `lib/brain/onboarding_service.go`、`model_gateway.go`、`model_generate.go` 与 `model_gateway_stats.go` | `Generate` 校验双令牌、认知回合、租约代次和账本序号，保存上下文清单、逻辑生成、所有物理尝试与唯一接受响应；生产贾维斯禁止 `-model-url` 且不再调用 `CompleteChat`。执行实例车道的模型尝试已接持久多维预算预留与结算 |
| 模型客户端与测试提供者 | `agents/modelgateway/provider.go`；测试提供者仅在 `fake_provider_test.go` | 生产包只编译 HTTP 客户端协议适配；确定性回答只供单测，`-dev-insecure` 不启用固定回答；**不是**生产出口 |

## 四条走读路线

1. **一条请求怎么穿过数据面被拦截**：壳按入口姿态写状态码；核是规范视图 → `Inspect` → `Gate`。带 `yufeng_dev` 构建标签的本地正则纵切片由 `make demo-init && make run` 启动，不进入发行二进制。生产世代清单选装 Coraza，检测键策略全量生效后才返回 403；无策略攻击返回 200。验收：`scripts/production-live.sh`、`TestCRSHitUploadsDetectedUnmitigatedOnce`。
2. **一个制品的一生**：从 `proto/yufeng/artifact/v1/v1.proto` 看制品结构 → `lib/kernel/release.go` 状态机 → `lib/brain/govern.go` 治理服务（生产只收提案意图并编译 `PolicyCandidate`；工具网关硬拒无 intent 的 `KIND_RULE` / `rules/v1`）→ `lib/brain/artifact_service.go` 下发 → `cmd/yufeng-edge/brain.go` 装载/退休。
3. **一个智能代理指令**：开发演示路径只在测试或 `yufeng_dev` 构建中保留。生产会话、案件研判与认知型执行实例都先建钉死来源游标和受管 Agent 配置的认知回合；贾维斯领取后只调用 `Generate`，模型请求与响应按有序记录落账，工具仍经工具网关。检查点等待时释放租约，唤醒或租约过期后按新租约代次和持久序号恢复。
4. **一份资源如何被看见**：单元注册生产能力 → 中台编译签名资产世代与模型档案 → Edge 同步上传脱敏事件，同时把有界规范化流量异步交给邻近 ModelSide → ModelSide 只向中台批量上报类型化结果 → 中台在入账事务内冻结检查票据、聚类并按告警创建认知回合。沿注册、世代列表、事件上传、模型结果、票据、聚类版本、认知回合与事件研判全链检索；原始流量上行、远端消息订阅或通用事件读取工具进入生产智能代理都是错误旁路。

## 现在进仓库先看什么

- 想懂"数据长什么样"：读六个数据契约 `.proto`（event/asset/artifact/plan/procedure/tool；共享枚举 common 不计入六项，各服务契约另见 docs/api.md §1），每个字段都有一句话中文注释；
- 想懂"检测器怎么接"：先读 `docs/glossary.md` 检测器 / 闸；活代码 `lib/edgecore/inspector.go` 与 `release_set.go` 的 `Gate`；`Detector.Evaluate` 只留给演示规则；
- 想懂“智能代理被授权做什么”：`lib/kernel/claims.go`——令牌声明的每个字段就是一条授权规则；
- 想懂"修复程序怎么写"：`procedures/steps.schema.json` 加 `proto/yufeng/procedure/v1/v1.proto` 顶部注释。

## 变更纪律

- 改网络行为或状态语义：先改 `docs/api.md`，再改 proto service；改数据消息字段或编码：先改 `proto/`，再同步文档。两者冲突时阻止合入并人工对齐；需要生成代码时执行 `make generate`，产物随仓库提交；
- 新术语：先入[术语表](../glossary.md)（含英文锚点）再使用；
- 按本图实施：以列出的路径打开第一处代码后，继续全仓检索同一服务方法、消息类型、表列、配置键和序列化字段，覆盖生产者、消费者、回放、部署与测试；文件清单永远不表示“只改这些”；
- 本地图与实现不一致时，视为实现未完成。
