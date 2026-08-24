# 御锋 2.0 设计

本文是中台资源路由、统一智能代理座架与虚拟补丁流量拦截层（L1）的工作设计。架构与选型以 [`architecture.md`](architecture.md) 为唯一权威，网络行为以 [`api.md`](api.md) 为准，名词以 [`glossary.md`](glossary.md) 为准。第 1、3、4 节分别细化中台、智能代理与流量拦截层目标语义，第 10 节记录仓库现状；二者不一致时按第 8 节迁移，不把纸面当成已交付。仓库目标版本只读取根目录 [`VERSION`](../VERSION)，已发布状态只读取 [GitHub Releases](https://github.com/ZionOVO/YuFeng/releases/latest)；只有同名非草稿 Release 的精确提交证据资产复核通过，才可宣称对应版本的软件发布与机器验收闭环。客户现场仍须记录真实上游、精确代理网段、证书核对、切换与回退责任人，才能宣布现场交付完成。

## 0. 审查采纳

2026-08 外部设计审查对照仓库实现后的结论如下。四个核心选择不变：Agent 不进请求数据路径；检测器只出发现、拦截只认已签名策略；中台承担授权与审计，边缘与设备侧确定性执行；L1、运行时约束层（L2）、冷补丁层（L3）共用「提案—授权—制品—执行—验证—回滚」。

审查指出的结构性问题，按「对照实现是否成立」决定去留。未采纳的不写进第 4 节。

| 审查主张 | 对照实现 | 决定 |
|---|---|---|
| 同步无发现不能当成漏检 | `lib/brain/triage.go` 对所有 `verdict=allow` 入队 `EVENT_TRIAGE`；第一阶段闭环把「放行且无规则」当成漏拦。正常流量同样无发现 | **采纳**。普通 `SYNC_NO_DETECTION` 只采样、不入队。`DETECTED_UNMITIGATED` / `DETECTED_UNMAPPED` 凭同步发现入队。只有 `SUSPECTED_MISS` 才要独立证据 |
| 策略不能按五类 `attack_class` + `target` 匹配 | 策略制品尚未落地；纸面第 4 节曾以攻击类为匹配键。代码里 `KIND_RULE` 正则同时检测并拦截 | **采纳**。匹配键改为检测键（DetectionKey）。五类只作报告分类与技能路由，是第一批允许自动治理的封闭集合，不是检测本体 |
| 映不上五类时用 `unmapped` 当漏检 | 早期设计曾把 `unmapped` 当漏检。代码尚无分类映射 | **采纳并改写**。新增研判原因 `DETECTED_UNMAPPED`，保留原始规则标识、标签、分数 |
| 截断体但无检查覆盖度 | `edgecore.Request` 已截断；`Verdict` 无覆盖度字段。Coraza 在 DetectionOnly 下遇体上限会 `ProcessPartial` | **采纳**。每个检查面输出 `FULL` / `PARTIAL` / `ABSENT` / `UNSUPPORTED` / `ERROR` |
| 身份、双向传输层安全协议（mTLS）、授予、`azp`、防重放必须先于生产 Agent 提案 | `Claims` 无 `azp`；工具网关只验一张令牌；`GrantService` 未装配；`keys.go` 在 brain 进程读私钥文件；生产传输层安全协议（TLS）未失败即关 | **采纳**。第一阶段闭环标为演示。生产提案前关闭身份协议（刷新续期、一次性引导、双令牌与 azp）、授予装配、TLS |
| canary 按 `request_id` 分桶 | `lib/edgecore/canary.go` 对随机 `request_id` 做 `sha256` 分桶。攻击者可重试直到未抽中 | **采纳**。默认按单元标识（`unit_id`）稳定分片；`request_id` 只做关联，不再当分桶键 |
| 漏检后 Agent 写任意正则 | 第一阶段确定性测试剧本写 `rules/v1` 正则；存在正则拒绝服务、过宽匹配、编码绕过 | **生产采纳**。生产旁路闸是受限的正向请求形状领域特定语言（RequestShape DSL）。演示正则只在测试编译单元，新提案不得再收任意正则 |
| 策略与检测器分别增量下发 | `ListReleases` 按条下发；`ReleaseSet` 按条装载。策略可能绑到未加载或已换版的检测器 | **采纳**。原子发布单位改为资产世代（AssetGeneration）；策略必须声明检测器与规范化器摘要 |
| 异步模型有类即可蒸馏成策略 | 异步模型未实现。纸面「有类走策略」在同步无发现时没有匹配输入 | **采纳**。模型只能补充已有检测键，或产生 `SUSPECTED_MISS`；不得编造可执行策略 |
| 管理面按路径跳过策略 | 纸面「健康检查与控制面前缀策略阶段跳过」。业务应用自己的 `/admin`、`/health` 会成为绕过面 | **采纳**。御锋管理面用独立监听端口；业务路径不做隐式绕过 |
| 单一存活时限同时表示复核与失效 | 制品只有 `ttl`，到期 `RetireRelease`。中台故障或无人复核时虚拟补丁会自动消失 | **采纳**。拆成 `review_at` 与 `hard_expires_at`，并写明过期行为 |
| 先入账再发内部流 | `api.md` 已写：NATS 分发失败不回滚已入账事件。异步检测会丢 | **采纳**。事务发件箱 + 幂等消费者 |
| 把原始流量送入中台或让模型进程直订 NATS | 原始流量只在已解密 HTTP 入口可得；中台只需要治理事实 | **采纳 Edge 邻近旁路**。Edge 规范化后交给邻近 ModelSide，Brain 只收无原文结果；多站联动仍走事件账与攻击活动 |
| 分析打分与 Agent run 共用 `PollWork` | `WorkItem` 全是 `run_id/plan_ref/toolset/capability_token` 语义 | **打回混装**。模型使用专用 Edge 输入与 Brain 批量结果协议；run `PollWork` 保持原义 |
| 单元能力广告、转发策略和消费授权混成一个矩阵 | 当前 Registry 只有资产系统能力；worker Bindings 已要求来自服务端授予 | **拆成三轴**：生产能力只广告；世代只选采样/证据/车道；实际可见性只看服务端档案与 Bindings |
| CheckTicket 可由 edge/brain 各自补默认值重建 | 当前有两套构造函数，brain 曾回退固定摘要与默认模型车道 | **打回双真相**。研判票据由 Brain 按已接受 Event + 钉死历史世代冻结；模型输入只用 Edge 生成的版本化规范流量 |
| 把 brain 拆成 api / authority / signer 三个部署单元 | 当前一个 `yufeng-brain` 进程持账本、工具网关和遥测入口；签名私钥已由独立签名套接字持有，治理池与受限流量池仍由同一中台进程持有 | **缓做部署拆分**。已落地 `Signer` 接口、数据库角色隔离和签名端类型化输入；逻辑上仍是一个中台，不得把这些纵深防御描述为完整信任计算基拆分 |
| 画像核、异步模型、L2/L3 业务本档必交 | 均未实现 | **不采纳为第一生产版范围**。第一生产版收缩为：身份绑定的治理中台 + 确定性 L1 边缘核 + Coraza 只检测 + 精确检测键策略 + 受限形状语言 + 类型化提案 + shadow/canary/守护回滚 |
| 继续先扩检测算法 | 代码主拦截器仍是正则 | **不采纳**。先冻结检测语义、HTTP 规范化、身份绑定和自动晋升，再装 Coraza |

审查原文中的“生产阻断”对应上表“采纳”行；所谓“契约定稿阶段”只描述实施顺序，不给问题严重度分级。

第一生产版一句话：

> 一个经过身份绑定的治理中台 + 一个确定性的 L1 边缘核 + Coraza 只检测 + 精确检测键策略 + 受限形状语言 + Agent 类型化提案 + shadow/canary/守护回滚。

上表“对照实现”列是 2026-08 审查时的仓库快照，不是当前完成证书。仓库当前已有 Inspector 与 Gate 活路径、中台单点不可变研判票据、Edge 邻近模型旁路、智能代理可恢复认知与补偿、持久预算、只追加权威审计、签名技能与只读调查执行实例。单站点企业试点的发布、真实入口、故障、安全、容量和恢复证据见 [`delivery-evidence.md`](delivery-evidence.md)；客户现场变更记录关闭前不得宣布现场交付完成。

---

## 1. 架构

### 1.1 切分

必须粘在一个进程里的：信任根（签发、验签、授权）、三本账的事务一致、人机回路（会话、授予、推进）。这是中台 `yufeng-brain`。

必须离开中台的：流量拦截（发生在入口）、设备上的原语执行、短命修复进程。中台宕机时，数据面仍按本地已验签制品裁决。

因此：一个中台、两类边缘、五种契约；Agent 运行时是独立进程，设备上没有智能。

```
                    人 / yfctl
                         │ ⑤ SessionService
                         ▼
┌────────────────────────────────────────────┐
│ yufeng-brain                               │
│  认证/用户/授予  治理  三本账  审计链         │
│  投影/聚类/路由  回放门禁  内部总线（NATS）   │
│  工作控制面：Agent 指令 · run · 工具          │
└─────┬──────────────┬───────────────┬───────┘
      │①②③           │ Poll / Invoke │④ PollCommands
      ▼               ▼               ▼
 yufeng-edge     yufeng-jarvis    yufeng-host
 反代或外部授权   agents/runtime    原语执行器
      │          yufeng-agentd ─────────┤
   业务流量           │ 本地监督代理    │
      ▼          yufeng-run             │
   被保护应用     （无网络凭证）         │

 yufeng-edge ──有界规范流量队列── yufeng-modelside
 yufeng-modelside ──有界结果队列── brain（无原文）
```

| 组件 | 放置 | 原因 |
|---|---|---|
| 控制台接口、治理内核、PostgreSQL 三本账 | 中台 | 信任根 + 账本 + 人机回路 |
| 贾维斯、agentd、run | 独立进程，可与 brain 同机 | 智能运行时不得嵌进信任根 |
| 同步引擎 + 策略裁决 | `yufeng-edge` | 拦截必须在入口，微秒档进程内 |
| 原语执行 | `yufeng-host` | 在机或经跳板；中台不持设备壳 |
| 异步模型 | Edge 邻近、可独立主机 | 原始流量留在防御网络；同机 Unix 域套接字，跨主机相互传输层安全协议 |
| 沙箱 | 中台亲和、可外移 | 默认无网，只消费冻结投影 |
| 外部模型端点、情报源 | 仓外 | 只经约定接口；原始流量不出户 |

### 1.2 中台

装配入口 `cmd/yufeng-brain/main.go` → `lib/brain/server.go` `NewMux`。中台不跑模型循环、不转发业务流量。下表“代码”列是设计定位；若与第 10 节的当前实现状态冲突，以代码与第 10 节为准。本表若干“未建”描述只保留 2026-08 审查时的差距背景。

| 块 | 职责 | 代码 |
|---|---|---|
| 认证 / 用户 | 登录会话、引导管理员、用户增删改 | `lib/brain/auth.go` `users.go` `bootstrap.go`；`proto/yufeng/auth/v1` `user/v1` |
| 授予 | Tools × Bindings 记录（GrantService 已装配；语义以登录 `access` 与授予 RPC 为准） | `proto/yufeng/grant/v1/grant.proto` `lib/brain` |
| 注册 | 单元身份、资产绑定、生产能力广告、生产健康与心跳计数；能力广告不授权 | `lib/brain/registry.go`；`proto/yufeng/registry/v1`、`proto/yufeng/unit/v1` |
| 制品下发 | `ListReleases` 快照/增量游标 | `lib/brain/artifact_service.go` |
| 遥测与模型结果入账 | 事件或类型化模型结果插入；冻结 CheckTicket；事务发件箱派发；按聚类谓词入队 `EVENT_TRIAGE` | `lib/brain/telemetry.go` `model_result.go` `triage.go`；模型告警在单一事务中完成事件、推理、票据、案件与研判唤醒，复核样本只聚合案件 |
| 治理 | 提案/门禁/shadow/晋升/回滚/退休 | `lib/brain/govern.go` `govern_write.go`；状态机 `lib/kernel/release.go` |
| 回放 | 发布前语料回归 | `lib/replay/replay.go`，与 edge 共用 `edgecore.Evaluate` |
| 调度 | 复核到期、硬过期、守护回滚、策略自动晋升 | `lib/brain/scheduler.go` |
| 资产 | 登记、绑定单元、`max_auto_tier` | `lib/brain/asset_service.go` |
| 控制台读 | 看板、事件列表 | `lib/brain/console.go` |
| 审计链 | 追加哈希、校验；定期对外检查点（目标） | `lib/brain/audit.go` |
| Agent 控制面 | 注册/刷新/领指令/Ack | `lib/brain/agent.go` `instruction.go` |
| Run / Worker | 建 run；run 与分析两条类型化领取车道，共用身份/档案/Bindings 判定 | `lib/brain/agent_run.go`；分析车道目标实现待建，语义见 `api.md` §18.3 |
| 工具网关 | `ListTools` / `DescribeTool` / `InvokeTool` / `ListSkills` / `LoadSkill` | `lib/brain/toolgateway.go`、`lib/brain/agent_catalog.go` |
| 会话 | 人话入库并入队 `SESSION_MESSAGE` | `lib/brain/session.go` |
| 执行单元指令 | `PollCommands` / `ReportStep` | `lib/brain/command.go` |
| 签名 | `Signer` 接口；生产密钥由密钥管理服务、公钥密码标准 11 或独立套接字持有，brain 不直接读私钥文件 | 套接字签名已跑通；文件钥须 `-dev-insecure` |
| 账本 | 连接池、goose 迁移；生产按角色拆库权限（遥测写入者不能改发布账） | `lib/store/store.go` `lib/store/migrations/` |
| 内部总线 | 内嵌或外置 NATS；入账与投递走事务发件箱；只作为 brain 受控域内实现，不给边缘/远端 worker 凭据 | `lib/eventbus/`；库级发件箱 + JetStream 已建（compose 默认内存流） |

三本账（PostgreSQL）：

| 账 | 表（迁移 `00001_traffic_interception_core.sql` 起） | 不变量 |
|---|---|---|
| 事件 | `events` | 只追加，不改字节；模型结果另写关联记录 |
| 资产 | `units` `assets` `unit_assets` | 单元是进程身份，资产是保护对象 |
| 发布 | `releases` `release_assets` `release_feed` `release_counters` `release_timeline` | `release_id` 治理主键；`artifact_id` 内容地址 |

审计 `audit_entries` 贯穿三账和 Agent 执行账本，按全局序号形成只追加哈希链；Agent 条目用 `run_id`、`turn_id`、租约代次与预算标识建立可查询坐标，只保存脱敏摘要和非敏感计数。Agent 另有 `agents` `agent_tokens` `agent_instructions` `runs` `work_items` `sessions`（`00004` `00006` `00008`）；这些业务表保存状态或受控正文，不能替代审计链，也不得由进程内事件切片承担恢复。

发布状态机（编译期非法转换不可达）：常见路径 `draft → signed → shadow → canary → enforce → retired`。单机绑定单元数不足时，另有合法边 `shadow → enforce`（`PromoteEnforce`，`docs/api.md` §7.6），不是第二条状态机，也不是非法转换。精确检测键策略过 shadow 后可按门槛自动晋升；形状规则与宽范围策略必须另一用户 `promote_*`。

逻辑上仍是一个中台，权限分成三层，第一生产版不必拆成三个二进制：

```
brain-api     认证、会话、遥测、控制台、Agent 编排
     │
     ▼
authority     治理状态机、授权裁剪、策略编译、晋升判定
     │
     ▼
Signer / 密钥管理服务
              只签已通过确定性校验的结构体和能力令牌
              不接自然语言、任意 JSON、遥测或 Agent 生成的制品字节
```

发布与回滚使用不同权限。紧急回滚可用短期委托密钥，但不能用同一能力创建任意新策略。

### 1.3 边缘

本节带“当时”或“未建”的句子记录 2026-08 审查时的差距。现行完成状态以当前代码与第 10 节为准。

**数据面 `yufeng-edge`**

- 入口：`cmd/yufeng-edge/main.go`。正式构建只接受 `-brain` 模式并完成注册、世代拉取、心跳与上传；`-local-demo` 只存在于带 `yufeng_dev` 构建标签的开发目标，不进入发行二进制。
- 客户端：`lib/edgeclient/`（注册、ListReleases、UploadEvents、本地 spool）。
- 核：`lib/edgecore/`。四种入口壳先适配成同一规范请求视图，再 `Inspector.Inspect`（无 `Action`）；`Gate` 才出动作。演示 `KIND_RULE` 只作闸的旁路表。见第 4 节。
- 壳：`release_proxy.go` 反向代理。外部授权壳当时未建，现行已跑通（第 10 节）。御锋自身的健康检查与指标走独立监听端口，不在业务路径上隐式跳过策略。
- 验签公钥启动期本地预置，Register 只报指纹。
- 下发目标：拉取完整资产世代，验签与单调序号，编译成功后原子替换 `activeGeneration`；enforce 依赖失败则保留上一份已验证可用世代。

**执行面 `yufeng-host`**

- `cmd/yufeng-host/main.go` + `lib/brain/command.go`：轮询命令、逐步回执。
- 当时步骤一律标成功。现行未知原语 `unimplemented` / `failed_precondition`（第 10 节）；业务成功只认验证步骤。
- 正式执行只支持 Linux/OpenWrt 本机 `embedded` 模式和六个白名单原语；历史 `remote` / `network` 枚举不产生 Host 执行能力，安全外壳协议、厂商接口和远程自动安装均不交付。

**Edge 亲和模型算力**

- 异步检测：Edge 从已解密 HTTP 入口构造版本化规范流量，经本地有界非阻塞队列交给 `yufeng-modelside`；ModelSide 按签名模型档案推理与采样，只向 Brain 上报无原文结果。Brain 断连不停止本地推理，两个队列分别满时只丢旁路并计数。模型配置与身份独立于引导聊天凭据槽（`docs/api.md` §19.2）。
- 沙箱：程序演练、验证代码复现。未建。

### 1.4 五种契约与六个数据消息

边缘只说超文本传输协议上的 Connect。内部总线边缘不接。

| # | 契约 | 方向 | 服务 | 失败 |
|---|---|---|---|---|
| ① | 注册 | 单元→中台 | `RegistryService` | 心跳丢失标降级，记录保留 |
| ② | 制品 | 中台→单元 | `ArtifactService.ListReleases` | 断网用缓存 |
| ③ | 遥测 | 单元→中台 | `TelemetryService.UploadEvents` | 本地有界堆积 |
| ④ | 指令 | 中台→host | `CommandService` | 回执断流视为失败 |
| ⑤ | 会话 | 人↔贾维斯 | `SessionService` | 即时通讯不是传输 |

`AgentControlService` / `RunService` / `WorkerService` / `ToolGatewayService` 是 Agent 控制面，不计入五契约。`WorkerService` 在 0.2.0 只承载 agentd 的 run 工作项；历史分析车道只保留线缆编码，Brain 不再创建、租赁或完成该类工作。流量模型只走 Edge 邻近 ModelSide 专用契约。

六个数据消息（`proto/yufeng/*/v1/v1.proto`）：事件、资产、制品、修复计划、修复程序、工具描述。另加共享枚举 `common/v1`。进程内数据面接口 = Inspector + Gate（均不进 proto）+ 能力令牌声明（`kernel.Claims`）。`Detector.Evaluate` 返回 `Action` 只留给演示规则编译单测，不是目标同步口。

### 1.5 授权

人：用户会话 + 授予表展开的 Tools × Bindings；空 Bindings 拒写（引导未完成时 `docs/api.md` §19.5 白名单除外）。  
贾维斯：Agent 访问令牌证明进程，instruction 能力令牌证明本条指令的工具、范围与预算。agentd：worker 工作负载访问令牌证明监督进程，run 工作项能力令牌由 agentd 代持；`yufeng-run` 自身没有网络凭证。

三角色只是默认模板。操作员默认可提案/门禁/开 shadow，默认无 `promote_enforce`、无用户管理。同一主体对同一 `release_id` 不得既 propose 又 promote。超过资产 `max_auto_tier` 签不出令牌；L3 另加人审，不被该字段放宽。

### 1.6 资源流转：生产、可见性与编排分离

资源流转只复用现有五种契约和工作项长轮询，不另建「资源募集」远程过程调用。brain 是唯一治理投影者和路由器；Edge、ModelSide 与 Agent 之间不得互订。ModelSide 的规范流量是只在数据面内部流转的特例，Brain 只见无原文结果。

三类事实必须分别建模：

| 事实 | 谁决定 | 约束 |
|---|---|---|
| 单元能生产什么 | 单元注册时广告，中台按身份和实现档案校验 | 只说明兼容性，不授予读取、领取或转发权限 |
| 这段时间生产多少、保留什么、进入哪条车道 | brain 编译并签名资产世代 | 采样、证据和转发策略均钉死在世代；资产标签只能影响下一代 |
| 谁能看见并处理 | brain 的 worker / Agent 档案、Tools × Bindings、租约 | 生产广告不能扩大消费者可见性；实际目标由中台调度，不写进边缘 |

资源分层如下：

| 资源 | 生产与流转 | 消费者 |
|---|---|---|
| 关键事件 | 拦截、观察、已检出未缓解、未映射、检查不完整、检测器失败，100% 经 `UploadEvents` 入账 | 聚类、研判与审计 |
| 普通流量样本 | 同步无发现按资产世代内签名采样策略上传；协议完成前 1% 只是兼容回退 | 基线与漏检对照；默认不叫醒 Agent |
| 检查票据 `CheckTicket` | brain 在接受关键事件时，用该事件和钉死的历史世代确定性冻结；与事件、案件和发件箱同一事务提交 | 调查工具与智能代理研判；不再用于 Brain 侧模型推理 |
| 原文证据 | 仅对应 `unit_id` 本机短期、加密、有容量的证据环 | 家用档默认不可见；私有档需 `evidence.pull` 与审批；破窗需双人授权并到边缘取 |
| 单元运行日志和指标 | 注册、心跳与可观测域 | 运维系统；不得混成检测语料送给贾维斯 |

权威流转顺序：

```
Registry 能力广告
        │
        ▼
brain 编译签名资产世代（采样 / 证据 / 模型档案）
        │
        ▼
edge ──规范流量──► modelside ──类型化结果──► brain 事务入账 / 案件
  │                                           │
  └──脱敏 Event──► brain 冻结 CheckTicket────┤
                                              ├──贾维斯研判
                                              └──调查 run
                                                       │
                                                       ▼
                                      攻击活动账 / 新资产世代 / 后续请求
```

三条车道不能合并：ModelSide 只做规范流量异步推理与结果上报；贾维斯通过 `PollInstructions` 消费研判对象；agentd 通过 run `PollWork` 孵化短命执行实例。ModelSide 与 Edge 可以同机或位于同一受控防御网络，但不接 Brain 内部总线；多站点关联只走事件账、攻击活动身份和新资产世代，不形成分析器网格。

---


## 2. 修复连续谱 L0–L3

每一层只覆盖漏洞行为的一个子空间。一次发现可以同时挂多层动作；组合由策略角色写进修复计划。虚拟补丁（L1/L2）带存活时限，被同链上的冷补丁（L3）取代后自动退休。

| | L0 报告 | L1 流量拦截 | L2 运行时约束 | L3 冷补丁 |
|---|---|---|---|---|
| 改什么 | 不改现场 | 请求是否进上游 | 进程允许的系统调用 / 网络 / 文件行为 | 业务软件本身 |
| 发生在 | 中台账本 | `yufeng-edge` | `yufeng-host` 施加的约束制品 | `yufeng-host` 执行程序制品 |
| 手段 | 漏洞/攻击报告 | 检测键策略；仅 `SUSPECTED_MISS` 时收窄形状规则 | 首版不执行；后续须另立架构决策 | 首版只有验签暂存、允许目录原子替换、允许服务重载和确定性验证 |
| 生效 | 写出即完成 | 入口下一次请求 | live 对运行中进程立刻生效；spawn-time 仅下次启动 | 步骤执行完；可含重启 |
| 谁提案 | 研判 / 调查 / 报告角色 | Agent 交 TriageDecision，确定性协调器或人形成 ProposalIntent | 策略角色写入修复计划 | 策略角色写入修复计划 |
| 谁批准生效 | 自动（只读） | 精确检测键策略：门槛自动 canary/enforce。形状规则与宽范围策略：另一用户 `promote_*` | 沙箱演练通过后 shadow，再按门槛自动 | **永远人审**，先报告后授权 |
| 谁执行 | 无 | 边缘核（无 Agent） | host 按制品施加，run 只负责编排/回执 | host 逐步执行；run 经 agentd 本地代理监督回执，凭证由 agentd 代持 |
| 旁路资产 | 可以 | 可以 | 不可以 | 不可以 |
| 历史 `remote` 枚举 | 可以 | 否（不在机） | 不可以 | 不可以 |
| `embedded` 接入 | 可以 | 可以（流量仍走 edge） | 首版不可以 | 六个本机白名单原语 |
| 本档 L1 生产 | 接口可写报告，不交情报产品 | **本档交付** | 未知原语失败关闭 | 只在 Linux/OpenWrt 最小面执行，不得伪造 `SUCCEEDED` |

资产字段 `max_auto_tier` 是自动执行天花板：超过该层的动作签不出能力令牌。L3 不受该字段放宽——签发侧写死必须人审。

### 2.1 L0

输入：事件、异步模型追加记录、情报制品。  
输出：报告对象（只读），可附修复计划草案但计划里的 L1/L2/L3 动作尚未授权。  
执行：无现场副作用。贾维斯可用只读工具写报告；调查角色补证据。

### 2.2 L1

输入：同步引擎的检测键发现（已检出未缓解，或已检出但未映射到自动治理类）；或带独立证据的 `SUSPECTED_MISS`。普通「同步无发现」不是输入。  
输出：绑定检测键的策略制品（主）；仅漏检证据路径才出形状规则。  
执行：`yufeng-edge` 进程内。检测与拦截分离，见第 4 节。

### 2.3 L2

输入：修复计划中声明「系统调用行为」的动作，引用约束类制品（包过滤规则、安全计算模式配置、内核探针目标文件等）。  
输出：host 装载约束；live 手段免重启，spawn-time 只在可重启资产上使用。  
执行：`CreateRun` → agentd 孵化 run → run 经工具网关请中台把命令排给绑定该资产的 host → host `ReportStep`。无验证步骤不得把 run 标业务成功。未实现原语返回 `unimplemented` / `failed_precondition`。

### 2.4 L3

输入：修复计划中声明冷补丁的动作，引用程序制品（信封 + `procedures/steps.schema.json` 步骤体）。步骤可标「须人工确认」。  
输出：软件被改；可重启；成功后中台按取代指针退休同链 L1/L2。  
执行：与 L2 同一条 run/host 管道，额外约束：

1. 中台拒绝签发含 L3 写原语的能力令牌，除非审计链上已有该 `release_id` / `plan_id` 的人审通过记录。  
2. 人审通过只授权**这一份**程序引用 + 目标资产 + 预算 + 存活时限，不授权「以后同类自动打」。  
3. run 内操作员角色只分析失败、上报；不可逆步骤失败走补偿（已执行步骤的回滚引用），不能自行改成成功。  
4. 「已验证」只认程序里的验证步骤回执（健康检查 / 漏洞复验），不认执行器自报成功。

无官方补丁的设备：修复计划退化为长期 L1/L2 + L0 报告，不编造 L3 成功。

---

## 3. Agent

### 3.1 协议与进程

贾维斯与认知型 run 共用同一认知循环、账本与工具语义；网络身份不同。贾维斯自己连 brain，run 只连 agentd 的本地监督代理。

| 进程 | 生命 | 领取 | 产出 |
|---|---|---|---|
| `yufeng-jarvis` | 长驻 | `PollInstructions`（会话、事件研判、引导） | `Generate`、工具调用、委派、回写会话、写报告 |
| `yufeng-agentd` | 长驻监督 | `PollWork` | 独占 worker / 工作项凭证，孵化 `yufeng-run`，代理 Generate / 工具 / 回执 |
| `yufeng-run` | 一次性 | agentd 经本地套接字交付的委托书、工具投影、预算和存活时限 | 只调本地监督代理 / 等 host 回执；无网络凭证；结束即焚 |

`agents/runtime` 承载统一认知回合循环与可恢复补偿事务；三个 `cmd/` 目录下的入口只做装配，不设贾维斯专用远程过程调用。生产贾维斯已从 `CompleteChat` 切到 `Generate`；执行实例持久预算、监督进程死亡回收、工具意图、效果与结算、只追加权威审计、签名工具与技能生命周期和调查回执均已实现。用户主动改变目标、补充任务、审批与委派交互、模型上下文协议连接器桥和真实进程沙箱尚无生产实现，也不是本次企业试点的前置条件。

ModelSide 不在上表：它不是智能代理，不加载技能、不持执行实例工具集，也不使用 `PollWork`。它从 Edge 接收规范流量，按签名模型档案推理，再用专用批量结果协议上报 Brain；身份、队列、载荷与 run 完全隔离。

四类凭证：一次性引导令牌 → 可轮换刷新令牌 → 短期访问令牌；每条 Agent 指令/run 工作项另发能力令牌。Agent、run worker 与 ModelSide 身份分 audience：贾维斯走 AgentControl 身份；agentd 走 WorkerService 绑定 `worker_id + RUN_SUPERVISOR + 公钥/证书` 的工作负载身份；ModelSide 使用绑定单元、资产和证书的独立身份。贾维斯调用同时验访问令牌与能力令牌；agentd 代表 run 时同样验 workload access 与工作项能力令牌，均要求访问令牌 `sub` 等于能力令牌 `azp`。run 的能力令牌保持 `sub=run_id`、`azp=worker_id`，由 agentd 代持并附加；run 自身看不到令牌。ModelSide 没有业务能力令牌，不能调工具或模型网关。

工作项领取：工作项 Bindings ⊆ worker 档案 Bindings；空档案不能领。

#### 3.1.1 认知座架与恢复

持久对象固定为 `AgentThread → AgentTurn → AgentStep → AgentItem / ModelGeneration → ModelAttempt`。Thread 保存长期来源，Turn 钉死本次 `message_sequence`、`cluster_version/event_cutoff` 或 `work_id + plan_digest`；恢复禁止重读“最新输入”。

贾维斯 Turn 复用 `agent_instructions`，run Turn 复用 `work_items`，不建第四条轮询队列。执行历史按 `item_sequence` 只追加；steering、follow-up 与用户回答按独立 `input_sequence` 追加，在安全 checkpoint 才物化为 Item。每次推进必须同时匹配 `lease_id`、所有权隔离栅栏 `lease_epoch` 与 `expected_item_sequence`。正常续租保持 lease id / epoch；释放后重新领取才换 id、增加 epoch。

模型生成拆成逻辑 `ModelGeneration` 与物理 `ModelAttempt`。一次 generation 可因恢复产生多个 attempt，但 transcript 至多接受一个响应；每个 attempt 分别预留和结算预算。供应商已收到请求而响应未落账时结果未知，不能宣称物理调用恰好一次。工具副作用同理使用 intent → effect started → settlement；不可重放工具跨过副作用边界后没有结算，只能进入结果未知或人工对账。

Turn 等待子 run、审批或用户输入时落 checkpoint、释放租约；落定后唤醒原 instruction / work item。`AckInstruction` 只关闭终态 Turn，不承担暂停语义。详细状态机与字段见 `docs/api.md` 第 18.10 节。

### 3.2 三层「角色」不得混用

| 层 | 词 | 管什么 |
|---|---|---|
| 平台账户 | `USER_ROLE_ADMIN` / `OPERATOR` / `VIEWER` | 人登录后的默认工具模板 |
| Agent 授予模板 | `orchestrator` / `worker` | 中台给进程的默认模板；实际权限仍是 Tools × Bindings |
| 执行实例岗位 | `triage` / `investigator` / `strategist` / `operator` / `verifier` / `reporter` | run **内部**分工，不是第二套授权枚举。授权只看出生时令牌 |

岗位尚未类型化为独立服务。落地方式：`CreateRun` 的 `role` 是提案，服务端按调用者授予裁剪后写入工作项；执行实例按该字段渐进加载已签名且激活的技能制品，工具集仍以能力令牌的工具白名单为准。技能只能提供知识、工作法和工具建议，不能补权，也不能在进程内当脚本执行；可执行步骤必须成为签名修复程序。

### 3.3 岗位在一条发现上的分工

| 岗位 | 典型 Tools | L0 | L1 | L2 | L3 |
|---|---|---|---|---|---|
| 研判 triage | `triage.get`、开调查 run、标优先级 | 分拣 | 区分已检出未缓解、已检出未映射、带证据的漏检；普通无发现不叫醒 | 不执行 | 不执行 |
| 调查 investigator | `ticket.get` / `cluster.get`、读资产投影、调回放、写证据候选 | 补证据 | 提交证据候选，由 brain 绑定可信 `evidence_refs` | 给约束制品找行为签名 | 给程序填复现步骤 |
| 策略 strategist | 写修复计划（多层动作） | 计划可含「只报告」 | 往计划里挂策略/形状规则动作 | 挂约束制品动作 | 挂程序制品动作；**不签发执行令牌** |
| 操作 operator | 对 host 的可逆原语、L1 研判完成 | — | 只交 TriageDecision，由协调器决定是否成案 | 在 run 里下发约束 | 仅在人审后的 run 里执行程序步骤 |
| 验证 verifier | 只读探针、健康检查、漏洞复验 | 报告可引用其结果 | 确认拦截后同类请求 403 | 确认约束生效 | 确认补丁后漏洞不再复现；无此回执不得标 L3 成功 |
| 报告 reporter | 写 L0 报告、会话回复 | 主笔 | 记录已上的虚拟补丁 | 同左 | 记录人审与执行结果 |

贾维斯是 `orchestrator` 模板的长驻进程：把上述岗位的工作**拆成指令或 run**，自己做会话与 L1 研判循环。它不替代 host，不在设备上打补丁。

### 3.4 从发现到冷补丁（含未来 L3）

```
已落账研判对象（edge 事件 / 带证据的异步模型 / 情报 / 人工报告）
        │  按资产+路由+方法+检测键或漏检证据类型聚合
        ▼
brain 冻结 TriageObject；贾维斯领 EVENT_TRIAGE（`PLAN_REQUEST` 本档不得入队）
        │
        ├─ L0：开 reporter run 或自己写报告
        ├─ L1：提交 TriageDecision；确定性协调器派生检测键/证据后编译 ProposalIntent
        └─ 需要 L2/L3：调 strategist 纯函数（特征 × 资产能力矩阵 × 压力）
                 → RepairPlan{ L1 动作, L2 动作, L3 动作, 取代指针 }
                 → 人看到 rationale
                        │
           L2 动作 ──────┤  CreateRun(operator/verifier)
           L3 动作 ──────┤  人审通过后才 CreateRun
                        ▼
              agentd 孵化 yufeng-run，并提供本地监督代理
                        │
                        ▼
              工具网关把命令排给 host
                        │
                        ▼
              host 逐步回执；verifier 步骤决定是否业务成功
                        │
                        ▼
              L3 成功 → 中台按 supersedes 退休同链 L1/L2
```

策略角色的决策是纯函数，输入输出进审计，可回放。它**不算**授权。L3 授权是人在治理/控制台上对那条计划动作的单独通过。

### 3.5 L3 在 Agent 侧的具体约束

- `CreateRun` 若 `plan_ref` 指向含 L3 且无人审：`permission_denied`，不建 run。  
- 人审记录进审计哈希链，字段至少：`plan_id`、动作下标、程序 `artifact_id`、目标 `asset_id`、审批人、时间。  
- 工作项 Bindings 必须等于（或被包含于）人审单上的资产集合。  
- 工作项能力令牌 Tools 只能是该程序用到的原语 + 只读探针；不含 `govern.promote_*`、不含对其他资产的写；令牌由 agentd 代持。
- run 不持 access / refresh / capability token 或客户端证书；agentd 丢失工作项租约时关闭本地代理并杀完整子进程树。
- host `ReportStep`：未知原语不得 `SUCCEEDED`。  
- 程序中途失败：run 内 operator 岗位分析并 `FailWork`；补偿步骤按程序里的回滚引用执行；不能把失败改写成成功。  
- 冷补丁落地后：调度器对 `supersedes` 指向的 L1/L2 发布写退休墓碑；edge/host 拉取后卸载虚拟补丁。

L1 生产关闭不实现上述执行，但 `CommandService` / host / `CompleteWork` 必须走拒绝分支，状态可查询。

### 3.6 流量调查案件与跨平台执行池

流量审查不向 Agent 投递日志流。brain 先把有界审查候选按单资产聚类为案件，Jarvis 只读脱敏案件并请求证据或委派短命调查 run。案件冻结一个受管 Agent 的稳定身份、配置摘要、工具与资产范围；`agentd` 启动的 `yufeng-run` 才是该 Agent 的执行实例，模型、工具、结论和审计均归属它而不是 Jarvis。一次性证据批准只授权现有模型网关处理冻结片段；Jarvis 与 worker 均不获得边缘原文读取能力。

标准中台增加一个初始并发为一的 agentd，批准后可在二十四小时内提高到最多四；这只是任务并发和预算变化，不产生新的容器控制权限。外部 agentd 由用户在 Linux、Windows 或 macOS 安装后主动注册。三平台共享工作项、账本与本地代理协议，进程控制和沙箱由平台适配层实现；缺少经过挑战验证的强制沙箱时不领取流量调查。`yufeng-host` 的正式执行面只包含 Linux/OpenWrt 的只读探测、已签名制品暂存、允许目录内原子替换、允许服务重载和确定性验证；远程登录、厂商接口、任意命令与包升级不在交付范围。

---

## 4. L1：流量怎么检测、怎么拦截

L1 只回答两件事：这次请求在已检查字节上是不是已知攻击形状；该不该在入口挡住。不改业务软件。Agent 不在这条请求路径上。

对照过 `sentry-docker`（Coraza 只检测、策略才拦、机器学习在路外）和 `safeshield`（参数画像即拦截、C/cgo 训练出核）。下面是审查后的目标语义。

### 4.1 一次请求（目标）

```
客户端
  ├─ 反代拦截 ──────────────► yufeng-edge 壳（谁写状态码、失败开/关）
  ├─ 外部授权拦截 ──网关──►     │
  └─ 已解密 HTTP 复制入口 ──►    │
                               ▼
                    同一 HTTP 检查配置档
                    → CanonicalRequestView
                               ▼
                    同步 Inspect（只出发现 + 覆盖度，无 Action）
                               ▼
                    Gate（只认世代内策略 / 形状；合取姿态与发布状态）
                    到此本次请求结束
                         │                         │
                         │有界规范流量             │契约③脱敏事件
                         ▼                         ▼
                 yufeng-modelside            yufeng-brain
                 异步推理+签名采样       事件账 / CheckTicket
                         │无原文批量结果          │
                         └──────────────────────────►│
                                                   ▼
                                     聚类研判 / 调查执行实例
                                                   ▼
                       攻击活动 / 提案意图
                    回闸：新世代 → 后续请求
```

403 只允许：入口姿态允许拦截 ∧ 发布状态允许 block ∧ 策略或形状为 block ∧ 覆盖度满足。观察壳永远不得 403。hold-and-forward 默认永不做。

活路径已按上图拆开：`Inspector.Inspect` 只出发现，`Gate` 唯一持有 `Action`，世代清单选装眼睛，入口姿态与发布状态两轴分开。第一阶段闭环的演示正则仍作为闸的旁路表，不能让新眼睛单靠接口返回值 403。

### 4.2 边缘观察与中台研判必须分开

同步检测器只能证明「它产生了发现」，不能证明「未产生发现的请求是攻击」。正常请求同样没有发现。

#### 4.2.1 边缘观察状态

| 状态 | 含义 |
|---|---|
| `SYNC_DETECTED` | 至少一个检测键命中 |
| `SYNC_NO_DETECTION` | 已按配置完整检查，无发现 |
| `INSPECTION_PARTIAL` | 至少有一个必查面不是 `FULL` |
| `INSPECTION_ERROR` | 检测器加载、解析、超时或执行失败 |

#### 4.2.2 中台研判原因

线上 / JSON 只许 `TRIAGE_REASON_*` 全名（`docs/api.md` §18.1.2）。下表短名是散文。

| 原因（线上全名） | 何时成立 | 是否入队 Agent |
|---|---|---|
| `TRIAGE_REASON_DETECTED_UNMITIGATED` | 同步有检测键，当前世代无对应 enforce 策略 | 是（聚合后） |
| `TRIAGE_REASON_DETECTED_UNMAPPED` | 有原始规则发现，但不属于当前自动治理五类 | 是（聚合后）；默认只出 L0；不得按五类出策略，不得自动晋升 |
| `TRIAGE_REASON_SUSPECTED_MISS` | 同步无发现，但存在下列独立证据之一 | 是（聚合后）；只许形状规则或升级同步检测器 |
| `TRIAGE_REASON_INSPECTION_INCOMPLETE` | 请求未被完整检查，不能判断漏检 | 否 |
| `TRIAGE_REASON_DETECTOR_FAILURE` | 检测器失败 | 否 |

边缘观察与中台研判不是同一套枚举。映射：

| 边缘观察（可叠加） | 中台研判 |
|---|---|
| 有检测键，无论覆盖度是否 `FULL` | 无对应 enforce → `DETECTED_UNMITIGATED`；有键但不属五类 → `DETECTED_UNMAPPED`。`SYNC_DETECTED` 优先于覆盖度 |
| 无键，且任一面为 `PARTIAL` / `UNSUPPORTED` / `ERROR`（`ABSENT` 不算） | `INSPECTION_INCOMPLETE` 或 `DETECTOR_FAILURE` |
| 无键，已按配置完整检查 | `SYNC_NO_DETECTION`（只采样） |

`SUSPECTED_MISS` 的合法证据：异步模型高分且同步无发现；人工报告；漏洞复现或回放；上游应用、运行时自我保护、蜜罐或 L2 的独立异常；可信漏洞情报附带的请求复现。第一生产版不启用模型漏检；该原因只认人工、回放与情报复现。

**普通 `SYNC_NO_DETECTION` 不创建 Agent 指令，只按采样策略入账。** 关键事件（拦、观察、未缓解、未映射、不完整、检测器失败）100% 入账。普通无发现当前以 1% 为协议未完成时的兼容回退；目标语义是资产标签只作为 brain 编译输入，由 brain 把采样率写入并签名到下一份资产世代。运行中不得靠未签名标签或本地参数即时改率。

#### 4.2.3 叫醒前必须聚合

聚类身份（决定是否同一条指令）：`asset_id` + 路由模板 + 方法 + 检测键或漏检证据类型。**覆盖度枚举和时间窗不进身份**，只进计数与附件，避免攻击者用截断或分块打散聚类。翻页窗默认 15 分钟；窗内同身份至多一条 `pending`/`leased`；代表取最高异常分，同分取最早 `event_id`，最多 5 个；空闲 2 小时关聚类。brain 为 `cluster_id` 创建或复用 AgentThread，再创建钉死 `cluster_version` 或 `event_cutoff` 的 Turn；指令 `payload_ref=turn_id`。一请求一任务禁止。

第一阶段 `triage.go` 谓词（`allow` 且无 `KIND_RULE`）只服务带 `yufeng_dev` 构建标签的开发演示；正式构建没有 `--demo-triage` 参数，生产必须使用上表语义。

#### 4.2.4 统计窗、审查候选与代表选择

支持 `TrafficReviewPolicy` 的边缘每五分钟上报一个不含原文的统计窗，并在同一窗口最多选择四个审查候选。关键流量全部进入计数，重复攻击只保留有界代表；普通流量不再随机逐条入账。brain 按资产关键性、未缓解/未映射发现、覆盖缺口、模型异常、路由新颖度、重复聚类和近期反馈排序，单案件最多取三个高风险样本与两个基线对照。数据库写入因此由窗口与候选上限决定，而不是由请求数决定。

启用只能按资产世代中的签名档位向前推进：`OFF` → `STATISTICS_ONLY` → `REDACTED_CASES` → `EVIDENCE_ON_APPROVAL` → `SHADOW_CANDIDATES`。前一档的运行证据和人工决定是下一档的输入；不回填历史原始流量，不因模型结论跳过 Shadow（影子观察）或自动进入 Canary（金丝雀）/Enforce（强制执行）。

### 4.3 检测键与发现

五类攻击标签（结构化查询语言注入、跨站脚本、路径穿越、服务端请求伪造、命令注入）只做控制台、聚合和 Agent 技能选择。同一类下有多条核心规则集规则、多个参数、多个端点、不同转换链和不同异常分数；按 `SQLI + body` 自动拦截等于在大范围路径上重新打开一组防火墙规则，不是给某个漏洞收窄虚拟补丁。

开放全球应用安全项目核心规则集的请求规则覆盖初始化、方法、扫描器、协议、multipart、本地/远程文件包含、命令执行、PHP、通用攻击、跨站脚本、结构化查询语言注入、会话固定和 Java 攻击等系列，远不止五类。五类是第一批允许自动治理的封闭集合，不是完整检测本体。

```
DetectionKey
  detector_id
  detector_version
  detector_manifest_digest
  rule_id
  phase
  target_location          // path | query | body | header
  target_selector          // query.id、json.user_id 等
  normalization_profile_digest

DetectionV1
  key: DetectionKey
  raw_tags[]
  attack_class?            // 可选，仅展示与路由
  taxonomy_version
  anomaly_score
  matched_variable
  evidence_span
  inspection_coverage_ref
```

- `DetectionKey` 才是策略匹配依据。
- `rule_id`、标签、分数和检测器摘要不能丢。
- `unmapped` 表示分类映射不支持，不表示检测器漏检。

### 4.4 检查配置档与覆盖度

截断体（推荐 64 KiB，命名常量）本身不能证明「后半段没有攻击」。四种入口壳看到的请求还必须先变成同一视图，否则解析差异就是绕过面。

`HttpInspectionProfileV1` 至少冻结：路径规范化、百分号解码轮数、编码斜杠策略、重复查询键、重复头、`Content-Length` 与 `Transfer-Encoding` 冲突、JSON 重复键、multipart 上限、解压策略、解压后体上限、头数量、参数数量、JSON 深度、multipart 段数。

四种入口壳都必须先适配成 `CanonicalRequestView`，再进入同步 Inspect，然后 Gate。活路径是 `Inspector.Inspect` 与 `Gate`；检测器不得再靠返回值 403。

客户端来源是与规范视图并列的检测元数据。直接对端与签名监听计划中的可信代理 CIDR 决定是否接受 `X-Forwarded-For`；解析结果可进 Coraza 连接信息，但不参与规范化摘要、检测键或 Gate。事件编译只保留边缘 HMAC 假名。

```
InspectionCoverage
  target: path | query | body | header
  status: FULL | PARTIAL | ABSENT | UNSUPPORTED | ERROR
  inspected_bytes
  total_bytes_known?
  parser_profile_digest
```

覆盖度语义：

- 精确的正向检测已在已检查字节中命中：可以按该检测键拦截，即使剩余 body 未检查。
- 负向或完整性谓词（「没有某字段」「结构不合法」「只允许这些字段」）：必须 `FULL`。
- `ABSENT` / `UNSUPPORTED` / `ERROR` 不得记成「无发现」。
- 外部授权没拿到 body 时，依赖 body 的策略不得参与。
- 截断、超时、解析失败必须进入 decision trace。

### 4.5 流量怎么检测

五层眼睛，档位不同。大模型与训练不出在同步档。异步结果永远够不到本次请求。

#### 4.5.1 同步：核心规则集（主眼睛）

- 引擎：Coraza，纯 Go，挂在 `yufeng-edge` 进程内。永远 DetectionOnly。
- 物料：冻结精确版本与 SHA-256，写入资产世代的检测器清单；验签后加载；换版 = 新世代；加载失败保留上一份已验证可用世代。贾维斯不能写核心规则集、不能改清单。
- 输出：`DetectionV1`，保留原始规则标识与标签；再经已签名的 `TaxonomyMapperV1` 映到五类或保持未映射。映射器输入输出、版本和摘要进事件。禁止在 worker 或边缘手写正则贴类。
- 御锋管理面（健康检查、指标）走独立监听端口。业务路径上的 `/admin`、`/health` 不隐式跳过策略。

讨论过、否决：核心规则集默认 enforce；每资产再发一整包规则集；规则集编进二进制。

基线清单已冻结：开放全球应用安全项目核心规则集 4.25.0，安全哈希算法 256 位摘要见 `procedures/http-inspection-baseline/core-rule-set-manifest.json`；偏执级别为 1，包含规则与 `sentry-docker` `wafcore/engine.go` 相同：`REQUEST-901/930/931/932/934/941/942`。`REQUEST-91x`、`920`、`913` 不装载、不进自动治理。

#### 4.5.2 同步：画像核（第二只眼睛，后接）

- 算法：参数级阈值，参考 safeshield，纯 Go（Aho-Corasick），禁止 cgo。
- 阈值表是制品；评估结果是发现，必须带检测键与覆盖度，不是 403。
- 第一生产版不是硬门禁。未装载：引擎列表不含画像，事件不得出现假命中。

讨论过、否决：画像当主拦截器；运行时 HTTP 管理面改 IP 黑白名单。

#### 4.5.3 异步：机器学习（路外）

- Edge 只从反向代理、HTTP 外部授权或其它已解密流量复制入口取得真实请求；不做交换机镜像抓包、传输控制协议重组或加密流量解密。
- Edge 形成 `normalized-http/v1` 规范流量：请求、单元、资产、世代、方法、路由、签名档案允许的请求头、查询参数、有界正文、内容类型、原始长度、截断状态和覆盖度。请求路径只尝试一次正文所有权转移，不等待模型、Brain、磁盘或消费者。
- `yufeng-modelside` 是独立 Python 服务，参考 sentry-docker ModelSide 的 TensorFlow 模型形状、字符编码、分组权重加载与批量推理；不复用其日志接入、Redis、消费者或结果上报代码。它不进 Edge 二进制、不持 Gate 权限。
- Edge 输入队列与 ModelSide 结果队列独立有界、非阻塞。ModelSide 按签名档案的模型版本、阈值、窗口、单元/路由上限和去重规则分类为 `MODEL_ALERT` 或 `REVIEW_SAMPLE`；Brain 断连不停止推理。
- Brain 只接收无原文的类型化结果。告警在同一数据库事务创建事件、推理记录、冻结票据、案件、事务发件箱与研判唤醒；复核样本先聚合案件，不逐条唤醒贾维斯。
- 三条出路：
  1. 同步已有检测键：模型只补充置信度或分类，可形成精确策略。
  2. 同步无发现且模型高分：只产生 `SUSPECTED_MISS`，走形状规则或检测器升级。
  3. 给不出稳定同步谓词：只形成 L0 报告，不编造可执行策略。
- 贴类必须走已签名 `TaxonomyMapperV1`，禁止 ModelSide 内临时正则。
- 不交付训练、不把权重编进平台二进制。ModelSide 同机优先 Unix 域套接字；跨主机必须相互传输层安全协议认证并限制在同一受控防御网络。原始流量永不进入 Brain。

#### 4.5.4 Agent 怎么接入检测

Agent **不读原始请求、不跑引擎、不订边缘、不订资源流**。只消费中台已经落账、按 4.2.3 聚合并冻结的研判对象。通用 `event.get/list` 即使按 Bindings 裁剪，也不能提供字段级投影边界，生产 Agent 不得使用。

| 通道 | 中台做什么 | Agent 看到什么 |
|---|---|---|
| 同步发现 | brain 把事件、检测与世代轨迹冻结进研判投影 | `triage.get`（无原文、无运维日志） |
| 已检出无闸 | 聚合后入队 `EVENT_TRIAGE`（未缓解或未映射） | 指令 + TriageObject + 仅策略的类型化提案 |
| 带证据的漏检 | 聚合后入队 `EVENT_TRIAGE`（`SUSPECTED_MISS`） | 指令 + TriageObject + 仅形状规则的类型化提案 |
| 普通无发现 | 采样入账 | 无指令 |
| 异步模型 | 追加记录；满足漏检证据时再入队 | 关联推理，仍无原文 |
| 人说话 | 只入队 `SESSION_MESSAGE` | 只能 `session.reply` |

需要深挖时，中台按 `AGENT_INVESTIGATE` 转发策略创建短命 run；工作项钉死不可变检查票据及摘要，调查实例只通过 `ticket.get` / `cluster.get` 读取字段级投影，并与贾维斯指令租约分开。调查结束、失败、取消和超时均落与票据绑定的终态回执；它不修改原 Event、不回改在途请求。单元运行日志、指标和心跳只进入可观测与注册域，不进入 TriageObject 或模型上下文。

聊天正文不是命令。解析「拦住它」直接出策略——已否决。

### 4.6 流量怎么拦截

闸门只有入口上的 `yufeng-edge`。host / 包过滤 / 进程内 Agent 不是 L1 拦截手段。

#### 4.6.1 资产世代（原子发布单位）

独立 `release` 不再是边缘的原子装载单位。资产世代是制品契约上的装载信封，不是第七个数据契约，也不是新的治理主键。

```
AssetGenerationV1          // 落在 artifact/v1，成员仍是 ReleaseItem
  asset_id
  generation_seq           // 勿叫 generation：心跳已占用该词
  parent_generation_id
  members[]                // 每条含 release_id + 已签名 Artifact
  min_edge_version
  not_before
  envelope_signature       // 只证「这组成员一起装」
  rollback_of              // 被替换的 generation_seq
```

检测器清单、规范化配置档、策略、形状规则、分类映射器、[采样策略](glossary.md#sampling-policy)、[证据策略](glossary.md#evidence-policy)（默认 `home`）、[证据摘要](glossary.md#evidence-digest)、[转发策略](glossary.md#forward-policy)都是成员制品，各自仍有 `release_id` 与 `artifact_id`。入口姿态不进世代。禁止用世代签名替代成员验签，禁止用 `generation_seq` 替换 `artifact_id`。

边缘：拉完整世代 → 验信封与每个成员 → 检查单调序号与依赖摘要 → 编译 → 成功后原子替换 `activeGeneration`。任一 enforce 依赖失败，保留上一份已验证可用世代（磁盘只留当前 + 上一份，共 2 份）。磁盘满：拒绝新世代、保留当前、单元标降级。普通坏条目可以隔离，但不能形成「新策略 + 旧检测器」的混合状态。

`not_before` 与能力令牌共用允许偏差 **60 秒**（命名常量）。未到点则等待同一 `generation_seq`，禁止跳号去装更新的世代。时钟落后导致一直吃不到新世代时打最高级告警，不静默卸已加载世代。

任一策略/形状状态变化（含回滚一条）都由中台编新世代。治理远程过程调用、心跳计数、事件轨迹仍用 `release_id`。`rollback_of` 指向被替换的序号；无签名回滚授权不得装更旧序号。破坏下载或编译失败走「留上一份」，这不是签名回滚，必须打告警且不得当成合法降级通道长期停留。

每条策略至少声明：`requires.detector_manifest_digest`、`requires.normalizer_profile_digest`、`requires.min_edge_version`。检测器摘要变化后，原策略不得静默继续生效，必须重新回放。

#### 4.6.2 主闸：策略制品

在尚未生产兼容前直接定 `PolicyCandidateV1`，不先发一个只按攻击类匹配的弱版本。

```
PolicyCandidateV1
  policy_id
  scope
    asset_id
    hosts[]
    route_template
    path_prefix
    methods[]
    content_types[]
    target_selectors[]
  predicate
    detection_keys[]
    min_anomaly_score
    require_match_present
    coverage_requirement
  action                  // log | block
  dependencies
    detector_manifest_digest
    normalizer_profile_digest
    min_edge_version
  governance
    scope_risk            // exact | route | prefix | asset_wide | class_only
    evidence_class        // crs_mapped | crs_unmapped | human | replay | intel | model
    evidence_refs[]
    replay_manifest_digest
    proposer
    human_sponsor?
    rationale
  lifecycle
    review_at
    hard_expires_at?
    expiry_behavior       // retire | keep_until_superseded | alert_keep
  rollout
    cohort_type           // unit | hmac_tenant
    shadow_limits
    canary_limits
```

原则：

1. 自动策略默认必须精确到资产、路由、方法、检测规则和参数位置（`scope_risk=exact`）。
2. `path_prefix=/`、`host` 空、选择器 `any`、仅按攻击类的候选自动升为 `asset_wide` 或 `class_only`，不得自动晋升，工具网关可直接拒收自动通道。
3. 模型输出的 class 不能绕过检测键要求。
4. `created_by`、资产、检测器摘要和 evidence 由服务端从可信记录填充。Agent 只提交非可信研判结论（TriageDecision）；brain 的确定性协调器从钉死聚类版本编译提案意图（ProposalIntent），再由治理内核编译制品。Agent 不得提交可信检测键、证据引用或任意制品字节。
5. 匹配：范围命中，且本次同步发现里存在策略声明的检测键，覆盖度满足 `coverage_requirement`。`log` 只观察。多条 `block`：短路，任一命中即拦。
6. 晋升是三元联合判定，不是只看范围。必须同时满足才可门槛自动 canary/enforce：
   - 范围：`scope_risk ∈ {exact, route}`；
   - 证据：`evidence_class ∈ {crs_mapped, human, replay}`。`crs_unmapped`、`model`、`intel` 与形状规则一律停在 shadow，必须另一用户 `promote_*`；
   - 覆盖度：回放语料上该键的覆盖度为 `FULL`，或正向谓词且语料未依赖未检查后缀。
   `DETECTED_UNMAPPED` 默认只出 L0 报告；若人坚持出策略，与形状规则同档，禁止自动晋升。核心规则集方法/协议/扫描器系列（`REQUEST-91x` / `920` / `913`）禁止进入自动通道。
   排除提案人关联单元的心跳计数。贾维斯令牌无 `promote_*`。决策 018 仍成立：没有 pending 审批态，另一用户发的是已有 `Promote*` 命令。
7. 生命周期：`review_at` 到点只产生复核任务，默认不自动卸闸；`hard_expires_at` 才按 `expiry_behavior` 卸闸或告警后保留。禁止用单一 `ttl` 同时表示复核与失效。

#### 4.6.3 旁路闸：请求形状（不是任意正则）

仅当研判原因是 `SUSPECTED_MISS` 时，工具网关才收形状规则。语言是受限正向形状，闭集算子只有：`method ∈`、`route_template` 或至少两段且非根的 `path_prefix`、从账本抄写的 `selector` 存在性、`len` 上下界、`charset ∈ {ascii_print, digit, alpha, hex, uuid}`。禁止正则、否定前瞻、回溯量词、字符类 `any`、单字符或根前缀、缺方法、无长度上界。源文 ≤ 2 KiB，选择器 ≤ 16。brain 编译；过宽 `failed_precondition` + `shape_too_wide`，非法 `invalid_argument` + `shape_illegal`。边缘只装编译后的确定性自动机，不解析源文。禁止 SecLang、禁止改基线引擎。

Agent 的 TriageDecision 不得携带检测键的 `target_selector`。可选形状草案里的参数选择器只是待验证断言；协调器编译的提案意图中，选择器只许从 `DetectionV1` 或事件账抄写。漏检路径没有检测键时，选择器只能是事件里已出现的参数名，不得发明新名。

晋升：到 shadow 为止。必须另一用户 `promote_*`。调度器不得自动晋升。

已在 enforce 的演示 `KIND_RULE` 正则：保留到硬过期或人工退休，迁移时不偷偷降级。新提案不得再收 `rules/v1` 任意正则。

#### 4.6.4 没有第三种 L1 闸

| 手段 | 是否 L1 拦截 |
|---|---|
| 核心规则集命中本身 | 否，只是发现 |
| 画像命中本身 | 否，只是发现 |
| 模型分数 | 否，追加记录；最多触发 `SUSPECTED_MISS` |
| 贾维斯进程内 return 403 | 否，不在数据路径 |
| host / 包过滤 / 安全计算模式 | 否，那是 L2 |
| IP/网段运行时封禁接口 | 否；若要做，做成策略或独立制品 |
| 按业务路径前缀跳过 | 否 |

#### 4.6.5 canary 分片

默认 `cohort_type=unit`：`sha256(unit_id || release_id)` 前 8 字节按大端取模。禁止用 `request_id` 当分桶键。`request_id` 仍由边缘随机生成，只进事件关联。

该资产绑定的单元数 `< ceil(100 / canary_percent)` 时，**禁止进入 canary**（含自动与 `PromoteCanary`）：只许 `RELEASE_STATE_SHADOW` + 另一用户 `PromoteEnforce` 直达 `RELEASE_STATE_ENFORCE`（`docs/api.md` §7.6）。禁止自动 enforce。否则单实例上 5% 会退化成整机 0% 或 100%，金丝雀从未挡包却可能凑满请求数自动全量。

第一生产版删除 `hmac_tenant`。本档不做多租户；客户端可申请的租户串会变成新的逃桶键。

#### 4.6.6 Agent 怎么接入拦截

不挡包。Agent 先提交 TriageDecision，brain 的确定性协调器再编译 ProposalIntent：

```
DETECTED_UNMITIGATED / 已有检测键的 DETECTED_UNMAPPED
    → TriageDecision(PROPOSE_POLICY / REPORT_ONLY)
    → 协调器从可信账本派生检测键与证据
    → ProposalIntent(policy) → 服务端编译 PolicyCandidateV1
    → gate → start_shadow
    → 仅 exact/route 且证据类合格且回放覆盖度合格时由调度器自动 canary/enforce
    → 世代下发 → 再打 403

SUSPECTED_MISS
    → TriageDecision(PROPOSE_SHAPE / ESCALATE_HUMAN / INSUFFICIENT_EVIDENCE)
    → 协调器校验非可信形状草案，并从事件账派生选择器
    → ProposalIntent(shape) → 服务端编译形状规则
    → gate → start_shadow → 停住
    → 另一用户 promote
    → 世代下发 → 再打 403
```

生产研判令牌只含取证只读工具与 `triage.complete`，不含 `govern.propose` / `govern.gate` / `govern.start_shadow` / `promote_*`。跨资产一律拒绝；扩大 scope 由协调器停止并审计。蒸馏不得把「模型有类、同步无键」写成策略。

### 4.7 接入壳与失败

同一核、同一世代、同一规范请求视图。入口姿态与发布状态是两轴。完整状态码矩阵见 `docs/api.md` 第 21 节。

首个企业试点的客户入口终止业务传输层安全协议（Transport Layer Security，TLS），御锋只处理已解密的超文本传输协议（Hypertext Transfer Protocol，HTTP）。业务证书与私钥不进御锋；控制面 TLS 只保护 brain 契约。反向代理是必交姿态，Envoy 外部授权按试点选择交付。未解密 TLS 的 HTTP 检查面是 `UNSUPPORTED`，不得产生假发现。

| 覆盖情况 | 反代拦截 | 外部授权拦截 | 侧载只告警 | 镜像/SPAN |
|---|---|---|---|---|
| 正向命中，即使其余 `PARTIAL` | **403** | 网关 **403** | 200 + `would_have_blocked` | 只记事件 |
| 负向/完整性谓词且该面非 `FULL` | 该策略不参与 | 同左 | 同左 | 同左 |
| `ABSENT` | 不罚 | 同左 | 同左 | 同左 |
| `UNSUPPORTED` | 不 503；依赖该面的策略跳过 | 同左 | 同左 | TLS 未卸时 HTTP 面默认 `UNSUPPORTED` |
| 超体 → `PARTIAL` | **413**，不转发上游 | 网关传来超体：**403**。没传 body：`ABSENT`，跳过 body 策略，**200** | 200，标不完整 | 丢/记不完整 |
| `ERROR` / `Rejected` | **400**，不转发 | 已拿到的字节畸形：**403**。通道超时：仍 200 | 200，标 `INSPECTION_ERROR` | 记错误 |
| 引擎崩溃 / 无 `request_id` | **503** | 单次 200；窗内超时率熔断才 503 | 丢样本，禁止 503 伤业务 | 丢样本 |

外部授权熔断半开：跳闸后默认 503；每秒放行 `ExtAuthzHalfOpenPerSec` 个真请求并记账；窗内超时率 ≤ 1% 保持 30s 合闸。禁止用合成健康检查当探测。新世代部分损坏：旧世代保持。合法大上传只许在世代里给该路由加大体上限。

### 4.8 讨论过的总表

| 题目 | 想法 | 结果 |
|---|---|---|
| 同步路径主拦截器是谁 | 核心规则集直接拦 / 画像主拦 / Agent 正则主拦 / 模型上路径 | **引擎只检测，策略才拦** |
| 策略匹配键 | 五类攻击标签 / 检测键 | **检测键**；五类只做报告 |
| 无同步发现 | 入队漏检 / 只采样 | **只采样**；漏检必须有独立证据 |
| `unmapped` | 当漏检 / 当未映射检出 | **`DETECTED_UNMAPPED`** |
| 核心规则集怎么上边缘 | 每资产发布 / 编进二进制 / 清单进世代 | **世代内的基线清单，加载≠拦截** |
| 基线默认 enforce | 新单元一上来就按规则集全拦 | **撤回** |
| Agent 还能否写正则 | 任意正则 / 收窄形状语言 | **生产只收形状语言**；演示正则不新收 |
| 晋升 | 都自动 / 都人审 / 精确策略自动、其余人推 | **精确策略自动、形状与宽范围人推** |
| 异步模型 | 有类即策略 / 只能补键或标漏检 | **只能补键或 `SUSPECTED_MISS`** |
| 发布单位 | 独立 release / 资产世代 | **世代原子替换** |
| canary 键 | `request_id` / 单元或稳定分片 | **单元**；单单元资产禁止 canary 自动通道；第一生产版无假名租户 |
| 流量入口 | 只反代 / 只外部授权 / 双形态 / 四姿态 | **四姿态**（反代 / 外部授权 / 侧载 / 镜像）；形态不进世代 |
| 超时与覆盖不足 | 一律开 / 一律关 / 分形态揉成一团 | **按面拆失败**：超体 413/400，不当无发现放行；观察壳不 503 |
| 检测器接口 | 返回 Action / 只出发现 | **Inspector 无 Action；Gate 才有** |
| Python 模型 | 进边缘二进制 / 必装 / 当闸 | **Edge 邻近独立 ModelSide**；不 403；原始流量不进 Brain |
| 人聊一句就拦截 | 聊天升级 `PLAN_REQUEST` | **否** |
| 管理面绕过 | 按业务路径跳过 / 独立端口 | **独立端口** |

### 4.9 已收口（写入 api 后实现）

| 点 | 决定 |
|---|---|
| 核心规则集文件与偏执级别 | 开放全球应用安全项目核心规则集 4.25.0、偏执级别 1；包含规则与 sentry-docker 对齐；安全哈希算法 256 位摘要见 `procedures/http-inspection-baseline/` |
| 画像核是否本档硬门禁 | 否；未装载则列表没有，不得假命中 |
| 模型不给类时本地贴类 | 禁止手写正则；只许已签名映射器 |
| 多条 `block` 重叠 | 短路，任一命中即拦 |
| 策略与形状规则同时命中 | 先策略后形状，两者都可 403 |
| Agent 研判经协调器成案且排除集为空 | 仅 `exact`/`route` 可自动 enforce；守护回滚必须先可用 |
| 未映射发现 | `DETECTED_UNMAPPED`，不能按五类出策略 |
| 外部授权超时 | 命名常量，推荐 50ms |

### 4.10 相对代码要动的挂钩

- `detector.go` / `engine.go`：同步口是 `Inspector.Inspect`（无 `Action`）与 `Gate`；`Detector.Evaluate` 只留给演示规则编译单测。
- `release_set.go`：按 `activeGeneration` 做策略匹配与形状旁路。
- 新：`HttpInspectionProfile`、`CanonicalRequestView`、策略索引、形状编译器。
- `canary.go`：分桶键改为 `unit_id`；单单元资产禁止进 canary 自动通道；删除 `request_id` 分桶。第一生产版无 `hmac_tenant`。
- `triage.go`：按研判原因 + 不含覆盖度的聚合身份入队。演示谓词只在 `yufeng_dev` 开发构建中可启用。
- `toolgateway.go`：收 `triage.complete`，拒绝 Agent 直填可信治理字段；确定性协调器从聚类、事件账与资产世代编译 ProposalIntent。`govern_write.go` 只收协调器或操作域用户的提案意图；调度器按 `scope_risk`、`evidence_class` 与 kind 分叉。
- `replay.go`：策略回放 = 同一视图 + 同一引擎发现 + 检测键匹配。
- 演示正则剧本只存在 `_test.go` 测试编译单元，不进入交付二进制；生产座架输出 `TriageDecision`，正式生成走中台的 `Generate`，不扩写 `CompleteChat`。
- `keys.go`：改为 `Signer` 接口。
- 制品下发：世代快照替换按条 `ListReleases` 作为边缘装载单位（查询接口可保留，装载语义变）。
- 外部授权新壳；管理面独立端口。
- 事务发件箱 + JetStream；异步 worker 后接。

### 4.11 头脑风暴后补上的硬默认

2026-08 第二轮五路审查后写入，避免「名词已采纳、机制未齐」。

1. **研判与提案分层**：Agent 的 `triage.complete` 只收 `cluster_id`、`disposition`、`rationale` 与可选非可信 `optional_shape_draft`，禁止可信检测键、资产、选择器、可信证据、创建主体、范围风险与证据类；夹带即拒绝。确定性协调器或操作域用户进入治理内核的 ProposalIntent 才是 `kind=policy|shape` + `cluster_id` +（策略必填）`detection_keys[]` 或（形状必填）`shape_source` + 可选收窄 `scope`，仍禁止客户端 `created_by`、摘要、`evidence_refs`、任意 payload 字节、`target_selector`。用户提交的键和形状只是断言，服务端必须用该聚类钉死版本验证，不能据此创造可信事实。
2. **评估序**：检测键策略 → 仍在役的演示 `KIND_RULE`（独立旁路表，不编入检测键索引）→ 形状规则。策略已 403 则短路。新提案硬拒 `rules/v1`。
3. **第一生产版不建远程证据库**。账本按脱敏只留摘要。人要原文：到该 `unit_id` 边缘取未上传的本地环缓冲（默认 15 分钟、加密盘、仅该进程可读）。Agent 与外部模型永不读。
4. **覆盖度失败语义按第 4.7 节矩阵**（完整表见 `docs/api.md` 第 21 节），禁止揉成「反代一律失败即关 / 外部授权一律失败即开」。拦截姿态超体 413、畸形 400，不当 503，不当无发现放行；观察壳不 503。外部授权单次超时仍 200；窗内超时率熔断后默认 503，半开每秒放行 `ExtAuthzHalfOpenPerSec` 个真请求，避免打满 50 ms 窗口买裸奔。
5. **签名信任根**：生产签名私钥已经由独立套接字持有，签名端只接受通过确定性校验的类型化对象，不接受自然语言、任意字节或任意 JSON；这收紧了私钥暴露面。`yufeng-brain` 仍持治理账、工具网关、遥测入口以及治理池和流量池两个数据库连接池，因此没有完成中台进程或地址空间的信任计算基拆分。发布与回滚必须是不同密钥或不同权限；不得把外置签名私钥或数据库角色隔离单独写成「整个中台信任计算基已收缩」。
6. **授予未装配**：生产构建 `GrantService` 未注册则拒启动。无授予或 Bindings 空：写远程过程调用 `permission_denied` + `grant_missing`。
7. **五类闭集**：`sqli` / `xss` / `path_traversal` / `ssrf` / `cmdi`，另加 `unmapped`。`taxonomy_version` 默认 `tax/v1`。分类映射器是世代内已签名制品。
8. **守护回滚优先于硬过期**。同一调度滴答内一个 `release_id` 只转一次 `retired`。
9. **超文本传输协议范围**：第一生产版 1.1 + HTTP/2 伪头映射进同一视图。HTTP/3 不做。WebSocket 只检握手；101 之后的帧 `UNSUPPORTED`，不得当无发现。
10. **入口姿态与发布状态两轴**；单元监听计划已签名；观察壳不得 403。
11. **证据策略**默认 `home`；摘要函数在世代签名范围内。
12. **hold-and-forward 默认永不做**；旁路满了丢旁路。
13. **Python 模型**只作为 Edge 邻近的独立 `yufeng-modelside` 服务（参考 sentry-docker ModelSide），不进 Edge 二进制、不当闸，原始流量不进 Brain。

---

## 5. 代码地图

本节目录树是 2026-08 审查盘点。现行定位以第 10 节、当前代码和 [`code-map.md`](code-map.md) 为准；冲突时不以本树的历史“现 / 未建”描述为准。

```
.
├── cmd/                          进程入口，只装配，不放业务循环
│   ├── yufeng-brain/main.go      中台：开库、迁移、密钥、调度器、Listen
│   ├── yufeng-edge/
│   │   ├── main.go               数据面入口；无 -brain 则本地装载演示制品
│   │   └── brain.go              中台模式：注册、拉发布、心跳、遥测上传、观察者
│   ├── yufeng-host/main.go       执行单元：轮询命令、逐步回执（当时伪造成功；现行拒绝未实现原语，第 10 节）
│   ├── yufeng-jarvis/main.go     贾维斯装配：注册、长轮询、把循环交给 runtime
│   ├── yufeng-agentd/main.go     run 监督（当时延时成功；现行真孵化 yufeng-run，第 10 节）
│   └── yfctl/                    命令行：演示密钥、签规则制品、发布
│       ├── main.go
│       └── publish.go
│
├── lib/
│   ├── brain/                    中台全部 Connect 服务
│   │   ├── server.go             NewMux：把下列服务挂到一条 HTTP
│   │   ├── health.go             存活/就绪/版本
│   │   ├── auth.go               登录、会话、自注册开关
│   │   ├── auth_support.go       令牌哈希、requireUser
│   │   ├── bootstrap.go          首次管理员
│   │   ├── users.go              用户增删改
│   │   ├── registry.go           单元注册、心跳计数、requireUnit
│   │   ├── artifact_service.go   ListReleases 快照/增量游标
│   │   ├── telemetry.go          UploadEvents；按生产研判原因聚类并创建钉死版本的最小 Turn
│   │   ├── triage.go             EVENT_TRIAGE 生产谓词；普通无发现不入队，演示谓词显式隔离
│   │   ├── triage_turn.go        研判 Thread / Turn 不可变输入投影与资产绑定
│   │   ├── triage_complete.go    非可信研判结论校验、确定性提案编译与影子启动
│   │   ├── instruction.go        指令种类与工具集常量
│   │   ├── govern.go             治理 RPC（提案/门禁/晋升/回滚/查询）
│   │   ├── govern_write.go       提案/门禁/shadow 的落库，给人与工具网关共用
│   │   ├── scheduler.go          过期/复核、守护回滚、自动晋升（须按 kind 与 scope_risk 分叉）
│   │   ├── asset_service.go      资产登记、绑定单元、max_auto_tier
│   │   ├── console.go            看板、事件列表/详情
│   │   ├── audit.go              审计哈希链追加与校验
│   │   ├── agent.go              Agent 注册/刷新/领指令/Ack
│   │   ├── agent_run.go          CreateRun、工作项领取
│   │   ├── toolgateway.go        工具调用与授权
│   │   ├── agent_catalog.go      签名工具与技能目录、认知回合钉死
│   │   ├── session.go            会话消息；只入队 SESSION_MESSAGE
│   │   ├── command.go            host 领命令、回执
│   │   └── keys.go               现状：读私钥文件。目标：Signer 接口
│   │
│   ├── kernel/                   治理内核（信任根，无输入输出）
│   │   ├── claims.go             能力令牌声明（尚无 azp；生产提案前必须有）
│   │   ├── token.go              签/验能力令牌
│   │   ├── artifact.go           制品签验、内容地址
│   │   └── release.go            发布类型状态机 draft→…→retired
│   │
│   ├── store/                    账本
│   │   ├── store.go              连接池、goose 自迁移
│   │   ├── migrations/           三本账、研判聚类、最小 Thread / Turn 与结论记录等迁移
│   │   ├── query.sql             新查询只进这里
│   │   └── sqlc/                 生成层，尚无业务调用
│   │
│   ├── edgecore/                 数据面核（现：检测即裁决；目标：发现+策略）
│   │   ├── detector.go           同步检测器接口（现 Verdict 含 Action）
│   │   ├── rule.go               KIND_RULE 正则（现当主拦截器）
│   │   ├── engine.go             Check / Decide：命中+模式直接拦（目标要拆）
│   │   ├── release_set.go        按发布装载检测器并裁决（目标按世代）
│   │   ├── proxy.go              单模式反代
│   │   ├── release_proxy.go      发布集反代 ServeHTTP
│   │   ├── canary.go             生产 CanarySelectedUnit 按 unit_id；CanarySelected 仅演示 request_id
│   │   ├── scope.go              路径范围
│   │   ├── artifact.go           本地装载验签
│   │   └── telemetry.go          本地 NDJSON
│   │
│   ├── edgeclient/               数据面连中台
│   │   ├── client.go             Register / ListReleases / UploadEvents
│   │   └── spool.go              断网遥测落盘
│   │
│   ├── replay/replay.go          发布前回放；现只懂 rules/v1
│   ├── eventbus/                 内嵌或外置 NATS（当时写持久流未建；现行发件箱 + JetStream API 已建，第 10 节）
│   └── observability/            管理面探针等（现行见 code-map）
│
├── agents/                       Agent 运行时（不进 brain）
│   ├── runtime/loop.go           领 Turn 指令 → Generate → 工具网关 → Ack；租约轮换与 checkpoint 恢复（docs/api.md §18.10）
│   ├── modelgateway/
│   │   ├── provider.go           客户端协议适配；确定性提供者只在 `_test.go`，生产出口不在本包
│   │   └── *_test.go             确定性模型与演示正则剧本（仅测试编译）
│   ├── roles/                    岗位词表，仅 doc.go
│   ├── tools/                    工具扩展，仅 doc.go
│   └── skills/                   技能制品说明，无实现
│
├── proto/yufeng/                 线上字段与服务（生成物在 proto/gen/）
│   ├── common/v1/                修复层级、接入、发布状态
│   ├── event/v1/                 事件账
│   ├── asset/v1/                 资产账
│   ├── artifact/v1/              制品信封 + ArtifactService
│   ├── plan/v1/                  修复计划（L1/L2/L3 动作组合）
│   ├── procedure/v1/             修复程序信封
│   ├── tool/v1/                  工具描述
│   ├── auth/v1/                  登录会话
│   ├── user/v1/                  用户管理
│   ├── grant/v1/                 授予（当时未装配；现行 GrantService 已装配，code-map）
│   ├── registry/v1/              单元注册心跳
│   ├── telemetry/v1/             事件上行
│   ├── govern/v1/                治理管道
│   ├── console/v1/               看板事件
│   ├── audit/v1/                 审计链
│   ├── health/v1/                探针
│   ├── agent/v1/                 Agent 控制面
│   ├── run/v1/                   执行实例
│   ├── worker/v1/                工作项
│   ├── toolgateway/v1/           工具调用
│   ├── session/v1/               人机会话
│   └── command/v1/               host 指令
│
├── procedures/steps.schema.json  程序步骤体
├── components/                   中台 worker 预留
│   ├── intel/                    情报摄取，仅 doc.go
│   └── eval/                     评测，仅 doc.go
├── bpf/                          内核侧预留，无对象
├── console/                      控制台前端；交付由 brain 托管 /app
├── deploy/                       compose.yaml、Dockerfile
└── scripts/
    ├── up.sh                     全链拉起
    └── demo-repair-loop.sh       正则修复循环测试（演示）
```

尚无目录、第 4 节要求新建的：

```
.
├── cmd/yufeng-run/               短命执行进程
├── lib/kernel/signer.go          Signer 接口（生产不读私钥文件）
├── lib/edgecore/
│   ├── inspect_profile.go        HttpInspectionProfile / CanonicalRequestView
│   ├── coverage.go               InspectionCoverage
│   ├── policy.go                 检测键策略匹配
│   ├── shape.go                  形状语言编译与匹配
│   ├── generation.go             activeGeneration 原子替换
│   └── external_authorization.go 外部授权壳
└── proto/yufeng/…                PolicyCandidateV1、DetectionV1、AssetGenerationV1、ProposalIntent
```

走读：

- 请求：`cmd/yufeng-edge` → 入口壳 → 规范化视图 → `Inspector.Inspect` 只出发现与覆盖度 → `Gate` 按世代内检测键策略、演示旁路或形状规则裁决 → 403 或转发 → `brain.go` 上传。
- 制品一生：`ProposalIntent` → 服务端编译 → `replay` → `kernel/release` → 编入世代 → `scheduler` → 边缘原子替换 `activeGeneration`。
- 研判：ModelSide 告警在事件入账事务内冻结不可变检查票据、聚类并创建或唤醒研判；复核样本只聚合。`EVENT_TRIAGE` → `runtime.Handle` 调持久 Generate → 工具网关提交非可信结论 → 协调器确定性编译提案；逻辑生成只接受一个响应，物理尝试逐条对账。
- 未来冷补丁：`plan/v1` → 人审 `audit` → `CreateRun` → agentd 孵化 run → `command` → host `ReportStep` → `supersedes` 退休 L1/L2。现断在 host 回执。

---

## 6. 部署

人机交付支持同机与分离两类拓扑（架构决策记录 036）：Brain 可以独立部署；Edge 与 ModelSide 由技术人员以原生进程或 Docker Compose 人工安装，也可以和 Brain 同置于一台服务器。

企业试点在客户已有入口与真实应用之间接入：客户入口保有业务 TLS 私钥并把解密后 HTTP 交给御锋；御锋反向代理到客户应用。部署前必须证明入口覆盖全部试点域名、可交付解密 HTTP，且入口到御锋网段允许该明文段；任一条不成立则不进入本试点实施。

| 角色 | 组成 |
|---|---|
| 控制面 | `yufeng-brain` + PostgreSQL；goose 自迁移；托管 `/app`；模型出口与引导服务 |
| 签发 | 独立 `yfctl signer` 套接字；brain 不挂私钥文件 |
| 编排 Agent | 同机 `yufeng-jarvis`，只连 brain（双向 TLS），不持模型密钥 |
| 数据面 | 技术人员手动安装、启动、升级和卸载 `yufeng-edge`；Edge 主动注册并拉取已签名监听计划与资产世代。无策略攻击仍 200（DetectionOnly）；403 只发生在检测键策略 enforce 之后。Brain 与贾维斯不创建容器、进程或服务，也不反向探测 Edge |
| 异步检测 | `yufeng-modelside` 与 Edge 同机时走 Unix 域套接字；跨主机时相互传输层安全协议认证并限制在受控防御网络。Edge 发送规范流量，ModelSide 只向 Brain 上报无原文结果；不是引导聊天凭据槽（§19.2 / 第 15 节） |
| 编排文件 | `deploy/compose.yaml` `deploy/Dockerfile`；开发旁路 `scripts/up.sh`；演示 `scripts/demo-repair-loop.sh` 不得当交付门禁 |

中台宕机或 JetStream 中断：已有世代缓存的 edge 继续拦截。生产缺证书或默认口令：brain 拒绝启动。edge / host 零 cgo。L1「防火墙 / 检测 / 拦截」即本机 `yufeng-edge`，不是 nftables。

---

## 7. 扩展

1. 制品：策略、画像阈值、程序、技能、工具描述。
2. 契约进程：情报源、模型提供、修复执行只通过各自类型化契约连接中台。Edge 邻近模型是例外的本地数据面旁路：`components/modelside/` 提供独立 Python 服务包与容器镜像，接收 Edge 规范流量并只向 Brain 上报结果；不复用执行实例的 `PollWork`，不直订 NATS 消息服务器。目录预留 `components/intelligence` 与 `components/evaluation`。
3. 模块装载机待命。`bpf/` 无对象。

新设备族 = 新程序包，不改 host 原语集。原语年级才变。

---

## 8. 相对现状

| 现状 | 改为 |
|---|---|
| `KIND_RULE` 既检测又拦截，演示门槛 0 自动 enforce | 引擎 DetectionOnly；检测键策略主拦截；形状语言旁路且必须人推 |
| `EVENT_TRIAGE` 只认 allow 且无规则 | 仅未缓解 / 未映射 / 带证据漏检；聚合后入队 |
| `govern.propose` 只收任意正则 | 收提案意图；服务端编译策略或形状 |
| 调度器对所有 shadow 自动晋升 | 仅 `exact`/`route` 策略自动；其余人推 |
| canary 按 `request_id` | 按 `unit_id` 或假名租户 |
| 按条 `ListReleases` 装载 | 资产世代原子替换；失败保留上一份 |
| 单一 `ttl` | `review_at` + `hard_expires_at` |
| 入账后再发 NATS | 事务发件箱 |
| 私钥文件在 brain 进程 | `Signer` 接口；生产不读私钥文件 |
| 仅反代壳 | 四姿态壳（反代拦截 / 外部授权拦截 / 侧载只告警 / 镜像或 SPAN 只观察）；覆盖度失败语义见 `docs/api.md` 第 21 节 |
| Brain 侧按事件票据执行异步模型 | Edge 从真实已解密 HTTP 副本构造规范流量，邻近 ModelSide 推理；Brain 只收类型化无原文结果。人机交付的**聊天模型**继续走引导凭据 + Brain 出口 |
| 回放只懂 `rules/v1` | 同一规范化视图 + 引擎发现 + 检测键匹配 |
| host / CommandService 演示成功 | L2/L3 未实现路径显式拒绝 |
| 岗位未类型化 | `CreateRun.role` 裁剪后绑定技能制品；授权仍只看令牌 |

已有 enforce 中的演示规则：保留到硬过期或人工退休。第一阶段正则脚本永久标为演示，不得充当生产验收。

实现顺序（不得再把身份放到最后，也不得先扩检测算法）：

1. 冻结契约：architecture / api / design / glossary / proto；先冻结生产能力广告、世代采样策略、Brain 单点研判票据投影、规范流量与模型结果协议，再动实现。任务清单只作排期入口，不作为产品语义来源。
2. 身份与信任根：刷新协议、一次性引导、`azp`、双令牌、持久预算、授予装配、生产 TLS、`Signer` 接口。现有生产 Agent 写路径必须持续通过这组门禁；新增路径不得绕过。
3. 边缘核：规范化视图、覆盖度、DetectionOnly、检测键策略匹配、世代装载、canary 改键。
4. 中台治理：提案意图编译、调度分叉、研判原因与聚合、`review_at` / 硬过期。
5. 形状语言（人推）与外部授权壳。
6. 事务发件箱 + Brain 单点 `CheckTicket` 研判投影；Edge 与 ModelSide 双有界队列；模型结果事务入账与五场景性能门禁。
7. 画像核后接。
8. L2/L3 保持拒绝正确；业务能力另档。

---

## 9. 审查后的工作范围

第 4 节是目标语义。第 10 节是仓库现状。第 0 节是采纳表。实现按第 8 节顺序。

本档第一生产版交付：身份绑定的治理中台、确定性 L1 边缘核、Coraza 只检测、精确检测键策略、受限形状语言、类型化提案、shadow/canary/守护回滚。

本档流量拦截层**后端**不交付：画像核硬门禁、异步模型训练、运行时约束与冷补丁业务原语、把中台拆成三个部署单元、多租户、单点登录、动态装载，以及让 Python 或 C 语言互操作进入边缘进程。可选 Python 异步检测进程见架构决策记录 027。前端接入、中台托管 `/app` 与初次配置引导已经通过企业试点机器验收；客户现场仍须完成真实网络参数和变更责任记录。

---

## 10. 纸面 / 已跑通 / 未做

| 项 | 状态 |
|---|---|
| 中台 Connect 骨架、三本账、治理状态机、回放（仅规则） | 已跑通 |
| 反向代理 + `KIND_RULE` 按发布模式拦截 | 已跑通（与目标语义相反） |
| 漏拦 → 贾维斯写正则 → 演示门槛 0 自动 enforce → 403 | 已跑通（演示；不是生产语义） |
| 会话不得带 `govern.*` | 已跑通 |
| Coraza DetectionOnly + 检测键策略（引擎命中不 403） | 已跑通。活路径是 `Inspector` / `Gate` |
| 研判原因、聚合叫醒、覆盖度、规范化视图 | 已跑通（覆盖度五态与规范视图已进活路径；解析差异语料 12 条） |
| 资产世代原子下发 | 已落地：`ListGenerations` 按 `since_seq` 逐代追赶，单次响应受 `max_bytes` 约束，`has_more` 时用本页最后序号续拉；单请求 `InspectThenGate` 共用一份快照。`ListReleases` 快照可附带当前世代信封，边缘不得只装最新信封跳序。损坏成员使整代失败并保留上一代。采样策略成为签名世代成员仍待协议与实现对齐，当前 1% 仅为回退 |
| 形状语言编译与匹配 | 已落地受限正向形状**源结构**装载与闸门匹配（proto 只定义源，不是独立编译产物）。生产对人侧 `ProposeArtifact` 与工具网关硬拒无 `intent` 的 `KIND_RULE` / `rules/v1` |
| 反代 + 外部授权壳、独立管理端口 | 四姿态壳与覆盖度→状态码已跑通。边缘只有取得已验证监听计划才绑定业务服务器；就绪探针只走独立管理端口 |
| 人工 Edge 部署与就绪回执 | 管理员提交类型化部署规格；Brain 确定性签发监听计划、基线世代和模型档案。技术人员人工安装 Edge；Brain 只按注册与心跳中的已装载版本判断就绪，不发管理探针 |
| 来源身份与假名 | 签名监听计划携带可信代理 CIDR；边缘按直接对端与 `X-Forwarded-For` 解析来源，供 Coraza 使用并在上行前编成部署作用域 HMAC 假名 |
| 单元生产能力 | 注册与心跳持久化关键事件、普通样本、票据特征、投影版本、姿态、传感、模块协议能力与容量；不兼容世代不下发，模块目录仅在真实 Edge 能力覆盖要求时激活，资产详情只读展示能力与生产健康，广告不产生授权 |
| Edge 生命周期边界 | Docker 套接字与进程管理权限不进入 Brain、贾维斯或 Agent 工具；原生与容器两种 Edge 交付都由技术人员显式安装、启动、升级和卸载 |
| `Signer` 套接字签名、授予装配 | 已跑通（生产套接字签名；文件钥须 `-dev-insecure`）。生产中台持治理池和流量池两个数据库连接池；`yufeng_traffic` 为 `LOGIN NOINHERIT` 受限角色，只能对 `traffic.traffic_windows`、`traffic.traffic_window_receipts`、`traffic.review_candidates`、`traffic.review_case_outbox` 执行 `SELECT` / `INSERT`。读取 `users` 或写入、修改 `releases`、`grants`、`audit_entries` 必须返回 PostgreSQL `42501`（`insufficient_privilege`）；缺独立数据源、同角色或授权越界时生产中台拒绝启动。两个数据库连接池仍在同一 `yufeng-brain` 进程和地址空间内，因此只能宣称数据库角色隔离已经落地，不能宣称整个中台信任计算基已经拆分或收缩 |
| 双令牌 `azp`、刷新协议、生产 TLS | 套接字签名与生产 TLS 已跑通。边缘访问令牌按 `AccessTokenTTL` 半寿刷新；brain 重启作废访问令牌后，认证失败会串行轮换刷新令牌并只重试原调用一次；完整刷新协议与双令牌 `azp` 校验仍按第 18 节约束 |
| 模型结果事务入账（不训练） | Edge 与 ModelSide 使用两个独立有界队列；Brain 对批量结果复核签名档案并在同一事务建立事件、推理、票据、案件与发件箱；告警唤醒研判，复核样本只聚合 |
| 画像核纯 Go | 未做（后接，未装载） |
| `Inspector` / `Gate` 拆分、四姿态壳、单元监听计划 | Inspect/Gate 活路径已跑通。监听计划按单元独立拉取、验签、缓存并驱动业务绑定；监听地址变化通过进程退出和容器重启重绑，允许秒级中断 |
| Python 异步检测执行实例 | `components/modelside` 提供独立 `yufeng-modelside` Python 服务包与镜像，按签名模型档案加载权重、批量推理与有界采样；不是平台 Go 二进制，支持与 Edge 同一 Compose 或独立部署 |
| `yufeng-run`、agentd 真孵化、host 拒绝未实现原语 | 已跑通（未知原语不得 `SUCCEEDED`） |
| AgentThread / Turn / Step / Item、`Generate`、checkpoint 与恢复 | 会话、研判和认知型 run 创建不可变 Turn；输入序列与 Item 序列分离；Yield 释放租约并持久化 checkpoint；生产贾维斯只调 Generate，Generation 单一接受且 Attempt 全量对账。run 多维预算、可恢复补偿事务与权威审计均已持久化；这些能力本身仍不等于全能座架 |
| 技能激活、模型上下文协议连接器桥、审批、委派和进程沙箱 | 签名技能激活、渐进披露、工具目录和注册实现绑定已建；模型上下文协议桥、审批与委派交互和真实进程沙箱只有目标边界，没有生产实现或发布证据。技能不补权，模型上下文协议只可经中台代理，运行时约束与冷补丁开放前必须有真实进程沙箱 |
| `Login`/`GetMe.access`、列表按绑定裁剪、工具网关硬拒正则 | 已跑通 |
| 初次配置引导 / brain 托管 `/app` / compose 起贾维斯 | 托管与 compose 已跑通；引导六步页（含设置防御资产） / 授予 / 提案意图 / 会话已建 |
| 贾维斯只做安全研判与治理建议，Edge 人工部署 | Brain 与 Agent 工具均无 Docker、进程管理、Edge 安装或探测权限；Edge 主动注册、拉取和回执制品 |
| 控制台只接真实中台 | 开发、预览与交付运行时均只装配 `ConnectClient`；无模拟业务开关、公开设计回廊、固定业务指标、演示账户或前端内置业务状态机。组件测试夹具不进入运行时依赖图，服务语义由 Brain 的 PostgreSQL 集成测试覆盖 |

---

## 11. 已拍板的设计问题

| 问题 | 决定 |
|---|---|
| 核心规则集加载范围与偏执级别 | 已冻结为开放全球应用安全项目核心规则集 4.25.0、偏执级别 1，并与 `sentry-docker` `wafcore/engine.go` 使用同一组包含规则；摘要见 `procedures/http-inspection-baseline/core-rule-set-manifest.json` |
| 参数画像检测器是否构成当前硬门禁 | **否**。未装载时引擎列表不得出现它，事件不得伪造命中 |
| 外部模型未返回攻击类别时，工作进程能否本地分类 | **否**。只能使用已签名的 `TaxonomyMapperV1` |
| `unmapped` 表示已检出还是漏检 | 表示 `DETECTED_UNMAPPED`，不能按自动治理五类生成策略 |
| 多条拒绝策略重叠 | 短路，任一命中即拦截 |
| 检测键策略与形状规则同时命中 | 先检测键策略后形状规则，两者都可返回 403 |
| 智能代理研判经协调器形成提案、排除集为空时能否自动全量生效 | 仅在范围风险属于精确或路由、证据来自核心规则集映射或人工或回放，且回放覆盖度合格时允许。未映射、模型、形状和宽范围必须由另一用户推进；单单元资产禁止进入金丝雀自动通道 |
| 外部授权超时 | 使用命名常量，推荐 50 毫秒，测试引用同一标识符 |

---

## 12. 威胁与信任边界

| 边界 | 规则 |
|---|---|
| 签名私钥 | 生产由 `Signer` / 密钥管理服务持有；brain 不读私钥文件；签名端不接自然语言或任意 JSON |
| 数据路径 | 只有 edge；贾维斯 / 模型 / brain 都不转发业务请求 |
| Agent 网络 | 贾维斯与 agentd 只连 brain；run 只连 agentd 本地监督代理且没有网络凭证。不连边缘、PostgreSQL、NATS、公网模型、被保护资产 |
| ModelSide 网络 | 同机只接 Edge Unix 域套接字并主动上报 Brain；跨主机两段都相互传输层安全协议认证且限制在受控防御网络。不获 NATS、数据库、Agent 或 Gate 权限 |
| 命令来源 | 只从中台长轮询响应体来。用户原文是已签发指令的附件，不是命令 |
| 会话令牌 | 无 `govern.*`。回写走 `session.reply` |
| 研判令牌 | 只含取证只读工具与 `triage.complete`；无 `govern.*` / `promote_*`。Agent 不填写可信检测键、证据或 scope |
| 能力令牌与租约 | 工具调用验访问令牌 `sub` = 能力令牌 `azp`；当前 `lease_epoch` 隔离旧所有者；`jti` 只标令牌实例，持久 `budget_id` 承载跨续租预算 |
| 模型与工具恢复 | ModelGeneration / ModelAttempt 分离；工具有 intent / effect / settlement；外部结果未知不自动重放，不宣称物理调用恰好一次 |
| 发布 | 验签 + 世代原子替换；坏条目隔离；enforce 依赖失败用上一份已验证可用世代 |
| 原文证据 | 账本默认无原文；需要复核时走边缘本地、加密、短生命周期的证据库。Agent 与外部模型永不读原文 |
| L3 | 无人审记录不得签发含写原语的令牌 |

主要攻击与对策：中间人 → 生产 TLS，缺证书不启动。令牌失窃后换进程重放 → 双令牌 + `azp` + `lease_epoch` + 持久预算账户 + 业务调用标识。提示词注入 → 无外界套接字 + 命令只从中台来 + Agent 只交 TriageDecision + 服务端确定性编译制品。提案人自喂门槛 → 自动晋升排除其单元。解析差异绕过 → 同一规范化视图 + 覆盖度。class 级误封 → 检测键 + `scope_risk`。执行器自报成功 → 未实现原语拒绝；成功只认验证步骤。被攻陷贾维斯 → 不能跨资产、不能 promote、不能执行未授权原语。

生产提案前必须能表驱动证明：复制到另一进程因 sender binding 失败；陈旧 `lease_epoch` 无法写入；同一 `budget_id + turn_id + call_id` 并发只执行和结算一次；贾维斯 `promote` 为 `permission_denied`；跨资产研判为 `permission_denied`；无签名回滚授权时拒绝旧世代重放。

---

## 13. 与参考实现的差异

| | sentry-docker | safeshield | 御锋（目标） |
|---|---|---|---|
| 同步眼睛 | Coraza DetectionOnly | C 画像核，命中即拦 | Coraza DetectionOnly；画像纯 Go 后接，只出发现 |
| 闸门 | 策略候选 | 画像本身 + 管理面 IP 封禁 | 检测键策略为主；仅带证据漏检才形状语言 |
| 机器学习 | Redis 流 + Keras，路外 | Python 离线训练出 C | Edge 邻近 ModelSide 双有界队列，路外；不训练；不能直接补同步策略 |
| 晋升 | 两段人工审批 | 无治理管道 | 精确策略门槛自动；形状与宽范围另一人 promote |
| 入口 | 仅 Envoy 外部授权，50ms 失败即开 | 仅反代 | **四姿态**（反代拦截 / 外部授权拦截 / 侧载只告警 / 镜像或 SPAN 只观察）；失败语义按覆盖度拆（超体 413/403、畸形 400、观察壳不 503），见 `docs/api.md` 第 21 节 |
| Agent | 控制面编排，不上路 | 无 | 同一认知座架；提交 TriageDecision，由确定性协调器编译可信意图；不上数据路径 |
| 运行时 | Python + Go + Redis | Go + cgo + Python 训练 | 平台 Go；`yufeng-edge` / `yufeng-host` 零 cgo；无 Redis；独立 Python ModelSide 服务不进平台二进制 |

借：检测与拦截分开、路外模型、签过名的策略、回放同引擎。不借：Python/cgo 进平台二进制（`yufeng-edge` / `yufeng-host` / `yufeng-brain` / `yufeng-jarvis`）、两段审批产品、Redis、管理面改黑名单、按攻击类开闸、模型当闸。Edge 邻近 Python ModelSide 见架构决策记录 036，不改变「平台语言 Go」。

---

## 14. 验收口径（数字进常量后不得另写一份）

语义已定。第 99 百分位额外延迟、吞吐、内存/磁盘与旁路/证据环/打分采样等预算已写入 [`architecture.md`](architecture.md) §13 与 `lib/kernel/limits.go`；测试必须引用同名常量，不得另写一份数字。

治理与身份：

- 复制访问令牌到另一进程：sender binding 失败。
- 陈旧 `lease_epoch`：任何推进、Generate 或 InvokeTool 均失败关闭。
- 同一 `budget_id + turn_id + call_id` 并发：只允许一次工具执行与预算结算；令牌 `jti` 轮换不重置预算。
- 贾维斯 `promote`：`permission_denied`。
- Agent `triage.complete` 跨资产：`permission_denied`；夹带可信检测键或证据字段：`invalid_argument`。
- 旧世代重放且无签名回滚授权：拒绝。
- brain 或 JetStream 中断：edge 继续使用上一份已验证可用世代。

WAF 与解析（表驱动，反代与外部授权同一组）：

- `Content-Length` 与 `Transfer-Encoding` 冲突；重复 `Content-Length`；重复查询键；多轮 URL 编码；编码斜杠与反斜杠；JSON 重复键；畸形 multipart；内容类型与体不一致；压缩炸弹；64 KiB 截断边界；HTTP/2 适配差异。

L1 主路径：

- 引擎能认出的攻击、无策略：200，事件为 `DETECTED_UNMITIGATED`，聚合后一条 `EVENT_TRIAGE`。
- 精确策略 enforce 后：403，轨迹指向该策略与检测键。
- 普通无发现：200，**不**入队。
- 截断导致 body 非 `FULL`：不得把「无发现」写成漏检；负向谓词不得 block。
- 形状规则：停在 shadow，另一用户 promote 后才 403。
- canary：同一 `unit_id` 对同一 release 结果稳定；重试不得改桶。

失败（完整矩阵见 `docs/api.md` 第 21 节；观察壳禁止 503）：

- 反代 detector panic：该请求 503，进程存活。
- 外部授权超时：单次仍 200；窗内超时率熔断才 503，半开见 `ExtAuthzHalfOpenPerSec`。
- 新世代部分损坏：旧世代保持。
- 发件箱发布失败：恢复后继续投递，不丢逻辑事件。
- 模型 worker 重复消费：只一条逻辑追加记录。
- 中台时钟漂移：按允许偏差，不静默加载未来制品。

演示门槛为零与 `demo-repair-loop.sh` **不得**作为本节证据。

第 99 百分位额外延迟、吞吐、内存/磁盘上限已写入 [`architecture.md`](architecture.md) §13 与 `lib/kernel/limits.go`（p99 额外 5ms、单 edge 2000 rps、内存/缓存盘各 512 MiB）。测试必须引用同一常量。

---

## 15. 隐私与出网

原始流量不出户。生产投影 `query_redacted` 不得存原文（已冻结；账本只留参数名）。演示夹具仍可写入已脱敏字段，不得把原文当验收通过。

事件账默认只保存：路由模板、参数名、检测键、规则标识、匹配位置、长度与字符类特征、基于哈希的消息认证码假名、必要短摘要。需要人工复核原始证据时，使用边缘本地、加密、短生命周期的证据库；Agent 和外部模型永远没有读取权限。单元进程日志、指标与心跳属于运维域，不进入 CheckTicket、TriageObject 或模型上下文。

异步模型输入端点必须是同机 Unix 域套接字或受控防御网络内的相互传输层安全协议地址，禁止公网。只有 Edge 向 ModelSide 发送签名档案允许的有界规范流量；Brain、贾维斯和调查 run 不取得原文。贾维斯**聊天补全**走引导写入的 OpenAI 兼容端点，只允许 **brain 模型网关**出网（可指向公网 HTTPS，密钥不进浏览器与贾维斯）。令牌与密钥不得进审计与模型上下文。假名化跨重启稳定。正常事件按签名世代策略采样；拦截、观察、未缓解检出、未映射检出、检查不完整、检测器失败为关键事件，带发布或世代轨迹。

---

## 16. 复现现状

```
# 本地正则拦截（开发构建；与目标语义不同：规则直接 403）
make demo-init && make run
curl "localhost:18080/api/items?id=1+UNION+SELECT+pw"   # 403

# 第一阶段 Agent 正则闭环（仅演示：门槛 0 自动 enforce）
export YUFENG_TEST_DSN='postgres://…/yufeng_test?sslmode=disable'
./scripts/demo-repair-loop.sh
# 或 go test ./lib/brain -run TestDemoRepairLoopAllowsThenBlocksAttack

# 正式中台（需容器运行时；不会发布演示规则）
make up

# 生产活路径（无策略 200 + DETECTED_UNMITIGATED 入队一条）
./scripts/production-live.sh
```

**仓库现状**：`deploy/compose.yaml` 只提供控制面基础服务；`deploy/compose.edge-modelside.yaml` 由技术人员显式启动 `edge` 与 `modelside`，可与控制面 Compose 合并运行或改连远端 Brain。`deploy/edge.Dockerfile` 单独交付 `yufeng-edge` 容器镜像，发布包同时交付原生 Go 二进制；`components/modelside` 另交付 Python 服务包与镜像。Brain、贾维斯和 agentd 不挂 Docker 套接字，也不创建 Edge。人机交付基座见第 6 节与架构决策记录 036。正式中台不注册 `--demo-triage`，开发正则闭环只由带 `yufeng_dev` 构建标签的目标承载且不参与发行工作流。`scripts/production-live.sh` 只验证局部链路；企业试点软件发布必须同时具备真实入口、故障、安全、容量、备份恢复和远端持续集成证据，当前结果见 [`delivery-evidence.md`](delivery-evidence.md)。
