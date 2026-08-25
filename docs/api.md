# API 文档 · 接口总集（v1）

> 本文负责网络应用程序编程接口（API）的行为与状态语义；`proto/` 负责可编译的线上字段与编码。
> 字段级表格使用 proto 字段名（snake_case），线上 JSON 使用 Connect/protojson 规则（lowerCamelCase），
> 两者对应关系见 §17.6；本文与 proto 冲突时必须阻止合入并人工对齐，不允许任一侧自动覆盖另一侧。
> 术语见 [glossary.md](glossary.md)，架构权威见 [architecture.md](architecture.md)。仓库目标版本只读取根目录 [`VERSION`](../VERSION)，已发布状态只读取 [GitHub Releases](https://github.com/ZionOVO/YuFeng/releases/latest)；只有同名非草稿 Release 的精确提交证据资产复核通过，才可宣称对应单站点企业试点版本的软件发布与机器验收闭环。客户现场仍须记录真实上游、精确代理网段、证书核对、切换与回退责任人，才能宣布现场交付完成。
> 接口设计参考实现：`../sentry-docker`（治理管道 / 回放 / 事件入库）与 `../safeshield`（免重启运行时管理），对照表见 §15。

---

## 0. 总则

### 0.1 接口边界

| 类别 | 管辖处 | 例子 |
|---|---|---|
| 网络 API | 本文档管行为与状态语义；proto service 管线上字段与编码 | Register / UploadEvents / PromoteCanary |
| 进程内库接口 | Go 接口与 godoc 注释 | `Inspector`（无 `Action`）+ `Gate`（唯一持有 `Action`）、`kernel.SignArtifact`、回放组件。`Detector.Evaluate` 返回 `Action` 不是接口边界正例 |
| 数据契约 | proto 消息 + 制品 JSON Schema | Artifact / Event / ReleaseTrace / 修复程序信封与步骤体 |

### 0.2 传输与命名

- **协议**：HTTP 上的 Connect 协议。路由 `POST /{proto包名.服务名}/{方法名}`，例如 `POST /yufeng.telemetry.v1.TelemetryService/UploadEvents`。所有认证域必须走 HTTPS/TLS。生产缺证书或未显式 `-dev-insecure` 拒绝启动。生产 Agent 写路径（工具网关与领指令）必须双向 TLS，私有网络不能替代。
- **必需头**：Connect 请求必须携带 `Connect-Protocol-Version: 1`；JSON 请求再带 `Content-Type: application/json`。
- **两种编码等价**：`application/json`（protojson，字段名 lowerCamelCase）与 `application/proto`（protobuf 二进制）。本文字段表给 proto 字段名，JSON 名按 protojson 转换：`asset_id → assetId`、`page_size → pageSize`。
- **错误响应体**（Connect 标准 JSON）：`{"code": "not_found", "message": "...", "details": [...]}`。前端读 `code` 字段，不要只依赖 HTTP 状态码。
- **HTTP 探针**（不走 Connect）：御锋管理面使用独立监听端口，默认 `:19090` 上的 `GET /livez`、`GET /readyz`、`GET /metrics`、`GET /version`。业务 `{BASE}` 上不再画这些路径，业务应用自己的 `/admin`、`/health` 不隐式跳过策略。

### 0.3 两个核心标识：release_id 与 artifact_id

- **release_id**：发布生命周期主键。`ProposeArtifact` 时由中台生成，直到退休保持不变。治理 API、统计、时间线、取代关系、边缘缓存都使用它。格式：不透明字符串，长度 ≤ 64，客户端不解析。
- **artifact_id**：签名制品信封的内容地址。门禁通过后，服务端把回放报告写入制品，再按下式计算：

```text
canonical = deterministic_proto_marshal(Artifact{ id="", signature=nil, 其余字段不变 })
artifact_id = "sha256:" + hex(sha256(canonical))
signature = Ed25519.Sign(canonical)
```

  `artifact_id` 覆盖 kind、payload、payload_schema、scope、ttl、supersedes、evidence_refs、replay_report、created_at、created_by。同 payload 不同 scope/ttl 的两次发布，artifact_id 不同。
- **校验**：边缘对每个制品无条件验签，并校验 `artifact_id` 与信封一致；验签失败或 id 不符的条目隔离跳过，不整批毒化拉取。

### 0.4 单元与资产模型

- `units` 是运行 `yufeng-edge` / `yufeng-host` 的进程身份；`assets` 是被保护对象；`unit_assets` 是绑定关系。
- `Register` 用持久化的 `unit_id` 注册。该 `unit_id` 尚不存在时，凭部署级引导令牌创建；已存在时必须持该单元当前会话令牌，禁止用引导令牌回盖。不得把**已属于其他单元或已手工登记**的 `asset.id` upsert 到本单元；`asset.id` 为空时由服务端分配新资产。v1 行为先限制为一个单元绑定一个主资产，schema 支持未来多资产。
- `ListReleases` / `UploadEvents` / `PollCommands`：令牌中的单元必须等于请求中的 `unit_id`；事件或条目里的 `asset_id` 必须已绑定到该单元，否则 `permission_denied`。
- 旁路资产（没有单元代注册的设备）由 `AssetService.CreateAsset` 手工登记，再经 `AttachUnit` 交给某个 edge 单元保护；`AttachUnit` 同样检查调用者 Bindings。

### 0.5 认证

| 域 | 调用方 | v1 凭证 | 行为 |
|---|---|---|---|
| 单元域 | yufeng-edge / yufeng-host | 首次 `Register` 凭部署级引导令牌换短期访问令牌 + 可轮换刷新令牌；日常 `Refresh` 续期；已注册单元禁止用引导令牌覆盖刷新令牌 | brain 重启只作废访问令牌；edge 用刷新令牌续期，不得重跑公开注册。无令牌 / 旧访问令牌 → `unauthenticated`；旧刷新令牌或重放已轮换刷新令牌 → `unauthenticated`；裸引导令牌覆盖已有单元 → `permission_denied` |
| 操作域 | 控制台 / yfctl | 用户名 + 密码经 `AuthService.Login` 换用户会话令牌（只证明是谁）；写 RPC 还要授予展开的 Tools × Bindings | 会话过期回登录。`USER_ROLE_*` 只决定默认 Tools 模板；空 Bindings 拒绝一切写，**唯一例外**是引导未完成时 §19.5 白名单。不另建对象级权限产品 |
| Agent 域 | yufeng-jarvis / 未来其它主动领取 Agent 指令的认知进程 | `RegisterAgent` 的一次性 bootstrap_token → 可轮换 refresh_token → `aud=agent` 短期 access_token；具体 instruction 再携带 capability_token | 引导令牌绑定精确 `agent_id + 公钥/客户端证书指纹`。Agent 身份只能领取自己的指令；Role 只是默认模板，实际能力由服务端签发的 Tools × Bindings × budget 决定 |
| Worker 工作负载域 | yufeng-agentd | `RegisterWorkerIdentity` 的一次性 bootstrap_token → 可轮换 refresh_token → `aud=worker` 短期 access_token；固定 `worker_kind` | `RUN_SUPERVISOR` 只有代持当前 run capability_token 才能代表 run 推进 Turn、生成或调工具。`yufeng-run` 无 brain 凭证，只连 agentd 本地监督代理；`yufeng-modelside` 使用独立的模型结果上报身份与 §21.5 协议，不是 Agent 或 run worker |
| ModelSide 工作负载域 | yufeng-modelside | 运维签发并绑定精确 `modelside_id + unit_id + asset_id + 客户端证书指纹` 的短期 `aud=modelside` 凭证；同机开发可用显式不安全开关 | 只允许 `ModelResultService.UploadResults`，不能调用 Agent、Worker、ToolGateway、ModelGateway、单元或用户接口；生产必须同时校验相互传输层安全协议客户端证书 |
| 公开域 | 探针、登录页、静态资源、可选账户自注册 | 无 | `/livez` `/readyz` `/metrics` `/version`、预留的 `/app` 静态资源、`AuthService.Login`；只有 `AuthService.Register` 是否公开由配置决定 |

- **v1 有用户账户；三角色是默认模板，不是上帝开关。** `USER_ROLE_VIEWER` 无写工具；`USER_ROLE_OPERATOR` 默认可 `govern.propose`、`govern.gate`、`govern.start_shadow`、绑定范围内 `run.create`，默认**无** `govern.promote_enforce`、无用户管理、**无**资产增删改；`USER_ROLE_ADMIN` 可管理用户、写授予、管理签名目录，并独占 `asset.create` / `asset.update` / `asset.delete`（以及默认的 `asset.attach` / `asset.detach`）。资产增删改查和目录发布的写侧**另加角色硬门**：非 `USER_ROLE_ADMIN` 即使持有对应工具也拒绝。自身治理写同样要求非空 Bindings。完成定义：不存在「仅凭 `USER_ROLE_OPERATOR`、Bindings 为空、就能调用任意治理写 RPC」的路径。同一用户对同一 `release_id` 不得既 propose 又 promote_enforce。超过资产 `max_auto_tier` 的动作签不出来。
- **初始管理员不靠公开注册产生**：服务端配置项 `auth.bootstrap_admin_username` / `auth.bootstrap_admin_password` 在首次启动时创建 `USER_ROLE_ADMIN` 账户。compose 用环境变量 `YUFENG_ADMIN_USER` / `YUFENG_ADMIN_PASS` **原样**注入这两项（缺省用户名 `admin` 允许；口令不得空、不得为 `admin` / `password` / `changeme`，否则 brain 拒绝启动，compose 启动机检失败，不是退出码 2）。首次启动没有资产，不得虚构 `bootstrap`。引导未完成时，该管理员只许调用 §19.5 白名单。`CompleteOnboarding` 写入可为空 Bindings 的系统授予；其 `asset.create` 是管理员全局工具，因此零资产状态也能在主控制台创建第一项资产，成功后自动把该资产加入创建者范围。生产环境默认密码拒绝启动。
- **自注册默认关闭**：`AuthService.Register` 受 `auth.allow_self_registration` 控制，默认 `false`；开启后注册用户固定为 `USER_ROLE_VIEWER` 且 Bindings 为空，由 `USER_ROLE_ADMIN` 提升角色并补授予。
- 控制台把令牌存 `sessionStorage`，收到 `unauthenticated` 清除并回登录页；登录接口返回的用户信息用于渲染当前身份和角色。
- 单元凭证：`RegistryService.Register` 只做首次身份交换。成功响应同时返回 `token`（访问令牌，默认 30 分钟）与 `refresh_token`（默认 30 天，服务端只存哈希）。日常恢复调用 `RegistryService.Refresh`：校验刷新令牌哈希、立即轮换、签发新访问令牌。brain 重启后未到期的刷新令牌仍可续期；访问令牌全部失效。已认证调用收到 `unauthenticated` 时，单元客户端串行轮换当前刷新令牌并只重试原调用一次；并发调用发现令牌已被同伴更新时直接使用新令牌，不重复轮换。刷新令牌也失效时失败关闭并进入运维恢复，不得回退引导令牌。
- 签名公钥**不通过 Register 下发**：edge 启动期从本地配置加载公钥；`pubkey_hint` 是本地公钥指纹，服务端发现与自身签名密钥不匹配时拒绝注册（错误码 §13 的 `signing_key_mismatch`），避免误配。

### 0.6 幂等、重试、分页、时钟、限流

- **注册**：`unit_id` 由单元持久化生成；重复注册仅在持该单元会话令牌时更新本单元自己的主资产字段，不得改写他人资产绑定。
- **遥测**：按事件 id 幂等去重；同一批响应中 `accepted + deduped + rejected == len(events)`。
- **治理写操作**：GovernService 的全部写 RPC（含 `ProposeArtifact`）必须携带 `Idempotency-Key` 头（≤128 字符）。同 release 同键重复请求返回首次结果；不同 release 可复用同键。
- **幂等 `pending` 恢复**：键已占用且状态为 `pending` 时，未超过 `IdempotencyPendingTTL`（architecture §13，120s；必须大于 `ChatCompleteTimeout`）返回 `aborted`（in flight）。超过该 TTL 后，**同键同摘要**允许删除过期 `pending` 并重新执行（崩溃恢复是至少一次，不是精确一次）；同键不同摘要仍 `failed_precondition`，不因过期而改写请求含义。
- **重试**：单元侧指数退避 + 抖动；遥测断网本地落盘缓冲，恢复补传。
- **分页**：控制台列表使用 `page_size`（默认 50，上限 200）与不透明 `page_token`（base64）。前端只回传不解析。
- **制品拉取**：`ListReleases` 与 `ListGenerations` 使用 `max_bytes` 字节预算（默认 4 MiB，上限 16 MiB），见 §3。
- **时钟**：注册响应带 `server_time`；事件时间戳双写（产生方 `occurred_at` + 中台入库时间）。
- **限流**：遥测 ≤ 100 事件/批；心跳之外所有单元 RPC 合计 ≤ 10 QPS/单元（超出返回 `resource_exhausted`）。治理统计走心跳计数器，不走事件全量上报。

---

## 1. 服务总览

"五种契约"是业务交互类别，不是服务或远程过程调用数量：前四种覆盖中台与两类边缘的注册、制品、遥测和指令交互，第五种由 `SessionService` 实现人—控制台—贾维斯会话。`AgentControlService`、`AgentInteractionService`、`AgentProfileService`、`RunService`、`WorkerService` 与 `ToolGatewayService` 组成其余中台工作控制面服务族，不计入五种边缘契约。Edge 与邻近 ModelSide 的规范流量入口，以及 ModelSide 向 Brain 的批量结果上报，是同一数据面内部的异步检测链，不改变五种平台契约的治理边界。

| # | 服务（proto 包） | 面向 | L1 必需 | RPC |
|---|---|---|---|---|
| 1 | `yufeng.registry.v1.RegistryService` | 单元 | ✅ | Register, Refresh, Heartbeat |
| 2 | `yufeng.artifact.v1.ArtifactService` | 单元 | ✅ | ListReleases, ListGenerations, ListUnitListenPlans |
| 3 | `yufeng.telemetry.v1.TelemetryService` | 单元 | ✅ | UploadEvents |
| 4 | `yufeng.govern.v1.GovernService` | 操作域 | ✅ | ProposeArtifact, GateArtifact, StartShadow, PromoteCanary, PromoteEnforce, RollbackRelease, RetireRelease, DenyFeedback, GetRelease, ListReleases, GetReleaseTimeline, GetReleaseStats；目录制品的 ProposeCatalogArtifact, SignCatalogArtifact, ActivateCatalogArtifact, RevokeCatalogArtifact |
| 5 | `yufeng.auth.v1.AuthService` | 公开/操作域 | ✅ | Login, Logout, GetMe, ChangePassword, Register, GetLoginConfig |
| 6 | `yufeng.user.v1.UserService` | 操作域（持 `user.admin`） | ✅ | CreateUser, ListUsers, GetUser, UpdateUser, DeleteUser, AdminResetPassword |
| 6a | `yufeng.grant.v1.GrantService` | 操作域（持 `grant.write` 或查自己） | 定契约 | ListGrants, PutGrant, RevokeGrant |
| 7 | 回放组件（进程内库接口，非网络） | brain 内部 | ✅ | `GateArtifact` 直接调用 `replay.Run`；外置算力阶段再升格为网络服务 |
| 8 | `yufeng.asset.v1.AssetService` | 操作域 | ✅ | CreateAsset, UpdateAsset, DeleteAsset, ListAssets, GetAsset, AttachUnit, DetachUnit, PutEdgeEnrollment, GetEdgeEnrollment, GetTrafficReviewPolicy, UpdateTrafficReviewPolicy, GetModelIngressWindow, UpdateModelIngressWindow |
| 9 | `yufeng.console.v1.ConsoleService` | 控制台 | ✅ | Dashboard, ListEvents, GetEvent |
| 9a | `yufeng.onboarding.v1.OnboardingService` | 控制台（引导） | 人机交付必交 | GetOnboarding, PutModelConfig, TestModelConnectivity, CompleteOnboarding；PutDeploymentSpecification 只保留线缆并固定返回 `unimplemented`（§19） |
| 9b | `yufeng.model.v1.ModelGatewayService` | Agent 生成；管理员读改槽 | 人机交付必交 + 座架目标契约 | CompleteChat（迁移/探测）；Generate（§18.10）；GetModelGateway / UpdateModelGateway / ProbeModelGateway（§19.4）；生产出口，不是 `agents/modelgateway` |
| 10 | `yufeng.audit.v1.AuditService` | 控制台 | ✅ | ListAuditEntries, VerifyChain |
| 11 | `yufeng.health.v1.HealthService` | 所有人 | ✅ | Livez, Readyz, Version |
| 12 | `yufeng.agent.v1.AgentControlService` | 贾维斯；`RUN_SUPERVISOR` 只可代表当前 run 调 Turn 推进接口 | 定契约 | RegisterAgent, RefreshAccessToken, PollInstructions, AckInstruction, Heartbeat；OpenTurn, GetTurn, ListTurnItems, ExtendInstructionLease, YieldTurn, CompleteTurn, RequestUserInput（§18.1） |
| 12a | `yufeng.agent.v1.AgentInteractionService` | 登录用户 / 控制台 | 座架目标契约 | GetThread, GetTurn, ListTurnItems, SteerTurn, AppendFollowUp, AnswerUserInput, CancelTurn, GetApproval, DecideApproval（§18.5.1） |
| 12b | `yufeng.agent.v1.AgentProfileService` | 登录用户 / 控制台 | 流量审查管理 | ListAgentProfiles, CreateAgentProfile, UpdateAgentProfile, DeleteAgentProfile, BatchUpdateAgentProfiles（§18.5.3） |
| 13 | `yufeng.run.v1.RunService` | 控制台 / 中台协调器 | 定契约 | CreateRun, GetRun, ListRuns, CancelRun, WatchRun, ListRunEvents；Agent 委派只走 §18.10 的 `run.*` 工具 |
| 14 | `yufeng.worker.v1.WorkerService` | agentd / 管理员 | 已实现 | 外部注册、加密激活与证书轮换、工作负载身份和档案；run 车道 PollWork / ExtendLease / ReportProgress / CompleteWork / FailWork（§18.3）；旧分析车道保留编码但不再接受新工作 |
| 15 | `yufeng.toolgateway.v1.ToolGatewayService` | 贾维斯；agentd 代表 run | 定契约 | ListTools, DescribeTool, InvokeTool, ListSkills, LoadSkill |
| 16 | `yufeng.session.v1.SessionService` | 人 ↔ 贾维斯 | 定契约 | CreateSession, SendMessage, PollMessages, ListMessages |
| 17 | `yufeng.command.v1.CommandService` | 执行单元 | ✅ | PollCommands, ReportStep |
| 18 | `yufeng.console.v1`（扩展 service） | 控制台 | 预留 | 事故 / 任务 / 执行实例时间线（L0/L3） |
| 19 | `yufeng.modelside.v1.ModelSideIngressService` | Edge → 邻近 ModelSide | 异步检测必交 | SubmitTraffic（§21.5）；同机 Unix 域套接字，跨主机相互传输层安全协议认证 |
| 20 | `yufeng.modelside.v1.ModelResultService` | ModelSide → Brain | 异步检测必交 | UploadResults（§21.5）；只接收类型化结果，不接收原始流量 |

### 1.1 URL 总目录（前端与调用方直接照抄）

约定：所有 Connect RPC 都是 `POST`；路径以 `{BASE}` 为前缀。同源部署时 `{BASE}` 为空字符串，实际请求如 `POST /yufeng.console.v1.ConsoleService/Dashboard`；跨域部署时填 `https://brain.example.com`。回放组件是进程内调用，没有 URL。

**前端/操作域必需（Bearer 操作令牌）**

| 方法 | URL 路径 | 对应章节 |
|---|---|---|
| POST | `{BASE}/yufeng.auth.v1.AuthService/Login` | §5.1（无认证） |
| POST | `{BASE}/yufeng.auth.v1.AuthService/Logout` | §5.2 |
| POST | `{BASE}/yufeng.auth.v1.AuthService/GetMe` | §5.3 |
| POST | `{BASE}/yufeng.auth.v1.AuthService/ChangePassword` | §5.4 |
| POST | `{BASE}/yufeng.auth.v1.AuthService/Register` | §5.5（默认关闭） |
| POST | `{BASE}/yufeng.auth.v1.AuthService/GetLoginConfig` | §5.6（无认证） |
| POST | `{BASE}/yufeng.user.v1.UserService/CreateUser` | §6 |
| POST | `{BASE}/yufeng.user.v1.UserService/ListUsers` | §6 |
| POST | `{BASE}/yufeng.user.v1.UserService/GetUser` | §6 |
| POST | `{BASE}/yufeng.user.v1.UserService/UpdateUser` | §6 |
| POST | `{BASE}/yufeng.user.v1.UserService/DeleteUser` | §6 |
| POST | `{BASE}/yufeng.user.v1.UserService/AdminResetPassword` | §6 |
| POST | `{BASE}/yufeng.grant.v1.GrantService/ListGrants` | §6.1 |
| POST | `{BASE}/yufeng.grant.v1.GrantService/PutGrant` | §6.1 |
| POST | `{BASE}/yufeng.grant.v1.GrantService/RevokeGrant` | §6.1 |
| POST | `{BASE}/yufeng.asset.v1.AssetService/CreateAsset` | §9 |
| POST | `{BASE}/yufeng.asset.v1.AssetService/UpdateAsset` | §9 |
| POST | `{BASE}/yufeng.asset.v1.AssetService/DeleteAsset` | §9 |
| POST | `{BASE}/yufeng.asset.v1.AssetService/ListAssets` | §9 |
| POST | `{BASE}/yufeng.asset.v1.AssetService/GetAsset` | §9 |
| POST | `{BASE}/yufeng.asset.v1.AssetService/AttachUnit` | §9 |
| POST | `{BASE}/yufeng.asset.v1.AssetService/DetachUnit` | §9 |
| POST | `{BASE}/yufeng.asset.v1.AssetService/PutEdgeEnrollment` | §9 |
| POST | `{BASE}/yufeng.asset.v1.AssetService/GetEdgeEnrollment` | §9 |
| POST | `{BASE}/yufeng.asset.v1.AssetService/GetModelIngressWindow` | §9 |
| POST | `{BASE}/yufeng.asset.v1.AssetService/UpdateModelIngressWindow` | §9 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/ProposeArtifact` | §7.2 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/GateArtifact` | §7.3 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/StartShadow` | §7.4 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/PromoteCanary` | §7.5 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/PromoteEnforce` | §7.6 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/RollbackRelease` | §7.7 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/RetireRelease` | §7.7 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/DenyFeedback` | §7.8 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/GetRelease` | §7.9 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/ListReleases` | §7.9 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/GetReleaseTimeline` | §7.9 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/GetReleaseStats` | §7.9 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/ProposeCatalogArtifact` | §7.10 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/SignCatalogArtifact` | §7.10 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/ActivateCatalogArtifact` | §7.10 |
| POST | `{BASE}/yufeng.govern.v1.GovernService/RevokeCatalogArtifact` | §7.10 |
| POST | `{BASE}/yufeng.console.v1.ConsoleService/Dashboard` | §10.1 |
| POST | `{BASE}/yufeng.console.v1.ConsoleService/ListEvents` | §10.1 |
| POST | `{BASE}/yufeng.console.v1.ConsoleService/GetEvent` | §10.1 |
| POST | `{BASE}/yufeng.audit.v1.AuditService/ListAuditEntries` | §10.2 |
| POST | `{BASE}/yufeng.audit.v1.AuditService/VerifyChain` | §10.2 |
| POST | `{BASE}/yufeng.onboarding.v1.OnboardingService/GetOnboarding` | §19 |
| POST | `{BASE}/yufeng.onboarding.v1.OnboardingService/PutModelConfig` | §19 |
| POST | `{BASE}/yufeng.onboarding.v1.OnboardingService/TestModelConnectivity` | §19 |
| POST | `{BASE}/yufeng.onboarding.v1.OnboardingService/PutDeploymentSpecification` | §19 |
| POST | `{BASE}/yufeng.onboarding.v1.OnboardingService/CompleteOnboarding` | §19 |

**公开/探针（无认证）**

`{BASE}` 是业务口，单机默认 `https://127.0.0.1:9050`（Connect + `/app`）。`{ADMIN}` 是管理面，单机默认 `http://127.0.0.1:19090`。HTTP 探针**不**画在业务 `{BASE}` 上。

| 方法 | URL 路径 | 说明 |
|---|---|---|
| GET | `{ADMIN}/livez` | 存活探针 |
| GET | `{ADMIN}/readyz` | 就绪探针 |
| GET | `{ADMIN}/metrics` | Prometheus 指标 |
| GET | `{ADMIN}/version` | 版本信息 |
| POST | `{BASE}/yufeng.health.v1.HealthService/Livez` | Connect 版存活检查 |
| POST | `{BASE}/yufeng.health.v1.HealthService/Readyz` | Connect 版就绪检查 |
| POST | `{BASE}/yufeng.health.v1.HealthService/Version` | Connect 版版本信息 |
| GET | `{BASE}/app` 与 `{BASE}/app/assets/*` | 控制台静态资源（brain 托管 SPA，未知路径回退 `/app/index.html`；见 §17.1） |

**单元域（前端不调用；Bearer 会话令牌）**

| 方法 | URL 路径 | 对应章节 |
|---|---|---|
| POST | `{BASE}/yufeng.registry.v1.RegistryService/Register` | §2.1（新单元用部署级引导令牌；已注册单元用当前会话令牌） |
| POST | `{BASE}/yufeng.registry.v1.RegistryService/Refresh` | §2.3 |
| POST | `{BASE}/yufeng.registry.v1.RegistryService/Heartbeat` | §2.2 |
| POST | `{BASE}/yufeng.artifact.v1.ArtifactService/ListReleases` | §3 |
| POST | `{BASE}/yufeng.artifact.v1.ArtifactService/ListGenerations` | §3.2 |
| POST | `{BASE}/yufeng.artifact.v1.ArtifactService/ListUnitListenPlans` | §3.3 |
| POST | `{BASE}/yufeng.telemetry.v1.TelemetryService/UploadEvents` | §4 |

**Edge 邻近模型旁路（前端不调用）**

| 方法 | URL 路径 | 对应章节 |
|---|---|---|
| POST | `{MODELSIDE}/yufeng.modelside.v1.ModelSideIngressService/SubmitTraffic` | §21.5（Edge 主动连接；Unix 域套接字或相互传输层安全协议） |
| POST | `{BASE}/yufeng.modelside.v1.ModelResultService/UploadResults` | §21.5（ModelSide 工作负载身份；只含结果元数据） |

**智能代理、工作进程与执行单元（字段契约及当前实现）**

| 方法 | URL 路径 | 对应章节 |
|---|---|---|
| POST | `{BASE}/yufeng.agent.v1.AgentControlService/RegisterAgent` | §18.1（bootstrap 认证） |
| POST | `{BASE}/yufeng.agent.v1.AgentControlService/RefreshAccessToken` | §18.1（refresh 认证） |
| POST | `{BASE}/yufeng.agent.v1.AgentControlService/PollInstructions` | §18.1 |
| POST | `{BASE}/yufeng.agent.v1.AgentControlService/AckInstruction` | §18.1 |
| POST | `{BASE}/yufeng.agent.v1.AgentControlService/Heartbeat` | §18.1 |
| POST | `{BASE}/yufeng.agent.v1.AgentControlService/OpenTurn` | §18.1（Agent 双令牌） |
| POST | `{BASE}/yufeng.agent.v1.AgentControlService/GetTurn` | §18.1（Agent 双令牌） |
| POST | `{BASE}/yufeng.agent.v1.AgentControlService/ListTurnItems` | §18.1（Agent 双令牌） |
| POST | `{BASE}/yufeng.agent.v1.AgentControlService/ExtendInstructionLease` | §18.1（Agent 双令牌） |
| POST | `{BASE}/yufeng.agent.v1.AgentControlService/YieldTurn` | §18.1（Agent 双令牌） |
| POST | `{BASE}/yufeng.agent.v1.AgentControlService/CompleteTurn` | §18.1（Agent 双令牌） |
| POST | `{BASE}/yufeng.agent.v1.AgentControlService/RequestUserInput` | §18.1（Agent 双令牌） |
| POST | `{BASE}/yufeng.agent.v1.AgentInteractionService/GetThread` | §18.5.1（用户会话） |
| POST | `{BASE}/yufeng.agent.v1.AgentInteractionService/GetTurn` | §18.5.1（用户会话） |
| POST | `{BASE}/yufeng.agent.v1.AgentInteractionService/ListTurnItems` | §18.5.1（用户会话） |
| POST | `{BASE}/yufeng.agent.v1.AgentInteractionService/SteerTurn` | §18.5.1（用户会话） |
| POST | `{BASE}/yufeng.agent.v1.AgentInteractionService/AppendFollowUp` | §18.5.1（用户会话） |
| POST | `{BASE}/yufeng.agent.v1.AgentInteractionService/AnswerUserInput` | §18.5.1（用户会话） |
| POST | `{BASE}/yufeng.agent.v1.AgentInteractionService/CancelTurn` | §18.5.1（用户会话） |
| POST | `{BASE}/yufeng.agent.v1.AgentInteractionService/GetApproval` | §18.5.1（审批冻结投影） |
| POST | `{BASE}/yufeng.agent.v1.AgentInteractionService/DecideApproval` | §18.5.1（用户会话 + 审批授权） |
| POST | `{BASE}/yufeng.agent.v1.AgentProfileService/ListAgentProfiles` | §18.5.3（受管 Agent 档案） |
| POST | `{BASE}/yufeng.agent.v1.AgentProfileService/CreateAgentProfile` | §18.5.3（创建流量审查岗位） |
| POST | `{BASE}/yufeng.agent.v1.AgentProfileService/UpdateAgentProfile` | §18.5.3（替换工具、资产与启停状态） |
| POST | `{BASE}/yufeng.agent.v1.AgentProfileService/DeleteAgentProfile` | §18.5.3（停止新案件委派） |
| POST | `{BASE}/yufeng.agent.v1.AgentProfileService/BatchUpdateAgentProfiles` | §18.5.3（批量替换工具与资产） |
| POST | `{BASE}/yufeng.run.v1.RunService/CreateRun` | §18.2 |
| POST | `{BASE}/yufeng.run.v1.RunService/GetRun` | §18.2 |
| POST | `{BASE}/yufeng.run.v1.RunService/ListRuns` | §18.2 |
| POST | `{BASE}/yufeng.run.v1.RunService/CancelRun` | §18.2 |
| POST | `{BASE}/yufeng.run.v1.RunService/WatchRun` | §18.2 |
| POST | `{BASE}/yufeng.run.v1.RunService/ListRunEvents` | §18.2 |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/CreateWorkerBootstrap` | §18.3（管理员一次性引导） |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/RegisterWorkerIdentity` | §18.3（一次性引导） |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/RefreshWorkerAccessToken` | §18.3（刷新轮换） |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/RevokeWorkerIdentity` | §18.3（管理员撤销） |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/RegisterWorker` | §18.3（认证后登记档案） |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/RequestWorkerEnrollment` | §18.3（外部 agentd 申请登记） |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/GetWorkerEnrollmentResult` | §18.3（登记客户端取得加密激活结果） |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/DecideWorkerEnrollment` | §18.3（管理员批准或拒绝） |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/ListWorkerEnrollments` | §18.3（管理员登记队列） |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/AcknowledgeWorkerActivation` | §18.3（目标 worker 确认激活包已持久化） |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/RenewWorkerCertificate` | §18.3（工作负载证书轮换） |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/ListWorkers` | §18.3（管理员范围内执行池） |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/PollWork` | §18.3 |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/ExtendLease` | §18.3 |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/ReportProgress` | §18.3 |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/CompleteWork` | §18.3 |
| POST | `{BASE}/yufeng.worker.v1.WorkerService/FailWork` | §18.3 |
| POST | `{BASE}/yufeng.toolgateway.v1.ToolGatewayService/ListTools` | §18.4 |
| POST | `{BASE}/yufeng.toolgateway.v1.ToolGatewayService/DescribeTool` | §18.4 |
| POST | `{BASE}/yufeng.toolgateway.v1.ToolGatewayService/InvokeTool` | §18.4 |
| POST | `{BASE}/yufeng.toolgateway.v1.ToolGatewayService/ListSkills` | §18.4 |
| POST | `{BASE}/yufeng.toolgateway.v1.ToolGatewayService/LoadSkill` | §18.4 |
| POST | `{BASE}/yufeng.model.v1.ModelGatewayService/Generate` | §18.10（Agent 或 RUN_SUPERVISOR 双令牌；座架目标契约） |
| POST | `{BASE}/yufeng.model.v1.ModelGatewayService/CompleteChat` | §19.4（Agent `access_token`；禁止 `-model-url`） |
| POST | `{BASE}/yufeng.model.v1.ModelGatewayService/GetModelGateway` | §19.4（仅管理员；引导完成后） |
| POST | `{BASE}/yufeng.model.v1.ModelGatewayService/UpdateModelGateway` | §19.4（仅管理员；引导完成后改槽） |
| POST | `{BASE}/yufeng.model.v1.ModelGatewayService/ProbeModelGateway` | §19.4（仅管理员；引导完成后探测，不改引导状态） |
| POST | `{BASE}/yufeng.session.v1.SessionService/CreateSession` | §18.5 |
| POST | `{BASE}/yufeng.session.v1.SessionService/SendMessage` | §18.5 |
| POST | `{BASE}/yufeng.session.v1.SessionService/PollMessages` | §18.5 |
| POST | `{BASE}/yufeng.session.v1.SessionService/ListMessages` | §18.5 |
| POST | `{BASE}/yufeng.command.v1.CommandService/PollCommands` | §18.6 |
| POST | `{BASE}/yufeng.command.v1.CommandService/ReportStep` | §18.6 |

**其他预留**

| 方法 | URL 路径 | 说明 |
|---|---|---|
| — | `yufeng.console.v1` 扩展 service | 事故/任务/执行实例时间线，service 名与 URL 待定稿 |

---

## 2. 注册契约 · RegistryService

### 2.1 Register

**认证**：请求始终携带 `Authorization: Bearer <token>`。新单元使用部署级引导令牌；已注册单元只有在当前访问令牌仍有效时才能重注册，且不得用引导令牌覆盖 `refresh_token_hash`。匿名请求、既有单元回退使用部署级引导令牌均返回 `unauthenticated` 或 `permission_denied`，不得覆盖既有单元身份。访问令牌失效时必须调用 `Refresh`，不得重跑 `Register`。

**请求 `RegisterRequest`**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| unit_id | string | ✅ | 单元持久化生成的稳定标识，长度 1–64；重复注册携带同一值才幂等 |
| kind | UnitKind | ✅ | `UNIT_KIND_EDGE` / `UNIT_KIND_HOST` |
| version | string | ✅ | 单元程序版本 |
| contract_version | string | ✅ | 期望契约版本，当前 `v1` |
| asset | Asset | ✅ | 上报资产信息；`asset.id` 为空时服务端分配，v1 作为主资产绑定 |
| pubkey_hint | string | ❌ | 本地签名公钥指纹：`hex(sha256(pubkey_bytes))` |
| capabilities | ProducerCapabilities | edge 必填 | 生产能力广告；只用于兼容性判断，不产生授权 |

`ProducerCapabilities` 固定表达：`outputs[]`（关键事件、普通样本、票据特征）、`projection_versions[]`、`postures[]`、`sensors[]`（HTTP、Coraza）、是否具备本地证据环与本地异步旁路，以及事件批量、在途请求、事件落盘、证据环容量、`model_ingress_hard_limit` 与 `max_model_ingress_batch_items`。模型输入硬上限是本机启动配置允许的条数、实际保留字节和排队年龄上界，只用于收窄签名监听计划；它不能产生模型可见字段或授权。列表规范化为去重升序，枚举不接受未指定值，版本字符串不得为空。

它只回答“这台单元客观上能生产什么”，由 brain 持久化为兼容性与调度输入：

- 自报能力不得生成 Tools、Bindings、证据读取权或任何消费资格；
- `UNIT_KIND_EDGE` / `UNIT_KIND_HOST` 仍是两类边缘身份，`yufeng-modelside` 不注册成第三种 UnitKind；
- 实际采样、证据和转发行为只服从签名资产世代；能力不足时拒绝或降级该世代，不得由单元自行换策略；
- 资产标签只参与 brain 编译下一份世代，不能作为边缘实时读取的未签名开关。

**响应 `RegisterResponse`**

| 字段 | 类型 | 说明 |
|---|---|---|
| unit_id | string | 确认后的单元标识 |
| asset_id | string | v1 主资产标识 |
| token | string | 会话令牌，后续所有单元 RPC 携带 |
| heartbeat_interval | int32 | 心跳间隔秒数，默认 30 |
| server_time | Timestamp | 时钟对齐 |
| contract_version | string | 协商后的契约版本 |
| refresh_token | string | 可轮换刷新令牌，仅本次返回明文 |
| access_expires_in | int32 | 访问令牌存活秒数，默认 1800 |

**错误**：`unauthenticated`（缺少或无效 Bearer 凭证）；`invalid_argument`（unit_id 格式非法）；`permission_denied`（用引导令牌覆盖已有单元，或 `asset.id` 已属于其他主体）；`failed_precondition` + `reason=contract_version_unsupported`（版本无法协商）；`failed_precondition` + `reason=signing_key_mismatch`（pubkey_hint 与服务端签名密钥不匹配）。

### 2.2 Heartbeat

**请求 `HeartbeatRequest`**

| 字段 | 类型 | 说明 |
|---|---|---|
| unit_id | string | 单元标识 |
| generation | uint64 | 进程代次；edge/host 每次启动生成新值。中台见 generation 变化即重置该单元的计数器快照 |
| release_counters | repeated ReleaseCounter | 按 release 的单调累计计数，见下 |
| buffered_events | uint32 | 本地缓冲事件数 |
| in_flight_requests | uint32 | 在途请求数 |
| loaded_release_count | uint32 | 已装载 release 数 |
| version | string | 当前程序版本 |
| capabilities | ProducerCapabilities | 当前进程能力；用于升级后刷新注册快照，不改变身份或授权 |
| producer_health | ProducerHealth | 关键事件/普通样本缓冲与丢弃、模型输入窗口实际值、状态、排队/在途容量、按原因丢弃、投影失败及健康投影版本；不得携带请求原文 |
| current_generation_id | string | Edge 已验签并原子装载的当前资产世代标识；未装载时为空 |
| current_generation_seq | int64 | 与 `current_generation_id` 对应的世代序号；中台只用该心跳回执确认策略已真实生效 |
| current_listen_plan_version | uint64 | Edge 已验签并应用的单元监听计划版本；未应用时为零。Brain 与人工接入配置的期望版本比较后确定逐项状态，不主动探测 Edge 管理口 |

**`ReleaseCounter`**

| 字段 | 类型 | 说明 |
|---|---|---|
| release_id | string | 发布标识 |
| artifact_id | string | 已装载制品标识 |
| mode | ReleaseMode | shadow / canary / enforce |
| requests_total | uint64 | 该 release 范围内已裁决请求总数 |
| blocks_total | uint64 | 该 release 命中且最终动作 block 的次数 |
| observe_total | uint64 | 该 release 命中但最终动作 observe 的次数 |
| canary_selected_total | uint64 | canary 命中的请求数 |
| upstream_5xx_total | uint64 | 转发上游返回 5xx 的次数（响应阶段回填） |
| latency_micros_total | uint64 | 转发耗时累计（微秒） |
| latency_samples | uint64 | 耗时样本数 |

**响应 `HeartbeatResponse`**

| 字段 | 类型 | 说明 |
|---|---|---|
| health | UnitHealth | `UNIT_HEALTH_HEALTHY` / `UNIT_HEALTH_DEGRADED` |
| artifact_pull_hint | bool | true 表示建议立即拉取一次 `ListReleases` |
| telemetry_flush_hint | bool | true 表示建议尽快上传缓冲事件 |
| server_time | Timestamp | 服务端时钟 |

**语义**：连续 3 个心跳间隔未收到心跳，中台把单元标为 degraded（保留记录，事件账照收，不删除资产）。

心跳只上报生产健康、已装载版本与丢弃计数；不得携带原始请求或用健康字段临时改写授权。Brain 可据此停止向不兼容单元下发新世代，并据 `current_generation_id`、`current_generation_seq` 与 `current_listen_plan_version` 确定人工部署的 Edge 是否已取得期望制品；不得反向连接或探测 Edge，也不能据此给 ModelSide 扩权。

### 2.3 Refresh

**认证**：无访问令牌。请求体携带 `unit_id` 与当前刷新令牌。

| 字段 | 类型 | 说明 |
|---|---|---|
| unit_id | string | 单元标识 |
| refresh_token | string | 当前刷新令牌明文 |

**响应**：新的 `token`、轮换后的 `refresh_token`、`access_expires_in`、`server_time`。旧刷新令牌立即失效。

**错误**：

| 条件 | 错误 |
|---|---|
| 缺字段 / 空令牌 | `invalid_argument` |
| 无此单元、刷新令牌哈希不匹配、已过期、重放已轮换令牌 | `unauthenticated` |
| 单元已吊销 | `permission_denied` |

brain 重启不删除 `refresh_token_hash`；只使尚未到期的访问令牌失效。edge 在原调用收到 `unauthenticated` 后必须串行 Refresh 并只重试一次，不得 Register；刷新失败则保留未提交工作并进入凭证恢复。

---

## 3. 制品下发契约 · ArtifactService

### 3.1 ListReleases

**请求 `ListReleasesRequest`**

| 字段 | 类型 | 说明 |
|---|---|---|
| unit_id | string | 本单元标识 |
| cursor | string | 上次响应的 `next_cursor`；首次为空 |
| full_snapshot | bool | true = 取全量快照页。cursor 为空或不是 `s:` 前缀时从第一项开始；cursor 为 `s:<offset>` 时从该偏移续页。**不得**在 `has_more` 时丢掉 cursor 从头重发 |
| max_bytes | int32 | 响应字节预算，默认 4 MiB，上限 16 MiB |

**响应 `ListReleasesResponse`**

| 字段 | 类型 | 说明 |
|---|---|---|
| items | repeated ReleaseItem | 见下 |
| next_cursor | string | 快照未完是 `s:<offset>`；整份快照取完后换成增量 feed 游标。两类游标不得混用 |
| has_more | bool | 还有下一页，立即续拉；为 true 时必须回传 `next_cursor` |
| snapshot | bool | 本次是否为快照页 |
| generation | AssetGeneration | 快照可附带该单元当前世代信封。边缘不得只装这一份最新信封而跳过中间序号，须经 `ListGenerations(since_seq)` 逐代追赶 |

**`ReleaseItem`**

| 字段 | 类型 | 说明 |
|---|---|---|
| release_id | string | 发布标识 |
| artifact | Artifact | 完整签名制品信封 |
| asset_id | string | 本条目针对的资产 |
| mode | ReleaseMode | shadow / canary / enforce |
| canary_percent | int32 | mode=canary 时生效，范围 1–25 |
| retired | bool | true = 退休墓碑，边缘收到即卸载并删除缓存 |
| retire_reason | RetireReason | retired=true 时给出原因 |
| changed_at | Timestamp | 状态变化时间 |

**拉取语义**：

1. 首次启动：`full_snapshot=true`，按返回快照重建本地 inventory；不在快照中的本地 release 卸载。
2. 稳态：按 `cursor` 增量拉取，服务端只返回 shadow/canary/enforce 的状态变化与退休墓碑；draft/signed 不下发。
3. 游标只有在整页条目全部“应用成功或隔离跳过”后才前进。单条验签失败/解不开的条目隔离并记录本地错误，不阻断同页其他条目。
4. 单元绑定到已有在役治理发布时，必须把这些发布补写入该单元的增量 feed。管理员人工 Edge 接入配置生成的基线检测器、规范化配置和模型档案属于完整资产世代，通过 `ListGenerations` 下发，不伪装成治理发布，也不写入 `ListReleases` feed。
4. 服务端保留每个 unit_asset 的发布日志至少 14 天（≥ 最大 TTL 7 天 + 余量）。cursor 过期时返回 `failed_precondition` + `reason=cursor_expired`，边缘改调 `full_snapshot=true` 重建。
5. 边缘本地同时按 `artifact.created_at + ttl` 自行过期卸载。即使退休墓碑丢失，最长 TTL 后也会失效，不会永久残留。
6. 拉取失败使用本地已验证世代继续工作（断网自治）；制品验签公钥来自启动配置，不从本接口获取。
7. **生产装载单位**：边缘不得按单条 `ReleaseItem` 立即混入当前检测器集。必须通过 `ListGenerations` 或快照里的 `generation` 信封凑齐同一 `generation_seq` 的检测器清单、规范化配置档、策略、形状规则、证据策略、证据摘要、流量审查策略与转发策略，验签并编译成功后原子替换 `activeGeneration`。入口姿态不进世代。实现不得把按条装载写成生产语义。支持 `TrafficReviewPolicy` 的 Edge 按签名模式运行统计窗与有界候选；旧 Edge 才使用 `NoDetectionSampleRate` 兼容值，不得读取实时资产标签改变行为。
8. 条目 5 的本地 `ttl` 过期仅适用于演示规则。生产策略认 `hard_expires_at`。

### 3.2 ListGenerations

**请求 `ListGenerationsRequest`**

| 字段 | 类型 | 说明 |
|---|---|---|
| unit_id | string | 令牌单元必须等于该字段 |
| asset_id | string | 必须已绑定到该单元 |
| since_seq | int64 | 只返回严格大于该序号的已签名信封 |
| max_bytes | int32 | 响应字节预算，默认 4 MiB，上限 16 MiB，与 `ListReleases` 共用 `ArtifactPageMaxBytes` / `ArtifactPageHardMaxBytes` |

**响应 `ListGenerationsResponse`**

| 字段 | 类型 | 说明 |
|---|---|---|
| generations | repeated AssetGeneration | 按 `generation_seq` 升序。单封超过预算时本页仍整封返回该条 |
| has_more | bool | 还有后续序号。为 true 时客户端必须以本页最后一条的 `generation_seq` 作为下次 `since_seq` 续拉，不得跳号、不得改用最新信封 |

**`AssetGeneration`**：`generation_id`、`asset_id`、`generation_seq`、`parent_generation_id`、`members[]`（仍是 `ReleaseItem`）、`min_edge_version`、`not_before`、`envelope_signature`、`rollback_of`。

无签名回滚授权时，`generation_seq` 小于当前已装载序号的信封必须拒绝。部分损坏不得进入混合状态。

下发前 brain 读取目标单元最近的 `ProducerCapabilities`：必须能产生关键事件、普通样本和票据特征，支持 `event/v1` 与 HTTP 传感；世代点名 Coraza 清单时还必须广告 Coraza 传感；已有监听计划的姿态必须落在广告姿态内。缺失或不兼容返回 `failed_precondition` + `producer_capability_mismatch`，整份世代不返回。能力只作拒绝条件，不能扩大资产绑定、发布范围、Tools、Bindings 或消费可见性。

### 3.3 ListUnitListenPlans

**请求 `ListUnitListenPlansRequest`**

| 字段 | 类型 | 说明 |
|---|---|---|
| unit_id | string | 必填，必须与访问令牌单元一致；不一致返回 `permission_denied` |
| since_version | uint64 | 只返回严格大于该版本的已签名计划 |

**响应 `ListUnitListenPlansResponse`**

| 字段 | 类型 | 说明 |
|---|---|---|
| plans | repeated UnitListenPlan | 按 `version` 升序；每页最多 32 份 |
| has_more | bool | 为 true 时以本页最后一份的 `version` 续拉 |

`UnitListenPlan` 的签名、版本、目标与原子替换语义见 §21.1。该流与 `ListGenerations` 独立，两者不共享游标或激活事务。

---

## 4. 遥测上行契约 · TelemetryService

### 4.1 UploadEvents

**请求 `UploadEventsRequest`**

| 字段 | 类型 | 说明 |
|---|---|---|
| events | repeated Event | ≤ 100 条/批，至少一次投递 |

**Event 的单元上报要求**（字段定义见 `yufeng/event/v1/v1.proto`）：

- 必填：`id`、`occurred_at`、`unit_id`、`asset_id`、`kind`、`verdict`；
- `request_id` 填写边缘生成并回写响应头的请求标识；
- `release_traces`：`verdict ∈ {BLOCK, OBSERVE, ESCALATE}` 时必填，逐条给出涉及的 release；纯 `ALLOW` 事件可省略（统计走心跳）。

**响应 `UploadEventsResponse`**

| 字段 | 类型 | 说明 |
|---|---|---|
| accepted | int32 | 入账数 |
| deduped | int32 | 按事件 id 去重数，边缘可安全丢弃 |
| rejected | repeated RejectedEvent | 逐条拒因，边缘记日志后丢弃，不无限重投 |

**`RejectedEvent`**：`event_id`、`code`（`invalid_event` / `unknown_unit` / `unknown_asset`）、`message`。

**语义**：`accepted + deduped + rejected == len(events)`。令牌中的单元必须等于每条事件的 `unit_id`；`asset_id` 必须已绑定到该单元，否则该条 `rejected.code=permission_denied`（不要用 `unknown_asset` 区分「不存在」和「不是你的」）。服务端在同一数据库事务内写入事件账与事务发件箱后返回；内部流投递由发件箱消费者完成，失败时重试，不得出现“已接受但异步检测永久丢失”。当前实现使用事务发件箱与持久消费者，不采用“入账后尽力发布”的旁路。

生产事件还应携带：检测键列表、各检查面覆盖度、边缘观察状态和当时生效的 `generation_id/generation_seq`。资源优先级固定如下：

1. 拦截、观察、`would_have_blocked`、已检出未缓解、未映射、检查不完整和检测器失败是关键事件，100% 入账；
2. 支持 `TrafficReviewPolicy` 的 Edge 不再把普通 `SYNC_NO_DETECTION` 随机逐条入账，而是完整计入统计窗并只冻结有界代表；旧 Edge 兼容路径仍使用 `NoDetectionSampleRate=1%`，且不创建 Agent 指令；
3. 进程日志、`/metrics` 和心跳属于可观测/注册域，不得包装成流量 Event 再送贾维斯；确定性运维规则可以另行产生运维告警；
4. 原始请求只留边缘本地证据环，不属于 `UploadEvents`。脱敏 Event 也不得因为后续要打分而扩大原文字段。

**检查票据冻结。** `CheckTicket` 是已接受 Event 的字段级脱敏研判投影，不是边缘另发的一批资源，也不是模型输入。Brain 必须在事件入账事务中按事件引用的历史资产世代解析 `EvidenceDigest`，把完整票据、票据摘要或带原因的隔离状态与 Event 行、事务发件箱一同提交。票据摘要固定为 `sha256:` 加确定性 Protocol Buffers 编码的 SHA-256；下游案件与 Agent 研判消息同时携带完整票据与摘要，禁止只有 `{event_id}`。

Edge 到 ModelSide 的本地异步旁路只使用 §21.5 的 `NormalizedTraffic`，不得先把正文包装成 Event 或 `CheckTicket` 再经 Brain 派发。隔离原因闭集为 `generation_missing`、`generation_mismatch`、`evidence_digest_missing`、`projection_invalid`。事件仍优先 `accepted` 入账；缺历史世代或投影失败时不创建研判票据。入队 Agent 的谓词见 §18.1.1（第一阶段演示）与 §18.1.2（生产）。

**失败行为**：`unavailable` 整批保留重投；`unauthenticated` 先串行轮换当前刷新令牌并只重试本批一次，刷新或重试仍失败才停止上传并进入凭证恢复，当前版本不得回退部署级引导令牌；`resource_exhausted` 退避降速。

### 4.2 流量统计窗与审查候选

`TelemetryService.UploadTrafficWindows` 接收五分钟 `TrafficWindow`；窗口标识由单元、资产、开始时间、世代序号和审查策略摘要确定，重复上传返回 `deduped`。窗口保留总请求数、关键请求数、处置与覆盖度计数、前 32 个方法×路由组合和聚合 `other`，不得含请求原文。

`TrafficWindow.evidence_drop_reasons` 是丢弃数的闭集分解：`encoding_failed`、`low_risk_capacity_reserved`、`vault_capacity_exhausted`、`vault_unavailable`。各值之和必须等于 `evidence_dropped_count`；brain 拒绝未知理由、负数或总数不一致的窗口。低于 60 分的证据最多使用证据库 80% 空间，保留的 20% 只给高风险代表；即使证据库不可用，统计窗仍继续计数并明确上报理由。

`TelemetryService.UploadReviewCandidates` 接收每单元每窗最多四个 `ReviewCandidate`。候选包含风险特征、脱敏检查投影、边缘证据句柄、摘要与过期时间；不包含文件路径或原始字节。响应逐条返回接受、去重或拒因。brain 必须优先接受统计窗；候选写池耗尽时显式拒绝候选，不得阻塞治理连接或伪造已接受。

旧 `UploadEvents` 保留非流量事件与旧单元兼容。支持 `TrafficReviewPolicy` 的单元不得再用普通流量 1% 随机事件代替统计窗。关键流量要求完整计数，不要求攻击洪峰中的每个重复请求都形成独立数据库行。

---

## 5. 认证契约 · AuthService

操作域所有 RPC（除 `Login`、可选 `Register` 和探针）都携带 `Authorization: Bearer <user_session_token>`。单元域令牌不归本节管理。

### 5.1 Login

**URL**：`POST {BASE}/yufeng.auth.v1.AuthService/Login`（无认证）。

**请求 `LoginRequest`**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| username | string | ✅ | 3–64 字符 |
| password | string | ✅ | 8–128 字符，传输层必须走 HTTPS/TLS |

**响应 `LoginResponse`**

| 字段 | 类型 | 说明 |
|---|---|---|
| token | string | 用户会话令牌，后续操作域 RPC 携带 |
| expires_at | Timestamp | 过期时间，默认 12h |
| user | User | 当前用户（不含密码） |
| access | EffectiveAccess | **必须**按授予表即时展开，形状与 §5.3 相同；禁止省略本字段 |

**错误**：`unauthenticated`（用户名或密码错误）；`failed_precondition` + `reason=user_disabled`（用户停用）。

### 5.2 Logout

**URL**：`POST {BASE}/yufeng.auth.v1.AuthService/Logout`（Bearer 操作令牌）。

请求空消息；服务端吊销当前令牌。重复调用幂等，已过期令牌直接返回成功。

### 5.3 GetMe

**URL**：`POST {BASE}/yufeng.auth.v1.AuthService/GetMe`（Bearer 操作令牌）。

响应：`user`（`User`）+ `access`（`EffectiveAccess`）。**服务端必须按授予表即时展开 `access`，禁止回空对象充当「已实现」。** 控制台启动时用它恢复登录态；**按钮与菜单只看 `access`，不看 `user.role` 猜权限**。`unauthenticated` 表示需要重新登录。

`Login` 响应同样带 `access`，形状与 GetMe 一致，避免登录后再打一轮。引导未到 `ONBOARDING_STATE_COMPLETED` 时，`access` 仍展开（管理员 `tools` = `user.admin` + `grant.write` + `console.read`，Bindings 为空），但控制台路由层强制管理员进 `/app/setup`（§19）。

**`EffectiveAccess`**

| 字段 | 类型 | 说明 |
|---|---|---|
| tools | repeated string | 当前生效的动作名，见 §6.1 工具名表 |
| bindings | repeated BindingRef | 当前生效的对象范围；空数组表示可读范围也为空（除登录/改密） |

**`BindingRef`**：`kind`（`asset` / `unit` / `release`）+ `id`（具体 ID，禁止 `*`）。

服务端每次请求按授予表**即时展开** `access`（吊销立即生效，不等重新登录）。前端仍应在 GetMe / 写失败后刷新 `access`。

### 5.4 ChangePassword

**URL**：`POST {BASE}/yufeng.auth.v1.AuthService/ChangePassword`（Bearer 操作令牌）。

| 请求字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| old_password | string | ✅ | 当前密码 |
| new_password | string | ✅ | 新密码，至少 8 字符 |

**语义**：校验旧密码后更新；成功时吊销当前用户除本次会话外的全部会话令牌，本令牌保持有效。

### 5.5 Register（默认关闭）

**URL**：`POST {BASE}/yufeng.auth.v1.AuthService/Register`（是否公开由 `auth.allow_self_registration` 决定，默认 false）。

| 请求字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| username | string | ✅ | 唯一，3–64 字符 |
| password | string | ✅ | 8–128 字符 |
| display_name | string | ❌ | 显示名 |

**响应**：`User`。自注册用户固定 `USER_ROLE_VIEWER`、`USER_STATE_ACTIVE`；角色提升由 `USER_ROLE_ADMIN` 经 `UserService.UpdateUser` 完成。
**错误**：`failed_precondition` + `reason=self_registration_disabled`；`already_exists` + `reason=username_taken`。

**通用 `User` 结构**：`user_id`、`username`、`display_name`、`role`、`state`、`created_at`、`updated_at`、`last_login_at`。任何接口不返回密码或密码哈希。

### 5.6 GetLoginConfig

**URL**：`POST {BASE}/yufeng.auth.v1.AuthService/GetLoginConfig`（无认证）。

响应：`allow_self_registration`（bool）、`password_min_length`（int32，默认 8）、`session_ttl`（Duration，默认 `43200s`）。登录页据此决定是否显示“注册”入口和密码提示文案。

---

## 6. 用户管理 · UserService

所有 RPC 要求调用者 `access.tools` 含 `user.admin`（默认只在 `USER_ROLE_ADMIN` 模板里）。角色字段仍只决定新用户的**默认 Tools 模板**，不代替授予。

| RPC | URL | 说明 |
|---|---|---|
| CreateUser | `POST {BASE}/yufeng.user.v1.UserService/CreateUser` | 创建用户：username、password、display_name、role；新用户 Bindings 为空，须再 `PutGrant` |
| ListUsers | `POST {BASE}/yufeng.user.v1.UserService/ListUsers` | query、role、state、page_size/page_token |
| GetUser | `POST {BASE}/yufeng.user.v1.UserService/GetUser` | user_id → User |
| UpdateUser | `POST {BASE}/yufeng.user.v1.UserService/UpdateUser` | 可改 display_name、role、state；改 role 只换默认模板，**不**自动改已有授予 |
| DeleteUser | `POST {BASE}/yufeng.user.v1.UserService/DeleteUser` | 软删除；不可删除最后一个 ACTIVE 且持 `user.admin` 的账户 |
| AdminResetPassword | `POST {BASE}/yufeng.user.v1.UserService/AdminResetPassword` | user_id + new_password + revoke_sessions |

### 6.1 授予 · GrantService

人的 Tools × Bindings 存在授予表，由本服务维护。不另建权限产品。写操作带 `Idempotency-Key`。

| RPC | 认证 | 说明 |
|---|---|---|
| ListGrants | 会话 | 无 `subject_user_id` 时返回**自己**的授予；查他人须持 `grant.write` |
| PutGrant | `grant.write` | 新增或覆盖一条授予 |
| RevokeGrant | `grant.write` | 按 `grant_id` 吊销，立即从 GetMe.access 消失 |

**`Grant`**

| 字段 | 类型 | 说明 |
|---|---|---|
| grant_id | string | 服务端生成 |
| subject_user_id | string | 被授予人 |
| tools | repeated string | 动作名，见下表 |
| bindings | repeated BindingRef | 具体对象，禁止 `id="*"` 或空（`user.admin` / `grant.write` / `catalog.manage` 可 bindings 空，因为不针对资产） |
| created_by | string | 授予人，系统引导为 `system` |
| created_at | Timestamp | |
| expires_at | Timestamp | 可选；过期视同吊销 |

**工具名（前端按钮与服务端同一张表）**

| 工具名 | 含义 | 默认 `VIEWER` | 默认 `OPERATOR` | 默认 `ADMIN` |
|---|---|---|---|---|
| `console.read` | 读仪表盘/事件/发布/资产/审计（仍受 Bindings 裁剪） | ✓ | ✓ | ✓ |
| `govern.propose` | ProposeArtifact | | ✓ | |
| `govern.gate` | GateArtifact | | ✓ | |
| `govern.start_shadow` | StartShadow | | ✓ | |
| `govern.promote_canary` | PromoteCanary | | | |
| `govern.promote_enforce` | PromoteEnforce | | | |
| `govern.rollback` | RollbackRelease | | | |
| `govern.retire` | RetireRelease | | | |
| `govern.deny_feedback` | DenyFeedback | | | |
| `asset.create` | CreateAsset；管理员全局工具，不要求预先存在资产 Binding | | | ✓ |
| `asset.update` | UpdateAsset | | | ✓ |
| `asset.delete` | DeleteAsset | | | ✓ |
| `asset.attach` | AttachUnit | | | ✓ |
| `asset.detach` | DetachUnit | | | ✓ |
| `run.create` | CreateRun | | ✓ | |
| `agent.manage` | 创建、编辑、批量设置和删除受管 Agent 档案；资产 Bindings 必须非空 | | | ✓ |
| `grant.write` | PutGrant / RevokeGrant / 列出他人授予 | | | ✓ |
| `user.admin` | UserService 写与用户列表 | | | ✓ |
| `catalog.manage` | 工具与技能目录提案、签名、激活、撤销 | | | ✓ |

`ADMIN` 模板**不含**治理推进工具。要让某人能 enforce，必须另写一条授予（不能授给自己）。

**PutGrant 拒绝（`permission_denied`）**

| reason | 何时 |
|---|---|
| `grant_self` | `subject_user_id` 等于调用者 |
| `grant_wildcard` | 任一 binding.id 为 `*` 或空 |
| `grant_scope` | 被授资产/单元/发布不是调用者 Bindings 的子集 |
| `grant_unknown_tool` | tools 含未登记名 |

系统引导授予的 `created_by=system` 包含管理员全局账户工具与 `asset.create`；Bindings 为 `CompleteOnboarding` 当时已存在的全部资产 ID，允许零资产时为空，禁止 `*` 与虚构 `bootstrap`。`asset.create` 的授权只要求管理员角色和全局工具，不要求目标资产已存在或预先出现在 Bindings。创建成功后把新 ID **只**追加进创建者自己的系统授予 Bindings，不写入他人授予；`DeleteAsset` 同步从所有授予 Bindings 剔除该 ID。存在 Edge 人工接入配置的资产禁止删除，必须先完成技术人员退役流程并移除配置。

### 6.2 读路径裁剪

`ListAssets` / `ListReleases` / `ListEvents` / `Dashboard` / `GetAsset` / `GetRelease` / `GetEvent` / `ListAuditEntries`：只返回调用者 Bindings 覆盖的对象。`console.read` 没有或 Bindings 为空时列表为空。零资产管理员仍可调用全局 `asset.create` 创建第一项资产；该写权限不扩大任何读范围。点开名单外 ID：`permission_denied`（与「不存在」相同对外文案，避免探测）。

**枚举**：

- `UserRole`：`USER_ROLE_UNSPECIFIED, USER_ROLE_ADMIN, USER_ROLE_OPERATOR, USER_ROLE_VIEWER`。
- `UserState`：`USER_STATE_UNSPECIFIED, USER_STATE_ACTIVE, USER_STATE_DISABLED, USER_STATE_DELETED`。

**写操作幂等**：CreateUser / UpdateUser / DeleteUser / AdminResetPassword 携带 `Idempotency-Key`。

---

## 7. 治理管道操作面 · GovernService

### 7.1 状态机

```text
RELEASE_STATE_DRAFT ──GateArtifact(通过)──▶ RELEASE_STATE_SIGNED ──StartShadow──▶ RELEASE_STATE_SHADOW
  ▲                                              │
  └────GateArtifact(不通过)──────────────────────┘
RELEASE_STATE_SHADOW ──PromoteCanary──▶ RELEASE_STATE_CANARY ──PromoteEnforce──▶ RELEASE_STATE_ENFORCE
   │                         │  PromoteEnforce（仅单元数不足以分桶，§7.6）    │
   └──────── RollbackRelease / RetireRelease ──────────────────────────────┘
                            ▼
                  RELEASE_STATE_RETIRED
```

- `GateArtifact` 通过时：写入回放报告 → 计算全信封 `artifact_id` → Ed25519 签名 → 状态变为 signed。
- `GateArtifact` 不通过：RPC 成功返回，状态留在 draft，`replay_report.passed=false`。
- `StartShadow` 是显式命令；signed 制品从 shadow 起步，不直接生效。
- L1 无 pending 审批流。`PromoteCanary` / `PromoteEnforce` 是带门槛的执行命令：调用者必须持有对应工具且 Bindings 覆盖该 release 的 `scope.asset_ids`；提案人不得自己点推进。

**通用枚举**：

- `ReleaseState`（线上 JSON / proto 只许全名）：`RELEASE_STATE_UNSPECIFIED`、`RELEASE_STATE_DRAFT`、`RELEASE_STATE_SIGNED`、`RELEASE_STATE_SHADOW`、`RELEASE_STATE_CANARY`、`RELEASE_STATE_ENFORCE`、`RELEASE_STATE_RETIRED`。散文里的 draft/shadow 只作口语，不得当仪表盘键或门禁断言。
- `RetireReason`：`RETIRE_REASON_UNSPECIFIED`、`RETIRE_REASON_ROLLBACK`、`RETIRE_REASON_MANUAL`、`RETIRE_REASON_TTL`、`RETIRE_REASON_SUPERSEDED`。
- GovernService 的全部写 RPC 都要求 `Idempotency-Key`；每次状态转换产生审计条目。

### 7.2 ProposeArtifact

**请求**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| kind | ArtifactKind | 与 intent 二选一 | 带 `yufeng_dev` 构建标签的开发演示可直接给 `KIND_RULE`。生产必须带 `intent`（§18.1.2）；无 intent 的 `KIND_RULE` / `rules/v1` 人侧与工具网关一律 `failed_precondition` |
| payload | bytes | ✅ | 按 payload_schema 校验 |
| payload_schema | string | ✅ | 带 `yufeng_dev` 构建标签的开发演示固定 `rules/v1`。生产为 `policy/v1` 或形状语言 schema；无 `intent` 的 `rules/v1` 见上栏，必须 `failed_precondition` |
| scope | Scope | ✅ | `asset_ids` 非空。生产自动通道禁止空主机 + `path_prefix=/` |
| ttl | Duration | ✅ | 第一阶段 300s–604800s。生产改 `review_at` / `hard_expires_at` |
| supersedes | string | ❌ | 被取代的旧 `artifact_id`，单指针 |
| evidence_refs | repeated string | ❌ | 0–100 条；正式证据链阶段启用校验 |
| created_by | string | 忽略 | 客户端填写无效。服务端写入认证身份：操作域为用户 `user_id`，工具网关为能力令牌 `sub`（Agent 为 `agent_id`） |

**响应 `ProposeArtifactResponse`**：`release_id`、`state=RELEASE_STATE_DRAFT`、`artifact`（无签名、`id` 为空、`created_at` 为空）。请求头必须携带 `Idempotency-Key`，重复请求返回同一 release_id。

### 7.3 GateArtifact

**请求**：`release_id`、`corpus_ref`（缺省 `builtin:l1-rules-v1`）、`budget`（Duration，缺省 5s）。

**响应 `GateArtifactResponse`**：

| 字段 | 说明 |
|---|---|
| release_id / state | 通过为 SIGNED，不通过仍为 DRAFT |
| replay_report | 完整报告（通过时已写入制品信封） |
| artifact | 通过时为已签名、已填 artifact_id 的制品 |

**通过标准**：`malicious_total > 0`、`malicious_blocked == malicious_total`、`benign_blocked == 0`（或 ≤ 配置阈值）、`management_blocked == 0`。不通过不是 RPC 错误，前端按 `replay_report.passed` 渲染。

### 7.4 StartShadow

**请求**：`release_id` + `Idempotency-Key`。
**响应**：`Release`（`state=RELEASE_STATE_SHADOW`）。前置状态必须为 `RELEASE_STATE_SIGNED`，否则 `failed_precondition` + `release_state_conflict`。

### 7.5 PromoteCanary

**请求**：`release_id`、`canary_percent`（1–25，缺省 5）+ `Idempotency-Key`。

**前置门槛（服务端用治理配置与心跳计数器检查）**：

- shadow 时长 ≥ `shadow_min_duration`（配置默认 300s，演示环境可调低）；
- shadow 请求数 ≥ `shadow_min_requests`（配置默认 100，演示环境可调低）；
- `replay_report.passed == true`。

该资产绑定单元数 `< ceil(100 / canary_percent)` 时**禁止**本 RPC：返回 `failed_precondition` + `reason=canary_cohort_too_small`，状态留在 `RELEASE_STATE_SHADOW`。人手全量走 §7.6 的直达边。

不满足时长/请求数门槛时返回 `failed_precondition`，details 中携带 `GateResult`：逐项给 `gate_key`、`passed`、`required`、`actual`、`message`。满足时状态推进到 `RELEASE_STATE_CANARY` 并返回 `Release`。

**授权与数据来源**：调用者 `access.tools` 须含 `govern.promote_canary`，且调用者标识不得等于该 release 的 `created_by`（用户比 `user_id`，Agent 提案则任何用户均不等于该 `agent_id`，可以人手推进）。自动调度走同一套门槛，但**排除提案人创建或绑定的单元**产生的心跳计数与事件；提案人为 Agent 且无此类单元时排除集为空。排除后不够则不自动推进，按钮交给另一持工具的用户。不得把模型输出当作晋升输入（§18.1.1）。

### 7.6 PromoteEnforce

**请求**：`release_id` + `Idempotency-Key`。

**合法前置状态**：

1. `RELEASE_STATE_CANARY`：走下列 canary 门槛。
2. `RELEASE_STATE_SHADOW` **且**该资产绑定单元数 `< ceil(100 / canary_percent)`（单机一份 compose 走这边）：不要求 `canary_min_*`。仍要求 `replay_report.passed == true`、调用者持 `govern.promote_enforce`、`user_id ≠ created_by`。其它 `SHADOW` → `failed_precondition` + `release_state_conflict`（必须先 canary）。

**canary 门槛**（仅前置为 `RELEASE_STATE_CANARY`）：

- canary 时长 ≥ `canary_min_duration`（默认 300s）；
- canary 请求数 ≥ `canary_min_requests`（默认 100）；
- `deny_feedback_total == 0`；
- 守护窗口健康：`consecutive_bad_windows == 0`。

不满足返回 `failed_precondition` + `GateResult`；满足推进到 `RELEASE_STATE_ENFORCE`。推进时若 `artifact.supersedes` 非空，旧发布自动退休（`reason=SUPERSEDED`）。授权与自动调度数据排除规则同 §7.5（工具名为 `govern.promote_enforce`）。自动调度**不得**走 shadow 直达边。

### 7.7 RollbackRelease / RetireRelease

| RPC | 请求 | 语义 |
|---|---|---|
| RollbackRelease | release_id, reason(string) + Idempotency-Key | shadow/canary/enforce → retired，`retire_reason=ROLLBACK`；边缘下次拉取收到墓碑 |
| RetireRelease | release_id, reason(string) + Idempotency-Key | 操作域只允许 `reason=MANUAL`；`TTL` / `SUPERSEDED` 由服务端内部触发 |

### 7.8 DenyFeedback

**请求**：`release_id`、`event_id`、`note`（1–2000 字符）+ `Idempotency-Key`。

**校验**：release 必须处于 canary/enforce；事件存在、属于该 release、`verdict=BLOCK`。重复举报同键幂等。连续举报达到 `deny_feedback_block_threshold` 或守护窗口达到坏窗口阈值时，服务端自动 RollbackRelease 并记审计。

### 7.9 查询组

| RPC | 请求要点 | 响应要点 |
|---|---|---|
| GetRelease | release_id | `Release`：release_id、state、artifact、proposed_at、signed_at、shadow_started_at、canary_started_at、enforced_at、retired_at、retire_reason、created_by |
| ListReleases | states 过滤（repeated ReleaseState）、asset_id、query（匹配 release_id/artifact_id/created_by）、page_size/page_token | `releases[]` 每条与 `GetRelease` 同一 `Release` 形状（含 `created_by`、`state`、`proposed_at`、`artifact.kind`、`artifact.payload_schema`）+ `next_page_token` |
| GetReleaseTimeline | release_id、page_size/page_token | entries：sequence、release_id、from_state、to_state、actor、reason、gate_report_ref、occurred_at |
| GetReleaseStats | release_id | shadow/canary/enforce 统计块：duration、requests、blocks、observes、canary_selected、deny_feedback_total、upstream_5xx、p99_micros；guard：consecutive_bad_windows、last_bad_window_at、last_bad_reasons；computed_at |

### 7.10 工具与技能目录发布

目录发布只允许管理员且要求账户级工具 `catalog.manage`，所有写操作必须带 `Idempotency-Key`。四个动作严格复用通用发布状态机：

| RPC | 合法输入与结果 |
|---|---|
| ProposeCatalogArtifact | 只接受 `KIND_TOOL_DESCRIPTOR` + `tool/v1` 或 `KIND_SKILL` + `skill/v1`，校验完整载荷后建立 `draft`；技能的 `publisher_key_id` 由服务端按当前制品签名根填写，客户端值不可信 |
| SignCatalogArtifact | `draft → signed`；经生产制品签名器签发，不跑流量规则回放；签名前再次校验工具实现、修复程序绑定和技能全部内容地址 |
| ActivateCatalogArtifact | `signed → shadow`；这是目录显式激活边，激活后才会被 ToolGateway 列举或加载 |
| RevokeCatalogArtifact | `shadow/canary/enforce → retired`，理由固定为人工撤销；新列表立即不可见，已打开 Turn 按 §18.10.5 的 checkpoint 规则处理 |

目录载荷无资产 Scope，不进入边缘资产世代，也不参与流量发布的 shadow/canary 自动晋升。`SignCatalogArtifact` 与 `GateArtifact` 是不同动作：前者只处理目录载荷的结构、绑定和签名，后者仍只处理需要回放门禁的流量制品。

---

## 8. 回放组件 · 进程内库接口（非网络 API）

**边界**：v1 回放是 brain 内部组件，`GateArtifact` 进程内调用 `replay.Run(artifact, corpus, budget)`，不开网络接口。输入输出即数据契约中的 `ReplayReport`。

**`ReplayReport` 字段**（`yufeng/artifact/v1/v1.proto`）：

| 字段 | 说明 |
|---|---|
| malicious_total / malicious_blocked | 恶意样本数与拦截数，要求全拦 |
| benign_total / benign_blocked | 良性样本数与误伤数，要求零误伤或低于配置阈值 |
| management_total / management_blocked | 管理面样本数与命中数，命中必须为零 |
| passed | 是否通过 |
| corpus_ref | 语料集引用 |

**判定标准**：恶意全拦、良性零误伤（或 ≤ 配置阈值）、管理面零命中。回放引擎与 edge 使用同一个裁决纯函数。

---

## 9. 资产面 · AssetService

| RPC | 请求 | 说明 |
|---|---|---|
| CreateAsset | Asset | 手工登记防御资产（旁路设备或待绑定目标）。`id` 可由调用方指定或服务端生成。仅 `USER_ROLE_ADMIN`。成功后把新 ID 追加进调用者授予 Bindings |
| UpdateAsset | asset_id + Asset 变更字段 + update_mask(FieldMask) + 可选 `expected_updated_at` | 仅管理员。只允许操作方修改 display_name、labels、criticality、max_auto_tier、access_mode；capabilities/last_probe_at 只能由单元探针上报。响应带回 `Asset.updated_at` |
| DeleteAsset | asset_id | 仅管理员。硬删除没有 Edge 人工接入配置和活动单元绑定的资产行；存在接入配置返回 `failed_precondition`。成功后从所有授予 Bindings 剔除该 ID |
| ListAssets | query、criticality 过滤、page_size/page_token | 详情含绑定单元只读投影、健康状态、在役 release 数。任意持 `console.read` 且 Bindings 覆盖的角色可读 |
| GetAsset | asset_id | 单个资产详情（读路径，非管理员可查自己范围内的资产） |
| AttachUnit | asset_id、unit_id + Idempotency-Key | 仅管理员。把旁路资产交给 edge 单元保护；v1 每单元只允许一个主资产 |
| DetachUnit | asset_id、unit_id + Idempotency-Key | 仅管理员。解绑；正在受保护的资产解绑后边缘下次快照卸载 |
| PutEdgeEnrollment | 既有 asset_id、稳定 unit_id、入口姿态、监听地址、反向代理上游、traffic_key、可信代理网段、ModelProfile、ModelIngressWindow + Idempotency-Key | 仅管理员且要求目标资产 `asset.update`。规范化配置并预声明 Edge 与 `${unit_id}-modelside` 身份，签发下一监听计划和保留既有非相关策略的下一资产世代；相同规范摘要直接返回原坐标，不重复签发。预声明单元不得改绑其它资产 |
| GetEdgeEnrollment | asset_id、unit_id | 要求 `console.read` 与目标资产 Binding。返回规范化配置、模型档案摘要、ModelSide 身份、期望/实际监听计划和资产世代、最近心跳/结果以及状态 |
| GetModelIngressWindow | asset_id、unit_id | 读取最新签名监听计划中的中央期望、最近心跳实际值、期望/已应用监听计划版本、状态和收窄原因。要求 `console.read` 与资产 Binding |
| UpdateModelIngressWindow | asset_id、unit_id、desired、expected_listen_plan_version + Idempotency-Key | 仅管理员且要求 `asset.update`。校验单元属于该资产且广告模型输入窗口能力；克隆最新监听计划、只替换窗口、递增版本、重新签名并审计，不创建资产世代 |

**写冲突语义**：不再整行后写胜。操作方字段与单元探针字段分区；同区冲突采用 `updated_at` 乐观锁，版本不匹配返回 `failed_precondition` + `version_mismatch`。

`AssetDetail.units[]` 是只读单元投影：包含单元标识、种类、版本、健康、入口姿态、流量键、最近心跳、生产能力、生产健康以及已装载的资产世代标识和序号。`AssetDetail.edge_enrollments[]` 返回该资产全部人工 Edge 接入投影。接入状态枚举为 `EDGE_ENROLLMENT_STATUS_WAITING_FOR_REGISTRATION`、`EDGE_ENROLLMENT_STATUS_ONLINE`、`EDGE_ENROLLMENT_STATUS_OUT_OF_SYNC`、`EDGE_ENROLLMENT_STATUS_OFFLINE`：未注册为等待，最近心跳过期为离线，在线但实际制品坐标不同为未收敛，在线且坐标相同为在线。ModelSide 状态依据身份是否首次合法上报及最近结果时间单独计算，不用 Edge 状态冒充。`tap_silent` / `tap_skew` 保持逐单元可见。流量审查策略更新后，控制台必须等待绑定 Edge 的心跳世代序号全部达到目标序号，才能显示“已生效”。控制台不能修改能力或健康，也不能把广告字段转换成操作权限。

任何签发新资产世代的治理或策略变更都在同一事务内把该资产全部 `EdgeEnrollment.expected_generation_id/seq` 推进到新坐标。`UpdateModelIngressWindow` 不创建资产世代，但会在同一事务内同步对应接入记录的规范窗口、摘要和期望监听计划版本；因此接入状态不会继续引用已被替代的坐标。

`ModelIngressWindow` 同时要求正数 `max_items`、`max_retained_bytes` 与 `max_queue_age`；平台接受 1–65536 条、1–256 MiB、10 毫秒–5 分钟。Brain 初始签发默认 4096 条、128 MiB、2 秒。Edge 将中央期望逐项收窄到本机硬上限；字节上限优先，因此年龄是可达覆盖目标，不保证最坏正文负载一定装满整段时间。中央缩容不批量清空现有项：后台发送和过期清理自然收敛，新流量仍按新上限淘汰最旧可排队项；收敛前状态为 `MODEL_INGRESS_WINDOW_STATE_CONVERGING`。本机收窄后状态为 `MODEL_INGRESS_WINDOW_STATE_DEGRADED` 并返回条数、字节或年龄的闭集原因；旁路关闭为 `MODEL_INGRESS_WINDOW_STATE_DISABLED`。

模型输入窗口易失且至多一次：不落盘、不重试，Edge 或 ModelSide 退出、传输失败、ModelSide 拒绝均允许丢失。`ProducerHealth.dropped_local_bypass_items` 保留为总计；`model_ingress_drops` 分解为淘汰最旧、过期、单项超限、在途容量、单次准入工作预算、传输失败与 ModelSide 拒绝，分项之和必须等于总计。为避免大窗口缩容或大小正文混合在单个业务请求上形成线性暂停，请求路径每次准入最多淘汰 32 个排队项；达到该预算仍无法安全准入时丢新项并累计 `admission_budget`。排队与在途保留字节都计入实际窗口。

`TrafficReviewPolicyStatus.edge_supported` 是面向整份资产绑定的发布兼容性投影，不是“任一 Edge 支持”或授权判断。它仅在资产至少绑定一个 Edge，且每个绑定 Edge 都在最近两分钟内成功心跳并同时声明 `traffic-window/v1` 与 `traffic-review-candidate/v1` 时为真；没有绑定 Edge、任一绑定 Edge 心跳过期、缺少任一能力或能力载荷不可解析时都为假。绑定到同一资产的 host 不参与该投影。该字段为真以后，策略是否已实际生效仍按上一段的全体绑定 Edge 世代序号判断。

---

## 10. 控制台查询面

### 10.1 ConsoleService

**Dashboard（无参数）** 响应：

| 字段 | 类型 | 说明 |
|---|---|---|
| assets_total | int64 | 资产总数 |
| degraded_units | int64 | 心跳丢失的单元数 |
| releases_by_state | map<ReleaseState, int64> | 发布状态分布 |
| events_24h_total | int64 | 24h 事件总数 |
| events_24h_blocked | int64 | 24h 拦截数 |
| model_alerts_24h | int64 | 24h 内 `KIND_MODEL_ALERT` 数 |
| pending_retire_soon | int64 | 24h 内将到期的发布数 |

**ListEvents**

请求：`asset_id`、`release_id`、`verdict`、`kind`、`since`、`until`、`query`（路径/规则关键词）、`page_size/page_token`。
响应：`events[] Event` + `next_page_token`。默认按 `occurred_at` 降序。每条 `Event` 含 `triage_reason`（proto 枚举全名，如 `TRIAGE_REASON_DETECTED_UNMITIGATED`）。人机交付活栈用本 RPC 判定未缓解入账，不另开查询。

**GetEvent**：`event_id` → `event`（含检测结论数组与 release_traces）、`model_inferences[]` 和 `triage_deliveries[]`。模型推理返回模型组、类型、版本、分数、阈值、攻击分类、结果种类、档案摘要和记录时间。研判交付只返回关联案件、贾维斯指令、处理方、种类、状态、创建时间和确认时间；不得返回能力令牌、租约载荷、内部指令正文或原始请求。整个响应继续按事件所属资产做 Bindings 裁剪。

### 10.2 AuditService

**ListAuditEntries**：`object_type`、`object_id`（支持 release_id/asset_id/unit_id）、`run_id`、`turn_id`、`actor`、`since/until`、`page_size/page_token`。`run_id` 与 `turn_id` 可单独查询，也可同时收窄；结果始终按全局链序号返回，不另造进程内事件真相。
响应条目：`sequence`、`occurred_at`、`actor_type`、`actor_id`、`action`、`object_type`、`object_id`、`run_id`、`turn_id`、`lease_epoch`、`budget_id`、`payload_digest`、`schema_version`、`details`、`previous_hash`、`entry_hash`。这些坐标和摘要全部参与哈希；模型输入、模型输出、工具参数、工具结果、步骤守卫、回执和错误只记录 `sha256:` 摘要与非敏感计数，不把原文、令牌、密钥或请求头写入审计链。

**VerifyChain**：`start_sequence`、`end_sequence` → `valid`、`start_hash`、`end_hash`、`entries_checked`。任意链段都以首条记录自带的 `previous_hash` 为边界校验，随后逐条校验前向链接；篡改正文、坐标、摘要或链接均返回 `valid=false`。控制台“审计”页一键验证。

---

## 11. 健康 · HealthService

| RPC | 参数 | 响应 |
|---|---|---|
| Livez | 无 | `{status: "ok", server_time}` |
| Readyz | 无 | `{status: "ok"}` 或错误；依赖 PostgreSQL 可达、迁移版本一致、内嵌 NATS 就绪 |
| Version | 无 | `{version, contract_version, build_sha, build_time}` |

裸 HTTP 探针 `/livez` `/readyz` `/metrics` `/version` 供容器编排与监控使用；HealthService 是给 Connect 客户端的同义接口。

## 12. 行为规范（跨接口的硬语义）

1. **请求标识**：edge 在入口为每个请求生成服务端随机 `x-request-id` 并回写响应头；默认不信任客户端传入的同名头。该 id 只进事件 `request_id` 做关联，**不得**作为 canary 分桶键（攻击者可重试直到未抽中）。
2. **小比例分桶**：默认键为 `unit_id || release_id`（`cohort_type=unit`）。取 `sha256(key)` 的前 8 字节，按大端解释为 uint64，`bucket = u64 % 10000`；`bucket < canary_percent * 100` 即命中。同一边缘实例对同一发布结果确定。该资产绑定单元数 `< ceil(100/canary_percent)` 时不得进入 canary，只许 shadow + 人手 `PromoteEnforce`（§7.6 直达边）。第一生产版无 `hmac_tenant`。按 `unit_id` 分桶已写入本契约；若代码仍按 `request_id`，不得用本句假装未立项。
3. **治理统计**：edge 每心跳上报按 release 的单调计数器；brain 保存上一轮快照做窗口差值。generation 变化视为进程重启，旧快照作废。事件账不承担全量请求计数。
4. **守护窗口与自动回滚**：canary/enforce 期间按 `guard_window`（默认 5 分钟）聚合；坏窗口 = 新增误报举报 ≥ 阈值，或 `upstream_5xx_rate` 相对 shadow 基线异常（超过基线 2 倍且绝对差 > 0.005），或 p99 相对基线增长超过 10% 且绝对值增加超过 5ms。连续 `guard_bad_windows`（默认 2）个坏窗口自动 RollbackRelease 并审计。
5. **门槛配置**：`shadow_min_duration` / `shadow_min_requests` / `canary_percent_default` / `canary_min_duration` / `canary_min_requests` / `ttl_default` / `ttl_min` / `ttl_max` / `deny_feedback_block_threshold` / `guard_window` / `guard_bad_windows` 进配置文件，之后制品化。演示环境允许放松 shadow/canary 门槛。
6. **复核与硬过期**：禁止用单一 `ttl` 同时表示复核与失效。`review_at` 到点只产生复核任务，默认不自动卸闸。`hard_expires_at` 可选，到期按 `expiry_behavior`（`retire` / `keep_until_superseded` / `alert_keep`）处理。本契约已要求 `review_at` / `hard_expires_at`；演示制品可继续用 `ttl` 字段，但不得把单一 ttl 写成生产语义。
7. **发布命令幂等**：治理写操作带 `Idempotency-Key`；边缘对同 `(release_id, artifact_id, mode, canary_percent)` 重复装载天然幂等。
8. **事件双时间**：`occurred_at` 是产生方时钟，入库时间由中台补写；事件账只追加不修改。
9. **账户与口令**：密码只以服务端选择的单向哈希存储，任何 RPC 不返回密码；用户会话默认 12 小时过期；登录接口按用户名+来源限流，错误超限返回 `resource_exhausted`。

---

## 13. 错误码目录

| code | HTTP 状态（参考） | 触发场景 | 前端/单元应对 |
|---|---|---|---|
| unauthenticated | 401 | 登录失败或令牌缺失、失效、撤销 | 单元停止受保护调用，当前版本进入运维恢复且不得回退部署级引导令牌；先尝试刷新令牌续期；控制台清会话回登录页 |
| permission_denied | 403 | 已登录但角色无权调用 | 前端隐藏入口；yfctl 展示无权限 |
| already_exists | 409 | 用户名/幂等实体已存在 | 提示重复，不重试 |
| not_found | 404 | release/资产/事件/用户不存在 | 不重试 |
| invalid_argument | 400 | 载荷不合约、page_size 超限 | 修正后重试；遥测侧进 rejected |
| failed_precondition | 400 | 状态机前置不满足、门槛差量、cursor 过期、版本冲突、用户停用、自注册关闭 | 读 details；推进类展示 GateResult；cursor 过期改全量快照 |
| resource_exhausted | 429 | 超批量/限流/登录尝试超限 | 退避降速 |
| unavailable | 503 | 中台暂不可用 | 指数退避；遥测本地缓冲 |

`failed_precondition` 的 details 可携带结构化原因：`release_state_conflict`、`gate_not_satisfied`（含 GateResult）、`cursor_expired`、`version_mismatch`、`contract_version_unsupported`、`signing_key_mismatch`、`user_disabled`、`self_registration_disabled`。

---

## 14. 服务范围清单（非完成证书）

本列表只划定接口范围，不是完成证书。当前软件支持一个企业站点、一个中台、一个数据面单元、客户入口终止业务传输层安全协议、反向代理首发和 Envoy 外部授权兼容入口；对应版本只有在公开 GitHub Release 的精确提交证据复核通过后才算机器验收闭环，客户现场变更记录仍须由现场负责人填写。局部接口存在或演示修复循环通过都不能替代真实上游、故障、安全、容量和回退证据。

范围：§2、§3、§4 全部；GovernService 的 Propose/Gate/StartShadow/PromoteCanary/PromoteEnforce/Rollback/Retire/DenyFeedback/GetRelease/ListReleases/GetReleaseTimeline/GetReleaseStats 及四个目录生命周期动作；AuthService 六个（Register 可配置关闭但 RPC 保留）；UserService 六个；回放组件（进程内）；AssetService 七个；ConsoleService 三个；AuditService 两个；HealthService 三个。
预留不实现：事故/任务/执行实例扩展；对象级细粒度 RBAC、审批流、OIDC/SSO。

---

## 15. 与参考实现的对照

| 参考实现（sentry-docker） | 本设计 | 变化与理由 |
|---|---|---|
| `POST /api/v1/defense/releases/{id}/approve-canary / approve-full` | PromoteCanary / PromoteEnforce | 参考实现是两段人工审批；本设计 L1 为带门槛的执行命令，调用者必须持有相应 Tools 且 Bindings 覆盖该 release；不另建审批产品 |
| `POST /api/v1/defense/releases/{id}/rollback`、`deny-feedback` | RollbackRelease / DenyFeedback | 举报自动阻断推进 + 连续举报自动回滚，语义显式化 |
| `GET /api/v1/defense/audit`、`dashboard` | AuditService.ListAuditEntries / ConsoleService.Dashboard | 审计加链段校验（VerifyChain） |
| `POST /api/v1/detections/batch`（traffic-sentry，上限 500） | TelemetryService.UploadEvents（上限 100） | 批量幂等保留；事件账同步提交，NATS 只做下游分发 |
| enforcer admin 的 ReleaseCommand（shadow/canary/percent + 幂等键） | ListReleases 条目 mode/canary_percent + Idempotency-Key | 推送命令改拉取 + 退休墓碑（断网自治优先） |
| Envoy 生成 request_id 且 `preserve_external_request_id: false` | edge 服务端生成 `x-request-id` 只做关联；canary 按 `unit_id` | 防止客户端选择 canary 桶；并防止按请求重试改桶 |
| `POST /internal/v1/replay`（replay-runner） | 进程内 replay.Run | 独立服务改中台内组件，同一裁决函数复用 |
| incidents / tasks / agent-runs 三组查询 | 预留（console 扩展，L0/L3） | L1 用不到，接口位留好 |
| 四角色 RBAC / 用户体系 | v1 用户账户 + `USER_ROLE_ADMIN` / `USER_ROLE_OPERATOR` / `USER_ROLE_VIEWER` 固定角色 | 先满足登录与用户管理；对象级权限与审批流生产版恢复 |
| safeshield `GET/POST /admin/ip-block-cidrs / ip-whitelist`（免重启运行时管理） | 被制品热加载整体取代（IP 黑白名单未来作为一种规则制品） | 运行时可变面必须免重启；一切可变物制品化 |

---

## 16. 调用序列与报文示例

下列示例里的 `http://brain:9050` 只说明路径形状。单机交付业务口是 **`https://127.0.0.1:9050`**，管理面探针是 `http://127.0.0.1:19090`。

### 16.1 单元冷启动到稳态

```
edge ──► Register ──────────► token / asset_id / server_time
     ──► ListReleases(full_snapshot=true) ──► 装载，重建 inventory
     ──► 循环：Heartbeat（30s，含 release_counters）
              UploadEvents（批量 / 断网落盘缓冲补传）
              ListReleases（游标增量）
```

### 16.2 操作员发布一个规则制品

```
yfctl ─► ProposeArtifact ──► draft + release_id
      ─► GateArtifact ─────► passed 则签名，state=signed
      ─► StartShadow ──────► state=shadow
      ─► PromoteCanary(5) ─► 门槛检查 → canary
      ─► PromoteEnforce ───► 门槛检查 → enforce
      （心跳计数器驱动守护窗口；DenyFeedback 可触发自动回滚）
```

### 16.3 遥测上传

```bash
curl -X POST http://brain:9050/yufeng.telemetry.v1.TelemetryService/UploadEvents \
  -H "Authorization: Bearer <单元会话令牌>" \
  -H "Connect-Protocol-Version: 1" \
  -H "Content-Type: application/json" \
  -d '{"events":[{"id":"bbfdba8ba0f4c40d451443be9a4c4150","unitId":"unit-1","requestId":"f1b2c3d4","occurredAt":"2026-08-15T08:29:37Z","assetId":"asset-1","source":"yufeng-edge","kind":"KIND_TRAFFIC","verdict":"VERDICT_BLOCK","http":{"method":"GET","path":"/api/items","queryRedacted":"id=1%20UNION%20SELECT"},"releaseTraces":[{"releaseId":"rel_01J...","artifactId":"sha256:6421…27e","mode":"RELEASE_MODE_CANARY","canaryPercent":5,"canarySelected":true,"matched":true}],"detections":[{"detectorId":"sha256:6421…27e","ruleId":"sql-union","confidence":1,"tier":"TIER_L1_TRAFFIC"}]}]}'
# → {"accepted":1,"deduped":0,"rejected":[]}
```

### 16.4 制品拉取响应（节选）

```json
{
  "items": [{
    "releaseId": "rel_01J...",
    "artifact": { "id": "sha256:6421…27e", "kind": "KIND_RULE", "payload": "…base64…", "ttl": "86400s", "createdAt": "2026-08-15T08:29:37Z", "signature": {"keyId": "a661c30281372fb1", "sig": "…"} },
    "assetId": "asset-1",
    "mode": "RELEASE_MODE_CANARY",
    "canaryPercent": 5,
    "retired": false
  }],
  "nextCursor": "eyJzZXEiOjEyfQ",
  "hasMore": false,
  "snapshot": false
}
```

### 16.5 门槛差量错误

```json
{"code": "failed_precondition", "message": "promotion gates not satisfied", "details": [{"reason": "gate_not_satisfied", "gates": [{"gateKey": "canary_min_requests", "passed": false, "required": "100", "actual": "12"}]}]}
```

### 16.6 控制台报文示例

**Dashboard**

```bash
curl -X POST http://brain:9050/yufeng.console.v1.ConsoleService/Dashboard \
  -H "Authorization: Bearer <操作令牌>" \
  -H "Connect-Protocol-Version: 1" \
  -H "Content-Type: application/json" \
  -d '{}'
# → {"assetsTotal":3,"degradedUnits":0,"releasesByState":{"RELEASE_STATE_ENFORCE":2,"RELEASE_STATE_SHADOW":1},"events24hTotal":1241,"events24hBlocked":37,"pendingRetireSoon":0}
```

**ListEvents（事件流第一页）**

```bash
curl -X POST http://brain:9050/yufeng.console.v1.ConsoleService/ListEvents \
  -H "Authorization: Bearer <操作令牌>" \
  -H "Connect-Protocol-Version: 1" \
  -H "Content-Type: application/json" \
  -d '{"pageSize":50}'
# → {"events":[{"id":"bbfd…","unitId":"unit-1","requestId":"f1b2c3d4","occurredAt":"2026-08-15T08:29:37Z","assetId":"asset-1","source":"yufeng-edge","kind":"KIND_TRAFFIC","verdict":"VERDICT_BLOCK","http":{"method":"GET","path":"/api/items","queryRedacted":"id=1%20UNION%20SELECT"},"releaseTraces":[{"releaseId":"rel_01J...","artifactId":"sha256:6421…27e","mode":"RELEASE_MODE_CANARY","canaryPercent":5,"canarySelected":true,"matched":true}],"detections":[{"detectorId":"sha256:6421…27e","ruleId":"sql-union","confidence":1,"tier":"TIER_L1_TRAFFIC"}]}],"nextPageToken":"eyJvZmZzZXQiOjUwfQ"}
```

**GetReleaseStats（防护策略页门槛展示）**

```json
{
  "releaseId": "rel_01J...",
  "state": "RELEASE_STATE_CANARY",
  "canary": {
    "duration": "302s",
    "requests": "128",
    "blocks": "2",
    "observes": "0",
    "canarySelected": "6",
    "denyFeedbackTotal": "0",
    "upstream5xx": "0",
    "p99Micros": "18200"
  },
  "guard": {"consecutiveBadWindows": 0},
  "computedAt": "2026-08-15T08:34:00Z"
}
```

### 16.7 控制台登录与用户管理

```
浏览器 ──► AuthService/Login ──► token + user
       ──► AuthService/GetMe ──► 恢复登录态
       ──► ConsoleService/... / GovernService/...（Bearer 会话令牌）
USER_ROLE_ADMIN ──► UserService/CreateUser / ListUsers / UpdateUser / DeleteUser / AdminResetPassword
用户 ──► AuthService/ChangePassword
```

---

## 17. 前端接入指南

### 17.1 托管与同源

- 目标与人机交付形态：brain 把 Vite/React 静态导出托管在 `/app`，单页应用（SPA）路由回退到 `/app/index.html`。页面用相对路径调用 `/yufeng.*`。
- 开发期仍可用 Vite 服务器，并把 `/yufeng` 代理到真实 brain；默认目标为本机 `https://127.0.0.1:9050`，开发代理可以接受本机自签名证书。使用 `-dev-insecure` 明文 Brain 时由开发者显式设置 `VITE_BRAIN_URL=http://127.0.0.1:9050`。**开发、测试预览与交付构建都只连接真实 brain**，不得提供可切换的模拟业务模式。
- 控制台运行时代码只装配 `ConnectClient`，不接受环境变量切换到本地业务状态机，也不编译设计回廊、演示账户、固定口令、固定业务指标或模拟业务数据。案件、审批与执行状态只从类型化引用重新读取当前授权状态。[Connect-ES](glossary.md#connect-es)（Connect 协议的 TypeScript 客户端生成与运行库）已选定、**本档不强制引入**；手写 `ConnectClient` 可交付，生成客户端落地后只替换适配层内部。

### 17.2 认证与生效权限

- 控制台必须有登录页：调 `AuthService.Login`，成功后把 `token`、`expires_at`、`user`、`access` 写入 `sessionStorage`（键名 `yufeng.session`）。
- 应用启动时调 `AuthService.GetMe` 恢复登录态并**用响应里的 `access` 覆盖本地缓存**（授予吊销立即生效）。
- 所有操作域 Connect 请求统一加 `Authorization: Bearer <token>`。
- 收到 `unauthenticated`：清除会话、回登录页。不要把任何密码或初始管理员凭据编译进前端静态包。
- **不要用 `user.role` 决定按钮。** 用 `access.tools` + `access.bindings`：
  - 用户管理入口：`tools` 含 `user.admin`
  - 授予页：`tools` 含 `grant.write`
  - Agent 档案写操作：使用 `AgentProfile.can_manage`；该只读字段由服务端按 `tools` 含 `agent.manage` 且档案全部原始资产落在当前 `bindings` 内计算，客户端不得用已裁剪的 `AgentProfile.bindings` 自行推断
  - 写按钮：对应工具名见 §6.1，且当前对象 ID 落在 `bindings` 内
  - 推进 canary/enforce：另须「当前用户不是该 release 的 `createdBy`」
- 角色徽章仍可展示 `user.role`（模板名），但文案不得写成「管理员=全部权限」。

### 17.3 当前适配层与 Connect-ES 目标

当前控制台页面只依赖 `ConsoleClient` 接口，运行时唯一实现是手写 Connect JSON HTTP POST 的 `ConnectClient`。仓库尚未引入 Connect-ES 包，也尚未生成 TypeScript 客户端；手写客户端只是过渡适配层，不是最终契约源。

组件测试可以从 `console/src/test/` 注入 `ConsoleClient` 场景夹具，但夹具不得进入 `console/src/api/`、不得由应用工厂选择，也不得作为服务行为的权威测试。夹具中的状态变化只用于触发页面反馈；认证、授权、发布状态机和案件编排语义由使用真实 PostgreSQL 迁移与 Brain 服务实现的 Go 集成测试负责。TypeScript 测试只验证页面呈现、Connect 路由、请求编码、响应归一化与错误处理。

Connect-ES 已选定但未引入。后续从同一份 proto 生成客户端后，只替换 `ConnectClient` 内部实现，页面与状态管理继续依赖 `ConsoleClient`，不需要改动。目标适配层形态如下：

```ts
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AuthService } from "./gen/yufeng/auth/v1/auth_connect";
import { ConsoleService } from "./gen/yufeng/console/v1/console_connect";

const transport = createConnectTransport({ baseUrl: "/" });
const auth = createClient(AuthService, transport);
const console = createClient(ConsoleService, transport);

// 登录：POST /yufeng.auth.v1.AuthService/Login
const login = await auth.login({ username, password });
sessionStorage.setItem("yufeng.session", JSON.stringify(login));

// 业务调用：自动带 Bearer
const session = JSON.parse(sessionStorage.getItem("yufeng.session") ?? "null");
const res = await console.dashboard({}, {
  headers: { Authorization: `Bearer ${session?.token ?? ""}` },
});
```

在生成客户端落地前，新增页面不得绕过 `ConsoleClient` 分散拼接 URL 或直接调用 `fetch`；临时手写类型集中留在适配层，并随 Connect-ES 接入一起替换。

### 17.4 页面 → RPC 映射

完整 URL 见 §1.1；方法名、字段名见对应章节。

| 页面 | 数据读取（服务端已按 Bindings 裁剪） | 写操作（按钮 ↔ 工具名） |
|---|---|---|
| 引导 `/app/setup` | GetOnboarding | PutModelConfig / TestModelConnectivity / CompleteOnboarding（仅管理员）；只确认模型网关与贾维斯，不登记或部署数据面 |
| 登录 | Login / GetMe（含 `access`） | Logout、ChangePassword |
| 资产 | ListAssets / GetAsset / GetEdgeEnrollment / GetModelIngressWindow | `asset.create` / `asset.update` / `asset.delete` / `asset.attach` / `asset.detach` / PutEdgeEnrollment / UpdateModelIngressWindow（仅管理员；前端按生效 Tools 隐藏写入口） |
| 防护策略（路由仍为 `/app/releases`） | ListReleases / GetRelease / Timeline / Stats | `govern.propose` / `gate` / `start_shadow` / `promote_canary` / `promote_enforce` / `rollback` / `retire` |
| 事件 | ListEvents / GetEvent（含模型推理与贾维斯研判交付） | `govern.deny_feedback`；可跳转关联案件 |
| 审计 | ListAuditEntries / VerifyChain | 无（只读，仍裁 Bindings） |
| 用户 | ListUsers / GetUser（须 `user.admin`） | UserService 写 |
| Agent 管理 `/app/agent` | ListAssets / ListAgentProfiles / SessionService | `agent.manage` 对档案增删改和批量覆盖；工具与资产在 Agent 编辑弹窗中设置 |
| 授予（兼容路由 `/app/grants`） | GrantService.ListGrants（须 `grant.write` 才能看他人） | PutGrant / RevokeGrant；新导航不再单列，转入 Agent 管理入口 |
| 模型网关 `/app/model` | GetModelGateway（仅管理员） | UpdateModelGateway / ProbeModelGateway（仅管理员；不退引导状态） |

服务端是唯一鉴权；前端隐藏只为少点出 `permission_denied`。列表为空是「你的范围内没有」，不是系统故障。

### 17.4.1 防护策略页推进按钮

| 按钮 | 显示条件 | 点下去仍可能失败 |
|---|---|---|
| 门禁 / Shadow | 有 `govern.gate` / `govern.start_shadow` 且 Bindings 覆盖该 release 的资产 | 状态不对 |
| 晋升金丝雀 / 强制 | 有对应 `promote_*`，Bindings 覆盖，且 `user.userId ≠ release.createdBy` | 门槛不够（`GateResult`）；自动通道已排除提案人单元数据 |
| 回滚 / 退休 | 有 `govern.rollback` / `govern.retire` | 状态不对 |

提案人看到推进按钮应禁用，旁注「须由其他持权用户推进」。

### 17.4.2 授予页

- 主体选一个已有用户，**不能选自己**。
- Tools 用 §6.1 多选；Bindings 从 `ListAssets`（调用者可见集合）勾选，禁止手填 `*`。
- 保存走 `PutGrant`；`grant_self` / `grant_wildcard` / `grant_scope` 原文展示 reason。
- 创建用户后停留在授予页补第一条，否则新用户读列表也是空的。

### 17.4.3 控制台响应式、移动壳层与模型网关布局

- 控制台不得依赖操作系统或浏览器用户代理分支布局。字体使用覆盖 macOS、Windows 与 Linux 的系统字体栈；数字、时间和标识使用等宽后备与等宽数字，避免平台字形宽度差异造成跳列。
- 页面在 320 CSS 像素宽度起不得产生整页横向滚动。桌面保留可折叠分组导航；641 至 1024 CSS 像素使用收窄侧栏；640 CSS 像素及以下改用带设备顶部安全区的顶栏和全屏导航抽屉。抽屉一次展示总览、安全运营、记录和获授权的系统设置入口，并提供关闭按钮、当前页面提示、焦点陷阱和 Escape 关闭能力；禁止把桌面侧栏横向摊平成滚动条。
- `/app/agent` 在内容空间足够时同时显示资产拓扑和贾维斯会话；视口 1100 CSS 像素及以下改成“资产拓扑 / 贾维斯”互斥单视图，避免桌面侧栏、拓扑和固定对话列共同挤压。切换控件使用页签语义且不得销毁共享会话。Agent 工具栏允许在自身内部水平滚动，案件列表和编辑弹窗不得超出动态视口；移动端编辑弹窗贴合安全区并把主要操作保持在可触达区域。
- 全局贾维斯在宽屏是右下浮窗，在 640 CSS 像素及以下是覆盖主内容的全屏对话层；关闭按钮、消息区和输入区分别固定在层内的头部、可滚动主体与底部。输入区必须适配动态视口、软键盘和设备安全区。对话层打开时背景不可继续滚动，关闭后恢复原状态。
- 页面、面板和网格子项都必须允许收缩。安全区、动态视口高度、触屏最小 44 CSS 像素命中区、触摸滚动和移动浏览器输入缩放须由标准 CSS 处理，禁止平台探测；键盘焦点不得被固定导航、对话层或弹窗遮挡。
- `/app/model` 的四个状态卡在宽屏等宽四列、中屏两列、窄屏单列；时间戳不得逐段断行。配置表单在宽屏使用对齐的两列字段，端点、密钥、反馈与操作跨满整行，窄屏退成单列且操作按钮占满可用宽度。
- 接入主机表在面板内部承担横向滚动，不能把页面或侧栏撑宽；数值列右对齐，时间与主机使用稳定的等宽显示。滚动容器必须可获得键盘焦点并带可读标签。
- 响应式验收至少覆盖 1280×720、1024×768、820×900、390×844 与 320×568；每个视口检查整页 `scrollWidth <= innerWidth`、导航与对话不遮住内容、Agent 两种单视图可达、表单无重叠、按钮可操作、表格溢出被限制在自身容器。桌面键盘与触屏均不得依赖悬停才能完成配置。

### 17.5 轮询与分页约定

| 场景 | 建议 |
|---|---|
| 事件流近实时刷新 | 每 2–5 秒 `ListEvents`，带上一页 `next_page_token` 只拉增量；游标过期时重拉第一页 |
| 发布状态可视化 | 每 5 秒 `GetRelease` 或 `ListReleases`，状态变化时展示 timeline |
| Dashboard | 每 10 秒刷新 |
| 写操作 | 先取 `GetRelease` 展示门槛（`GetReleaseStats` + 配置公开的 required 值），再调推进 RPC；错误 details 中的 GateResult 直接渲染到按钮旁 |

### 17.6 JSON 字段名对照示例

| proto 字段 | JSON 字段 |
|---|---|
| release_id | releaseId |
| page_size | pageSize |
| next_page_token | nextPageToken |
| occurred_at | occurredAt |
| canary_percent | canaryPercent |
| release_traces | releaseTraces |

### 17.7 错误处理

- 一律先读响应体 `code`，再按 §13 处理。
- `failed_precondition` 的 `details` 是结构化 proto，不是给用户直读的文本；前端把 `GateResult.gates` 渲染为“未满足项 + 当前值/要求值”。
- 写操作必须生成并复用 `Idempotency-Key`（例如 `crypto.randomUUID()`），重试保持同键。

### 17.8 当前客户端边界

网络 proto service 与 Go 生成代码已经存在，TypeScript 的 Connect-ES 生成流程尚未配置。当前 `ConsoleClient` 是页面边界，手写 `ConnectClient` 是唯一运行时实现；后续生成代码落地时只替换其内部，不改页面。

页面使用的 `ConsoleClient` 以 `console/src/api/client.ts` 为准，必须包含：认证（Login/GetMe 带 `access`）、资产/事件/发布/审计、治理写、以及 `listGrants` / `putGrant` / `revokeGrant`。示例里的三方法子集已过时。

过渡适配层的请求/响应类型从本文档表格翻译为 TypeScript 时注意：proto `int64` 在 JSON 中是字符串；`Timestamp` 是 RFC 3339 字符串；`Duration` 是 `"300s"` 样式的字符串；`bytes` 是 base64 字符串。

### 17.9 引导未完成时的路由

- 浏览器地址只许 `https://127.0.0.1:9050/app/setup`（SPA basename `/app`，路由 `/setup`）。禁止再写一套不带 `/app` 的交付 URL。
- `state != ONBOARDING_STATE_COMPLETED` 时：管理员整站只渲染 `/app/setup`，**不进入主壳**（态势/事件/发布/用户管理页）。本页只配置模型网关、探测连通性、确认贾维斯在线并显式结束引导。
- 其他角色只渲染「等待管理员完成初次配置」。
- 未完成时除 §19.5 白名单外，`Dashboard` / `ListEvents` / `ListReleases` / `GetRelease` / `ListAuditEntries` 一律 `failed_precondition` + `reason=onboarding_incomplete`（不是只靠前端藏路由）。
- `/app/setup` 不渲染 Edge、ModelSide、Host、防御资产、批准账户、Docker 命令或服务管理命令；这些能力只存在于完成引导后的主控制台与部署手册。
- 密钥输入框不得回填明文。`GetOnboarding` 返回字段见 §19.2。

---

## 19. 初次配置引导（OnboardingService）

> **§19 专指本引导契约。** 字段级契约维护规则改称 §20。第 18 节仍是 Agent 控制面。阅读顺序：§17 → 本节 → §18。

本节是控制面初次配置的网络契约。名词见 [glossary.md](glossary.md#onboarding)。数据面的人工接入属于 §9 资产域常规流程，不得重新塞回本状态机。

**库不变量**：全库恰好一行；主键固定为 `id = 1`。并发写 RPC 用行锁；失败写 `state=ONBOARDING_STATE_FAILED` 与 `last_error`，不删除已存密钥。

线上状态与 JSON **只许 proto 枚举全名**（禁止用 `pending` / `completed` 短名当契约）。

合法前进边：`ONBOARDING_STATE_PENDING` → `ONBOARDING_STATE_MODEL_CONFIGURED` → `ONBOARDING_STATE_MODEL_LIVE` → `ONBOARDING_STATE_COMPLETED`。任一步写失败 → `ONBOARDING_STATE_FAILED`，保留已经写入的凭据槽。历史 `ONBOARDING_STATE_EDGE_LIVE` 只保留线缆和旧库兼容；服务端读取时按 `ONBOARDING_STATE_MODEL_LIVE` 投影，禁止新写入。

从 `ONBOARDING_STATE_FAILED` 出发的合法边（同页重试，不是重置）：

| RPC | 前置 | 成功后状态 |
|---|---|---|
| `PutModelConfig` | 无额外前置 | `ONBOARDING_STATE_MODEL_CONFIGURED` |
| `TestModelConnectivity` | `has_secret=true` | `ONBOARDING_STATE_MODEL_LIVE` |
| `CompleteOnboarding` | 仍只认 §19.1 两条 | `ONBOARDING_STATE_COMPLETED` |

禁止从 `ONBOARDING_STATE_COMPLETED` 退回（本档无重置 RPC）。

### 19.0 界面动作（闭集）

浏览器只打开 `https://127.0.0.1:9050/app/setup`。界面按顺序完成四个动作：

1. `PutModelConfig` 把端点、模型、方言和只写密钥保存到 brain 凭据槽；
2. `TestModelConnectivity` 从 brain 发出最小补全并显示成功或可重试错误；
3. 轮询 `GetOnboarding.jarvis_online`，等待技术人员已经安装的贾维斯主动注册或轮询；Brain 不反向连接，也不在本页启动进程；
4. 管理员点击“进入控制台”，调用 `CompleteOnboarding`。成功后进入 `/app`，再在资产页登记第一项资产和 Edge 人工接入配置。

模型探测失败不得提供绕过完成谓词的跳过按钮。资产、Edge、Host、ModelSide 和批准账户均可在主控制台开放后独立配置，任一项缺失都不阻止进入主控制台。

### 19.1 `ONBOARDING_STATE_COMPLETED` 谓词（唯一）

`CompleteOnboarding` 必须同时满足下列两条，否则 `failed_precondition`，`details` 恰好一条 `type.googleapis.com/yufeng.onboarding.v1.OnboardingGate`（`missing_predicates` 列出未满足编号 1–2，升序去重），状态不变：

1. 最近一次 `TestModelConnectivity` 成功，且之后密钥未改；
2. 配置项 `-jarvis-agent-id`（默认 `jarvis-1`）已注册，且最近一次 `Heartbeat` **或** `PollInstructions` 落在 `JarvisOnlineWindow` 内（即 `jarvis_online=true`，与 §19.2 同一判定）。该谓词只证明研判编排进程在线，不授予部署能力。

通过后服务端给引导管理员写入系统授予：Tools = `grant.write`、`user.admin`、`catalog.manage`、`console.read`、`asset.create`、`asset.update`、`asset.delete`、`asset.attach`、`asset.detach`（**不含** `govern.propose` / `govern.promote_*`），Bindings = 完成时库中全部资产 ID，零资产时允许为空。其后 `CreateAsset` 作为管理员全局工具创建第一项资产，并自动把新 ID 加入创建者范围。不得改写其它账户、资产、授予或历史检测数据。

引导完成前贾维斯令牌不得含 `govern.propose` / `govern.promote_*`。贾维斯能力闭集只覆盖安全研判与治理建议；部署、进程管理、容器管理、Edge 安装、基线签发和 Edge 探测工具一律不得注册或签发。

### 19.2 RPC

| RPC | 认证 | 写 | 行为 |
|---|---|---|---|
| `GetOnboarding` | 任意登录用户 | 否 | 返回 `state`（枚举全名）、`base_url`、`model`、`dialect`（枚举全名，缺省 `MODEL_DIALECT_OPENAI_CHAT`）、`has_secret`、`secret_hint`、`jarvis_online`、`last_error`、`updated_at`。无密钥明文。旧 Edge 引导字段继续保留线缆编号但标记退役，服务端返回空值或零，控制台不得读取 |
| `PutModelConfig` | 仅 `USER_ROLE_ADMIN` | 是，`Idempotency-Key` | `base_url` 必须是绝对 HTTPS URL。`dialect` 省略或 `MODEL_DIALECT_UNSPECIFIED` 则写入 `MODEL_DIALECT_OPENAI_CHAT`；只许 §19.4 三种方言。密钥**只**写入凭据槽。`YUFENG_MODEL_API_KEY` 不是第二份权威密钥：仅供人机交付活栈 / CI **脚本**读出后填进本 RPC 的 `secret` 字段；brain 补全路径只读槽，不读该环境变量。成功 → `ONBOARDING_STATE_MODEL_CONFIGURED`（覆盖旧密钥必须重新探测） |
| `TestModelConnectivity` | 仅管理员 | 是，`Idempotency-Key` | **brain 模型网关**用凭据槽密钥按槽方言发一次最小补全。模型 HTTP **不得**握着引导行 `FOR UPDATE`：锁内取槽快照 → 锁外出网 → 重新加锁核对槽未变再提交。HTTP 成功且非空文本 → `ONBOARDING_STATE_MODEL_LIVE`。失败 → `ONBOARDING_STATE_FAILED` + `last_error`（英文小写）+ Connect `unavailable` 或 `failed_precondition`。探测期间槽被改写 → `aborted`，不把过期结果写成 `MODEL_LIVE`。本档不跑评测集。可重试本 RPC，不必重新 `PutModelConfig`（除非改密钥或方言） |
| `PutDeploymentSpecification` | 兼容线缆 | 否 | 方法和旧消息编号保留并标记退役；服务端固定返回 `unimplemented`。控制台、正式脚本和新客户端不得调用；等价的新行为只存在于资产域 `PutEdgeEnrollment` |
| `CompleteOnboarding` | 仅管理员 | 是，`Idempotency-Key` | 只检查 §19.1 两条。通过 → `ONBOARDING_STATE_COMPLETED` 并写入管理员系统授予（见 §19.1 末段）；失败 `failed_precondition`，`details` **恰好一条** `type.googleapis.com/yufeng.onboarding.v1.OnboardingGate`，字段 `missing_predicates` 为 `repeated int32`（取值 1–2，升序去重），状态不变，不写系统授予 |

`base_url` 指向公网模型端点**仅允许从 brain 模型网关出网**。贾维斯、edge、浏览器不得持该密钥。Edge 邻近 ModelSide 只装载签名模型档案和本地权重，不读取聊天凭据槽；其跨主机入口必须位于受控防御网络并使用相互传输层安全协议，禁止默认公网。

### 19.5 引导期白名单 RPC

`state != ONBOARDING_STATE_COMPLETED` 时，操作域只许：

- 公开/会话：`Login`、`Logout`、`GetMe`、`GetLoginConfig`、`ChangePassword`
- 引导：`GetOnboarding`、`PutModelConfig`、`TestModelConnectivity`、`CompleteOnboarding`

其它操作域 RPC（含 `Dashboard`、`ListEvents`、`ListReleases`、`GetRelease`、`ListAuditEntries`、`ProposeArtifact`）→ `failed_precondition` + `reason=onboarding_incomplete`。

### 19.3 数据面人工生命周期

数据面只能在主控制台中先 `CreateAsset`，再调用 §9 的 `PutEdgeEnrollment`。该资产域远程过程调用完成规范化、预声明绑定、监听计划签发和资产世代签发；它不调用工具网关，不创建 Agent 指令，不访问 Docker 套接字、进程管理器、Edge 管理口或 ModelSide。

技术人员从受信任交付物中选择一种方式安装：

- 原生部署：安装 `yufeng-edge` Go 二进制和可选的 `yufeng-modelside` Python 服务包，由操作系统服务管理器显式启动、升级、回滚和卸载；
- 容器部署：使用仓库提供的 Compose 配置，可以将 Brain、Edge 与 ModelSide 同置，也可以只部署连接远端 Brain 的 Edge 与 ModelSide；容器的创建、重建和删除仍由技术人员执行；
- 分离部署：Edge 与 ModelSide 可以作为独立进程、独立容器或独立主机运行。同机优先使用 Unix 域套接字；跨主机必须使用相互传输层安全协议认证，并限制在同一受控防御网络。

Edge 首次启动后主动调用 `Register`，随后拉取已签名监听计划、资产世代和检测策略，并通过 `Heartbeat` 回执已装载版本。Brain 只根据这些主动请求与回执计算每项 `EdgeEnrollment.status`；不存在全局 `onboarding.edge_ready`。升级与卸载不会触发 Brain 或 Jarvis 的进程动作；短暂离线仅使该接入状态变为离线，签名制品和审计记录保留。

贾维斯只负责安全研判和治理建议。正式工具注册表不得出现 Edge 安装、容器部署、进程启动、基线发布或 Edge 探测能力，也不得为部署创建 Agent 指令。

旧 `DeployDataplane` 与 `PutDeploymentSpecification` 远程过程调用及其消息只为保持已经发布的 Protocol Buffers 线缆编号兼容而标记退役；服务端固定返回 `unimplemented`，控制台、脚本和正式客户端不得调用。它们不构成部署能力。历史 `deployment_onboarding` 中存在的有效 Edge 规格由数据库迁移无损复制到 `edge_enrollments`，旧行保留但不再作为注册或控制台状态权威。

### 19.4 与模型网关的关系

模型出网仍是**一条**引导凭据槽：同时只接入一个 `base_url` + `model` + `dialect` + 密钥。本档不提供多供应商并行路由或负载均衡。生产贾维斯只调 §18.10 的 `Generate`；`CompleteChat` 仅保留迁移兼容与槽连通性探测。两者都只连 brain，供应商 HTTP 方言只在 brain 出网时展开。

`dialect` 线上只许 proto 枚举全名：

| 方言 | 出网 | 鉴权 | 文本抽取 |
|---|---|---|---|
| `MODEL_DIALECT_OPENAI_CHAT`（缺省） | `POST {base_url}/chat/completions` | `Authorization: Bearer` | `choices[0].message.content` |
| `MODEL_DIALECT_OPENAI_RESPONSES` | `POST {base_url}/responses` | `Authorization: Bearer` | `output_text`，否则拼接 `output[].content[].text` |
| `MODEL_DIALECT_CLAUDE_MESSAGES` | `POST {base_url}/messages` | `x-api-key` + `anthropic-version: 2023-06-01` | 拼接 `content[].text`（`type=text`） |

`base_url` 含版本前缀（如 `https://api.x.ai/v1`、`https://api.anthropic.com/v1`），brain 只再追加上表路径。`MODEL_DIALECT_UNSPECIFIED` 与空列按 `MODEL_DIALECT_OPENAI_CHAT` 解释。未知方言 → `invalid_argument`（写入）或 `failed_precondition`（出网）。Claude 方言把 `role=system` 的消息合并进请求体 `system`，`messages` 只含 `user`/`assistant`；若合并后没有 `user` 消息 → `failed_precondition`。现有 `CompleteChat` 只回收非空文本，不把供应商 `tool_calls` / `tool_use` 回写给贾维斯；因此它**不能**充当统一座架 Generate。`Generate` 的方言适配器必须把文本与供应商工具调用归一为有序 `output_items[]`，保存供应商调用标识但由 brain 另发平台 `call_id`。

- 生产 `yufeng-jarvis` **禁止** `-model-url` 旗标（含指向 brain、内网或公网）。贾维斯连 brain 只用它已有的控制面地址（与 `PollInstructions` 同一 `{BASE}`）。
- 迁移补全 RPC：`POST {BASE}/yufeng.model.v1.ModelGatewayService/CompleteChat`。认证：贾维斯 **Agent 域** `access_token`（与 `PollInstructions` 同一张）。请求：`messages[]`（`role` + `content`）。响应：`text`（非空才算成功）。它没有 Turn、租约、ContextManifest、工具调用或权威预算语义，新座架禁止调用。brain 用引导凭据槽按 `dialect` 出网；**不**读 `YUFENG_MODEL_API_KEY`。未完成引导或槽空 → `failed_precondition`。`CompleteChat` 与每个 `Generate` attempt 都追加调用记录（主机、模型名、是否成功、耗时、usage/成本可得值、小写英文错误；**无密钥**）。
- 引导未完成时改槽仍走 `PutModelConfig` / `TestModelConnectivity`（§19.2），成功会推进引导状态。
- 引导完成后改槽**不得**退回 `ONBOARDING_STATE_*`，也不得再走 `PutModelConfig`（仍是非法边）。管理员改槽与看状态用下列 RPC（均仅 `USER_ROLE_ADMIN`，不查授予表）：

| RPC | 写 | 行为 |
|---|---|---|
| `GetModelGateway` | 否 | 返回当前槽投影（`base_url`、`model`、`dialect`、`has_secret`、`secret_hint`，无密钥明文）、`status`（枚举全名）、`provider_count`（统计窗内出现过的不同主机数，当前槽主机若已配置则计入）、窗长 `window_seconds`（实现引用 `ModelGatewayStatsWindow`）、窗内 `calls_total` / `calls_ok`、`last_call_at`、`last_error`、`providers[]`（`host`、`calls_total`、`calls_ok`、`last_at`）。未完成引导 → `failed_precondition` |
| `UpdateModelGateway` | 是，`Idempotency-Key` | 仅 `ONBOARDING_STATE_COMPLETED`。`base_url` 必须是绝对 HTTPS URL。`secret` 为空则保留旧钥，非空则覆盖槽。`model` 空则保留旧名（旧名也空则用 `DefaultChatModel`）。`dialect` 为 `MODEL_DIALECT_UNSPECIFIED` 则保留旧方言（旧列空则 `MODEL_DIALECT_OPENAI_CHAT`）。**不**改引导状态。未完成引导 → `failed_precondition`（去 `/app/setup`） |
| `ProbeModelGateway` | 是 | 仅已完成引导且 `has_secret=true`。brain 用槽钥按槽方言发一次最小补全，记入调用记录。成功/失败都**不**改 `deployment_onboarding.state`。失败回 `last_error`（英文小写）与 Connect `unavailable` 或 `failed_precondition` |

`status` 线上只许 proto 枚举全名：`MODEL_GATEWAY_STATUS_UNCONFIGURED`（无钥或无 `base_url`）、`MODEL_GATEWAY_STATUS_READY`（已配置、窗内无调用）、`MODEL_GATEWAY_STATUS_LIVE`（窗内有调用且全部成功）、`MODEL_GATEWAY_STATUS_DEGRADED`（窗内有成功也有失败）、`MODEL_GATEWAY_STATUS_DOWN`（窗内有调用且全部失败）。

控制台页面 `/app/model` 只给管理员：展示接入主机数、窗内成功率与各主机状态，并允许改端点、模型、密钥与探测。非管理员不得改模型。

- `agents/modelgateway` 的确定性剧本只存在于测试编译单元，不进入任何交付二进制。`-dev-insecure` 只放宽本地传输；未配置 `-model-url` 时仍调用中台持久 `Generate`，不得回退到固定答案。
- 贾维斯提交 `Generate` 时不携带或覆盖模型名；实际模型只取当前中台模型槽。运行时不得用 `fake`、测试模型名或本地默认值污染正式请求。
- 默认 `base_url=https://api.x.ai/v1`，默认方言 `MODEL_DIALECT_OPENAI_CHAT`。`model` 未填时服务端使用 `lib/kernel.DefaultChatModel`（当前冻结值 `grok-4-1-fast-non-reasoning`）。引导页与模型网关页可改成该方言接受的模型名。人机交付活栈脚本必须传同一常量，禁止另写模型名。
- 确定性测试提供者只允许存在于 `_test.go` 测试编译单元，不得进入 compose 服务命令行或交付二进制。
- 异步流量推理不进本槽、不使用聊天模型网关。它由签名 `ModelProfile` 和 §21.5 的 Edge 邻近 `yufeng-modelside` 执行；Brain 不接收原始流量，也不反向拨号 ModelSide。聊天生成与流量推理分别认证、记账和限额。
- 模型评测集、对抗语料、延迟 SLO 对比**不在本档**。

---

## 18. Agent 控制面契约（现行语义与扩展约束）

**原则**：`yufeng-jarvis` 与认知型 `yufeng-run` 共用一种逻辑认知循环、账本和工具语义；网络身份不同。贾维斯主动长轮询 brain，`yufeng-agentd` 代表 run 主动长轮询 brain，`yufeng-run` 只连接 agentd 的本地监督代理且没有网络对等体。brain 负责排队、授权、审计、模型出网与工具执行；**后端永不向 Agent 拨号**。对中台的写只有两条路：经 `ToolGatewayService.InvokeTool`，或持能力令牌调用 **Tools 已列出** 且 Scopes 允许的受控应用程序编程接口（API）；Scopes 不得单独放出 Tools 里没有的写动作。

本节记录现有接口及其扩展约束。新增字段必须先按第 20 节同步 Protocol Buffers 消息契约与生成代码；不得用临时 JSON、环境变量或第四条队列绕过。实现状态只看 [`development/code-map.md`](development/code-map.md)，不能因本节写入目标语义就宣称已交付。

### 18.1 AgentControlService（贾维斯；RUN_SUPERVISOR 代表 run 推进 Turn）

| RPC | 认证 | 说明 |
|---|---|---|
| RegisterAgent | bootstrap_token | 绑定 `agent_id` 的一次性引导令牌注册；请求 `agent_id`、`agent_public_key`、能力标签；响应 `refresh_token`、`access_token`、`expires_in`。请求 `agent_id` 与绑定值不同 → `permission_denied` 且不消耗令牌。生产配置拒绝未绑定的部署级共享令牌（见 §0.5 与 §18.8 第 1 条） |
| RefreshAccessToken | refresh_token | 用 refresh_token 换短时 access_token；refresh 可轮换并吊销旧值 |
| PollInstructions | access_token | 长轮询指令；请求 `agent_id`、`long_poll_seconds`（默认 `AgentLongPollDefault`=30s，上限 `AgentLongPollMax`=60s；省略或 0 按默认；超过上限 `invalid_argument`）、`max_instructions`（默认 1，上限 8）；每条返回项各自携带 `lease_id`、`lease_epoch`、`lease_deadline` 与能力令牌 |
| AckInstruction | access_token | 只关闭已达 Turn 终态的指令；请求 `instruction_id`、`lease_id`、`lease_epoch`、`status`、`result_ref`、`error`。不得用 Ack 表示等待或暂停。`CASE_REVIEW` 因网络或服务瞬时失败回执 `failed` 时，Brain 在同一指令记录上有界退避重试，每次重领换租约并吊销旧能力；超过上限才收敛为案件 `FAILED` |
| Heartbeat | access_token | Agent 进程心跳与版本/负载摘要 |
| OpenTurn | access_token + capability_token | 以当前 `instruction_id/work_id + lease_id + lease_epoch + expected_item_sequence` 打开或恢复 Turn；返回钉死输入、checkpoint、下一个执行序号和预算投影 |
| GetTurn / ListTurnItems | access_token + capability_token | 读取当前能力令牌覆盖的 Turn 与有序 Item；不得返回模型密钥、隐藏推理或其它主体内容 |
| ExtendInstructionLease | access_token + capability_token | 正常续租只延长 `lease_deadline`，保持 `lease_id` / `lease_epoch`；可返回引用同一 epoch / `budget_id` 的新能力令牌，旧令牌在原 `exp` 前仍可完成已在途请求，释放或重新领取时吊销 |
| YieldTurn | access_token + capability_token | 在同一事务写 checkpoint、转入 `WAITING_*`、释放租约并吊销该租约能力令牌；等待不使用 Ack |
| CompleteTurn | access_token + capability_token | 以条件写入转入 `COMPLETED` / `FAILED` / `CANCELLED` / `OUTCOME_UNKNOWN`；成功后才允许 Ack |
| RequestUserInput | access_token + capability_token | 写待回答问题并转入 `WAITING_INPUT` 后 Yield；这是问用户，不是审批 |

`RegisterAgent` / `RefreshAccessToken` / `PollInstructions` / `Heartbeat` 只发行并接受 Agent 身份，生产不再给 agentd 发 Agent 身份。agentd 通过 §18.3 `RegisterWorkerIdentity` 获得 `aud=worker, worker_kind=RUN_SUPERVISOR` 的 workload access token；它代表 run 调用上述 Turn 推进接口时，工作负载访问令牌 `sub=worker_id`，工作项能力令牌 `sub=run_id`、`azp=worker_id`，并由 `work_id` 解析 Turn。贾维斯则是 Agent 访问令牌 `sub=agent_id`、指令能力令牌 `sub=agent_id`、`azp=agent_id`。两条路径都必须满足双令牌绑定，不能让 run 自报 worker 或父 Turn。旧 Agent access 仅由 §18.3 的显式开发兼容开关接受，不得进入生产或扩给 ModelSide。

上述标为双令牌的 RPC 与 ToolGateway 使用同一传输头：`Authorization: Bearer <access_token>` + `X-Yufeng-Capability: Bearer <capability_token>`。令牌不得进入消息字段、checkpoint 或模型上下文。

`AgentInstruction` 目标字段：`instruction_id`、`agent_id`、`kind`（当前可入队闭集只有 `SESSION_MESSAGE` / `EVENT_TRIAGE` / `CASE_REVIEW`）、`payload_ref`、`turn_id`、`budget_id`、`capability_token`、`created_at`、`deadline`、`lease_id`、`lease_epoch`、`lease_deadline`。其它 proto 枚举不属于当前可入队集合，不能被调用方当成扩展点。对于认知指令，`payload_ref` 必须等于 `turn_id`；来源游标只在 Turn 中保存。

语义：brain 把会话消息、事件通知写进**指定 `agent_id`** 的持久指令队列。本档**不得**入队 `APPROVAL_REQUEST` / `PLAN_REQUEST` / `SUPERVISE_RUN` / `SHUTDOWN`。`PollInstructions` / `AckInstruction` 只把访问令牌 `sub` 当作领取者，请求里的 `agent_id` 不作授权；只能领 `instruction.agent_id == sub` 的条目。初次领取及释放后的重新领取生成新 `lease_id`、递增 `lease_epoch`、签发引用同一 `budget_id` 的新能力令牌，并吊销旧租约令牌；同一持有者正常续租不增加 epoch。`capability_token` 只出现在领取响应里；目标上信封按该 Agent 注册公钥加密。无注册公钥则不得入队。编排会话签发的能力令牌不得含治理写工具。

第一阶段演示实现 `SESSION_MESSAGE` 与 `EVENT_TRIAGE` 的入队与领取；生产流量案件另使用 `CASE_REVIEW`。其它指令种类不得入队。人主动要求补丁不在本档交付，不得用会话正文或未冻结的计划请求冒充；若以后需要，须先在本文冻结操作域远程过程调用（RPC）再实现。入队谓词、令牌工具集与提案身份见 §18.1.1。

### 18.1.1 第一阶段：漏拦叫醒与介入拦截（演示，已冻结）

本节冻结“智能代理介入虚拟补丁流量拦截层”的演示网络语义。实现必须与此一致，未写明的行为不得自行发明。本节谓词**只服务演示**：把 `verdict=allow` 且无在途 `KIND_RULE` 当作叫醒条件，并允许贾维斯提出 `rules/v1` 正则、门槛为零时自动全量生效。

生产不得沿用本节的入队谓词、任意正则和自动晋升形状规则。生产目标见 §18.1.2；那是对演示语义的安全收紧。确定性提供者只在测试编译单元验证本节演示，不属于开发或生产运行模式；单站点企业试点中的贾维斯禁止使用 `-model-url`，模型调用统一由中台模型网关出站。

**介入的含义。** Agent 不在数据路径上裁决放行或拦截。介入仅指：中台按本节签发 `EVENT_TRIAGE`，贾维斯经工具网关提出 `KIND_RULE` 制品并走门禁与 shadow；生效（canary / enforce）只来自门槛调度或另一主体的 `promote_*`。用户会话正文不是命令，不得升级为带 `govern.*` 的指令。

**`EVENT_TRIAGE` 入队。** 投递目标为服务端配置的 `JarvisAgentID`（与 `SESSION_MESSAGE` 同一白名单）。仅当 `UploadEvents` 将本条记为 `accepted`（新插入，非 `deduped` / `rejected`）且下列谓词全部为真时入队一条；任一为假则静默跳过，不得用错误响应表示「未叫醒」。

1. `verdict == allow`。`observe` / `block` / `escalate` 不入队：已有制品在观察或已拦截，分别走自动晋升 / 人手 `promote_*` / 既有拦截，不重复提案。
2. 事件携带 HTTP 容器（`traffic.http` 有 `method` 与 `path`）。缺少则无法生成第一阶段规则制品，不入队。
3. 不存在未退休发布同时满足：`kind == KIND_RULE`；`scope.asset_ids` 含本事件 `asset_id`；`scope.route_selector` 为空，或为本事件 `path` 的前缀。有则视为该路径已有在途或已生效虚拟补丁；更新走 `supersedes`，不由第二条 `EVENT_TRIAGE` 发起。
4. 不存在 `status ∈ {pending, leased}` 的 `EVENT_TRIAGE`，其 Bindings 含同一 `asset_id`，且源事件（`payload_ref` 指向的事件）的 `method` 与 `path` 与本事件相同。
5. 该 `event_id` 未曾作为 `EVENT_TRIAGE.payload_ref` 入队。

`payload_ref` 必须等于该 `event_id`。无目标 Agent 注册公钥则不得入队（与 §18.1 总则相同），本条事件仍为 `accepted`。

**`SESSION_MESSAGE` 入队。** `SendMessage` 在同一数据库事务内：写入会话消息并取得 `message_sequence` → 找到或创建该 `session_id` 对应的 `AgentThread` → 创建钉死该序号的 `AgentTurn` → 入队恰好一条 `SESSION_MESSAGE`。`payload_ref` 与 `turn_id` 均为新 Turn 的标识；不得再以 `session_id` 让恢复方读取“最新消息”。也可使用可靠事务发件箱完成后半段，但不得出现消息已保存而 Turn / 指令永久缺失。禁止根据消息正文、模型输出或启发式改发 `EVENT_TRIAGE`、`PLAN_REQUEST` 或任何 Tools 含 `govern.*` 的指令。

**指令能力令牌。** 领取时签发；Tools 与 Bindings 只能是下表，不得从请求体或模型输出扩权。

| `kind` | 允许的 Tools | Bindings | 禁止 |
|---|---|---|---|
| `SESSION_MESSAGE` | `session.reply` | 该 `session_id` | 一切 `govern.*`、`event.get` / `event.list` / `release.list`、`run.create` |
| `EVENT_TRIAGE` | `event.get`、`event.list`、`release.list`、`govern.propose`、`govern.gate`、`govern.start_shadow` | 该事件的 `asset_id`（`asset:<id>`） | 一切 `govern.promote_*`、`govern.rollback`、`govern.retire`、`session.reply` |

`session.reply` 只写回已有会话，第一阶段研判不自动开会话；研判的人机可见面是 `created_by = agent_id` 的 release 与审计条目。

**工具网关（本阶段最小集）。** 名称与操作域授予表（§6.1）中的 `govern.*` 相同，实现为对既有治理 RPC 的代理；`event.get` / `session.reply` 仅为 Agent 工具，不进入用户授予表。`args_json` 必须通过对应 JSON Schema，非法返回 `invalid_argument`。Bindings 外对象一律 `permission_denied`（不区分是否存在）。

| 工具 | `args_json` 要点 | 服务端行为 |
|---|---|---|
| `ticket.get` | `event_id`、`ticket_digest` | 只从不可变 `check_tickets` 读取字段级脱敏投影；事件资产必须落在令牌 Bindings 内，摘要必须与冻结行完全一致 |
| `event.get` | `event_id` | 返回该事件；其 `asset_id` 必须落在令牌 Bindings 内 |
| `event.list` | 可选 `asset_id`、`verdict`、上限 ≤ 50 | 只返回 Bindings 覆盖的事件 |
| `release.list` | 可选 `asset_id`、状态过滤、上限 ≤ 20 | 只返回 Bindings 覆盖的发布 |
| `session.reply` | `session_id`、`content`（非空，UTF-8 最长 8192 字节） | 以该 Agent 身份追加会话消息；`sender` 由服务端取能力令牌 `sub`；`session_id` 必须等于令牌绑定的会话。`SendMessage.content` 同一上限，超限 `invalid_argument` |
| `govern.propose` | `kind`、`payload`、`payload_schema`、`scope`、`ttl`（可选 `supersedes`） | 等价 `ProposeArtifact`。第一阶段 `kind` 仅 `KIND_RULE`，`payload_schema` 仅 `rules/v1`。`scope.asset_ids` 必须非空且 ⊆ 令牌 Bindings。`created_by` 及请求里任何身份字段忽略，写入能力令牌 `sub`（`agent_id`） |
| `govern.gate` | `release_id`，可选 `corpus_ref` / `budget` | 等价 `GateArtifact`；该 release 的 `scope.asset_ids` 必须 ⊆ 令牌 Bindings |
| `govern.start_shadow` | `release_id` | 等价 `StartShadow`；Bindings 约束同上 |

会话令牌调用任一 `govern.*` 必须 `permission_denied`，且不得写发布、扣预算或写幂等成功。`EVENT_TRIAGE` 令牌调用 `govern.promote_canary` / `govern.promote_enforce` 同样 `permission_denied`。

**提案主体与晋升。** `ProposeArtifact`（含工具代理）的 `created_by` 只由服务端从认证身份写入：操作域为用户 `user_id`，工具网关为能力令牌 `sub`。职责分离按提案主体，不按「是否人类」：`created_by` 与调用者标识相同则不得 `promote_*`。贾维斯是提案人时，其 `EVENT_TRIAGE` 令牌本就不含 `promote_*`。

自动晋升的排除集仍是「该 release **提案人创建或绑定的单元**」上报的心跳与事件（§5.5 / §7.5）。提案人为 Agent、且该 `agent_id` 未创建或绑定任何单元时，排除集为空，真实流量计数可以满足门槛并自动 canary / enforce。这是 T1（可逆 L1、短存活时限、门槛自动推进），不是漏洞。人手 `promote_*` 仅在排除后门槛不够时由**另一用户**持对应工具执行。

调度器推进不是「模型提案作为晋升输入」：模型最多把发布推进到 shadow；canary / enforce 只认门槛或持 `promote_*` 且 `user_id ≠ created_by` 的用户。

### 18.1.2 生产级 L1：研判、策略与世代（已冻结）

本节是 L1 生产关闭的网络与状态语义。研判结论已由 `TriageDecision` 表达，生产工具网关实现 `triage.get` / `triage.complete`；不得退回只按攻击类匹配的弱策略版本。

**proto 字段对照（无占位）**

| 概念 | proto |
|---|---|
| 检测键 | `yufeng.common.v1.DetectionKey` |
| 覆盖度 | `yufeng.common.v1.InspectionCoverage` |
| 边缘观察 / 研判 | `ObservationState` / `TriageReason` |
| 世代信封 | `yufeng.artifact.v1.AssetGeneration`（成员仍是 `ReleaseItem`） |
| 策略候选 | `yufeng.artifact.v1.PolicyCandidate`（`KIND_POLICY` / `policy/v1`） |
| 提案意图 | `yufeng.govern.v1.ProposalIntent` |
| Agent 研判结论 | `yufeng.agent.v1.TriageDecision` |
| 复核 / 硬过期 | `Artifact.review_at` / `hard_expires_at` / `expiry_behavior` |
| 范围与证据 | `Artifact.scope_risk` / `evidence_class` |
| HTTP 检查配置档 | `yufeng.artifact.v1.HttpInspectionProfile` |
| 模型追加 | `yufeng.event.v1.ModelInference`（不改 `Event` 字节） |

**入队（收紧 §18.1.1）。** `EVENT_TRIAGE` 仅在事件 `accepted` 且中台研判原因为下列之一时入队；普通 `SYNC_NO_DETECTION` 不入队。

| 研判原因（线上 / JSON 只许 proto 全名） | 必要条件 |
|---|---|
| `TRIAGE_REASON_DETECTED_UNMITIGATED` | 同步存在检测键，当前资产世代无对应该键的 enforce 策略 |
| `TRIAGE_REASON_DETECTED_UNMAPPED` | 存在原始规则发现，但不属于自动治理五类；入队后默认只出 L0，不得自动晋升 |
| `TRIAGE_REASON_SUSPECTED_MISS` | 同步无发现，且具备下述一种独立证据 |

`TRIAGE_REASON_SUSPECTED_MISS` 的独立证据类型只认 `proto/yufeng/common/v1/v1.proto` 中的 `MissEvidenceType` 闭集：人工报告；漏洞回放或复现；附带请求复现的可信情报；同步无发现且达到已签名模型档案告警阈值的模型结果。模型分数、普通无发现、覆盖不足或检测器失败都不能脱离对应类型和可信账本记录自行构成漏检证据。上游应用防护、运行时防护、蜜罐或资产侧异常若要进入本闭集，必须先冻结为人工、复现或情报证据，不能新增临时字符串。

入队前必须按 `asset_id` + 路由模板 + 方法 + 检测键或漏检证据类型聚合（覆盖度与时间窗不进身份）；每个聚类至多一条未完成（`pending`/`leased`）指令。brain 创建或复用来源为该 `cluster_id` 的 AgentThread，再创建钉死 `cluster_version` 或 `event_cutoff` 的 AgentTurn；`payload_ref=turn_id`。无目标 Agent 注册公钥则不得入队，事件仍为 `accepted`。演示 §18.1.1 谓词只在测试或带 `yufeng_dev` 构建标签的目标中存在，正式构建不注册其开关。

**Agent 研判与提案分工。** 生产研判 Agent 先调用 `triage.get` 读取钉死投影，再只通过 `triage.complete` 提交非可信 `TriageDecision`：`cluster_id`、`disposition`、`rationale`、可选 `optional_shape_draft`。`disposition` 线上闭集为 `TRIAGE_DISPOSITION_PROPOSE_POLICY`、`TRIAGE_DISPOSITION_PROPOSE_SHAPE`、`TRIAGE_DISPOSITION_REPORT_ONLY`、`TRIAGE_DISPOSITION_ESCALATE_HUMAN`、`TRIAGE_DISPOSITION_INSUFFICIENT_EVIDENCE`。Agent 不得提交可信检测键、资产标识、检测键目标选择器、可信证据引用、创建主体、范围风险或证据类。

brain 的确定性协调器从 Turn 钉死的聚类版本、事件账和资产世代派生可信字段，再决定是否编译 `ProposalIntent`。`ProposalIntent` 仍是操作域用户或确定性协调器进入治理内核的类型化入口，不是模型输出结构。操作域用户提交的 `detection_keys` / `shape_source` 只是待验证断言：服务端必须确认检测键确实存在于该 `cluster_id` 的钉死版本，形状选择器确实出现于可信事件投影且语言满足收窄规则；不匹配返回 `failed_precondition`，不得按用户输入创造事实。工具 `govern.propose` 在生产只收完整提案意图，不收任意制品字节；无 `intent`、或 `kind=KIND_RULE` / `payload_schema=rules/v1` 的调用，**人侧 `ProposeArtifact` 与中台协调器必须同样**返回 `failed_precondition`，不得写入 draft。唯一例外是测试或带 `yufeng_dev` 构建标签的开发演示。

**研判令牌绑定。** 生产 `payload_ref` 是 `turn_id`；Turn 内钉死 `cluster_id` 与版本游标。重签能力令牌时必须由 Turn 解析已钉死聚类与资产，Bindings 只含类型化的 `turn:<turn_id>` 与 `asset:<asset_id>`，禁止加入裸标识，禁止把 `turn_id` 当事件标识，或去查“最近一条事件”。

生产研判读取只走字段级投影工具：`triage.get` 返回钉死聚类版本、代表性 `CheckTicket`、关联 `ModelInference` 和必要发布摘要；`triage.complete` 提交上述非可信结论。Bindings 只限制对象集合，不能代替字段脱敏，因此生产研判令牌不得含返回通用 Event 的 `event.get` / `event.list`，更不得含进程日志、指标或证据环读取。§18.1.1 的 `event.*` 只属于显式演示路径。调查 run 同样优先使用 `ticket.get` / `cluster.get` 等投影工具；任何保留的通用事件工具都必须在 ToolGateway 服务端裁成同一投影，不能仅靠提示词要求“不要看原文”。

| 叫醒原因（线上全名） | 允许编译的制品 | 禁止 |
|---|---|---|
| `TRIAGE_REASON_DETECTED_UNMITIGATED` | `PolicyCandidateV1`，谓词必须含检测键 | 任意正则、仅攻击类、`path_prefix=/` 的自动通道 |
| `TRIAGE_REASON_DETECTED_UNMAPPED` | 默认只写 L0。人坚持出策略时须绑原始规则检测键，且不得自动晋升 | 按五类出策略；CRS 方法/协议/扫描器进自动通道 |
| `TRIAGE_REASON_SUSPECTED_MISS` | 请求形状语言 | 任意正则、编造检测键策略 |

`created_by`、资产、检测器摘要、`evidence_refs`、`target_selector` 由确定性协调器从账本填充；`TriageDecision` 若夹带这些字段必须 `invalid_argument`，不能采用“忽略后继续”的宽松语义。自动晋升必须同时满足 `scope_risk ∈ {exact, route}`、`evidence_class ∈ {crs_mapped, human, replay}`、回放覆盖度合格。`prefix` / `asset_wide` / `class_only` / `crs_unmapped` / `model` 不得走自动晋升，协调器直接停止并写审计原因。

**令牌。** 生产研判令牌只含 `triage.get`、必要的其它字段级只读投影工具与 `triage.complete`，不含通用 `event.get` / `event.list`，也不含 `govern.propose` / `govern.gate` / `govern.start_shadow` / `promote_*`；会话令牌无 `govern.*`。跨资产、扩大已声明 scope 一律 `permission_denied`。`triage.complete` 要求访问令牌与能力令牌双携带、当前 `lease_id/lease_epoch`、`access_token.sub == capability_token.azp`，且生产 TLS 已开启。上述身份项未关闭时，不得把本节实现为可写路径。

**晋升。** 仅同时满足范围、证据类与回放覆盖度的策略可按 §7.5 门槛自动 canary/enforce（见上段）。形状规则、未映射策略与宽范围停在 shadow，必须另一用户 `promote_*`。调度器对演示 `KIND_RULE` 在生产配置下不得自动晋升；第一阶段演示只存在于测试或带 `yufeng_dev` 构建标签的开发目标，正式二进制不注册演示参数。该资产绑定单元数不足以形成非 0/100 的 canary 时，禁止自动进入 canary。

**下发。** 边缘装载单位是资产世代，不是单条 release。`ListReleases` 可继续作为查询，但 edge 必须在世代完整、验签通过、依赖摘要一致后原子替换 `activeGeneration`。旧世代无签名回滚授权时拒绝重放。

**匹配。** 策略命中 = 范围命中 ∧ 本次同步发现含声明的检测键 ∧ 覆盖度满足 `coverage_requirement`。外部授权未拿到的检查面不得参与依赖该面的策略。负向谓词要求对应面 `FULL`。

**HTTP 检查配置档默认值（反代与外部授权必须引用同一份）。** 字段见 `HttpInspectionProfile`；默认：

| 字段 | 默认 |
|---|---|
| `normalize_path` | true（折叠 `//`，解析 `.` / `..`） |
| `percent_decode_rounds` | 2 |
| `encoded_slash` | `reject` |
| `duplicate_query` | `first` |
| `duplicate_header` | `first` |
| `cl_te_conflict` | `reject` |
| `json_duplicate_key` | `reject` |
| `multipart_max_parts` | 16 |
| `multipart_max_part_bytes` | 65536 |
| `decompress_algorithms` | 空（不解压） |
| `decompress_max_bytes` | 65536 |
| `max_headers` | 64 |
| `max_params` | 128 |
| `json_max_depth` | 8 |
| `engine_body_limit_bytes` | 65536（`kernel.EngineBodyLimitBytes`） |

解析差异语料见 `procedures/http-inspection-baseline/corpus/parse-diff.jsonl`（12 条）。四种入口壳对同一语料必须得到同一规范请求视图与同一组覆盖度。

### 18.2 RunService（控制台与中台协调器调用）

| RPC | 说明 |
|---|---|
| CreateRun | 创建执行实例：`run_id`、`role`、`plan_ref`、`toolset`、`budget`、`ttl`、`bindings`、`created_by` |
| GetRun / ListRuns | 查询单个/列表，返回权威 `budget_snapshot` |
| CancelRun | 尚未跨越副作用边界时原子取消；已有动作时先持久化取消请求，由当前或重新领取的执行器进入同一补偿分支 |
| WatchRun | 服务端流，推送 run 状态变化 |
| ListRunEvents | 从权威审计链按 `run_id` 投影模型尝试、工具意图与结算、预算、租约和步骤回执；不读取进程内事件切片 |

`CreateRun` 只写队列，不启动进程。请求里的 `role` / `toolset` / `bindings` / `budget` / `created_by` 是提案：服务端按调用者授予裁剪，`created_by` 取认证身份，Bindings 为空或越权则 `permission_denied`。现行 `budget` 是 `1..100` 的调用档位；服务端把档位展开成 §18.10.4 的完整、非无限预算快照，`ttl` 同时钉死 `max_active_time` 与不可续期的 `execution_deadline`。实际领取由 `yufeng-agentd` 经 WorkerService 拉取；领取者档案 Bindings 必须**包含**整份工作项 Bindings（工作项 ⊆ 档案）；档案为空或缺项则不能领。`yufeng-run` 在绑定补偿计划前启动失败时，`FailWork` 可原子把空计划事务与 run 收成失败；计划一旦绑定，仍必须满足下述补偿或结果未知约束后才能进入终态。

`GetRun` / `ListRuns` / `WatchRun` / `ListRunEvents` 要求当前有效授予同时包含 `console.read` 且覆盖 run 的全部 Bindings；`CancelRun` 则要求 `run.create` 和同样的完整覆盖。列表只在可见项之间分页，`page_token` 不得跳过被隐藏项之后的可见记录。取消请求与 run 状态在同一事务内落盘。没有已开始动作的待领取工作可直接关闭；已有动作或当前租约仍在执行时，run 先进入 `cancelling`，保留补偿所需的租约、能力令牌和预算，续租响应向监督进程返回取消标记。监督进程经独立取消管道通知 `yufeng-run`，但保持本地代理和 brain 连接可用；执行器按已持久化计划逆序补偿，补偿回执全部结算后才由 `FailWork` 把 run 收成 `cancelled`。已进入终态的 run 不得被取消改写。

run 的补偿事务以 PostgreSQL 为恢复真相。执行器第一次运行必须先绑定有摘要的固定计划；每步计划至少包含稳定序号、步骤键、动作重放策略、是否有补偿以及补偿重放策略。每次动作依次写 `ACTION_INTENT_RECORDED`、`ACTION_EFFECT_STARTED` 和 `ACTION_SUCCEEDED` / `ACTION_FAILED`；补偿依次写 `COMPENSATION_INTENT_RECORDED`、`COMPENSATION_EFFECT_STARTED` 和 `COMPENSATED` / `COMPENSATION_FAILED`。守卫在动作意图中以摘要固定，副作用或补偿在对应 `EFFECT_STARTED` 落盘成功前不得执行。

上述状态变化、租约领取/续期、模型物理尝试、工具意图/副作用边界/结算和预算预留/结算同时进入 §10.2 的全局只追加哈希链。Agent 账本记录 `run_id + turn_id + lease_epoch + budget_id`，并只携带内容摘要、步骤序号、用量和状态；旧的 run 事件表只归档迁移前历史，不参与新写入、查询或恢复，`agents/runtime` 也不得建立可用于恢复的进程内审计存储。

租约过期后的 `WorkItem.saga_snapshot` 返回同一计划和逐步状态：已结算动作或补偿直接跳过；`SAFE` / `IDEMPOTENT` 可从未结算的副作用边界重试；`NEVER_REPLAY` 已写 `EFFECT_STARTED` 但没有结算时必须写 `OUTCOME_UNKNOWN` 并显式终止，不能跳过、重放或伪造成功。`CompleteWork` 只接受全部动作成功的事务；普通失败或取消只在应补偿步骤全部结算后进入终态。

当前非危险 `yufeng-run` 以 Go 运行时软内存上限约束托管堆，并保留 CPU 与文件描述符 rlimit；不得用 `RLIMIT_AS` 限制 Go 进程的虚拟地址空间。该机制不是硬内存隔离，生产 L2/L3 危险 Procedure 必须等 Linux 沙箱与 cgroup 硬内存边界落地后才可开放。

Agent 访问令牌不得直接调用 RunService。父 Agent 委派只走工具网关的 `run.create` / `run.get` / `run.join` / `run.cancel`，由 ToolGateway 从当前 ToolIntent 派生血缘、Bindings 和父预算；见 §18.10.7。这样 `CreateRun` 的用户认证域不会与 Agent 双令牌域混用。

### 18.3 WorkerService（agentd run 车道）

`WorkerService` 只承载由 `yufeng-agentd` 监督的短命 run。`PollWork` 禁止靠 capability label、空字段或任意 JSON 把规范流量或模型结果伪装成 run WorkItem；`yufeng-modelside` 只使用 §21.5 的专用协议。

**共同注册与档案**

| RPC | 说明 |
|---|---|
| CreateWorkerBootstrap | 仅操作域管理员持 `grant.write` 调用；请求精确 `worker_id`、`worker_kind`、worker 公钥、客户端证书 SHA-256 指纹、具体资产 Bindings 和引导令牌到期秒数。服务端校验 Bindings 不越过管理员范围，原子写一次性引导哈希与 `subject_kind=worker` 的范围授予；明文引导令牌只返回一次 |
| RegisterWorkerIdentity | 一次性引导：`worker_id`、`worker_kind=RUN_SUPERVISOR`、`bootstrap_token`、`worker_public_key`；返回可轮换 refresh token、短期 workload access token 与到期秒数 |
| RefreshWorkerAccessToken | 以当前 refresh token 轮换 refresh 并签发新的短期 workload access token；旧 refresh 立即失效 |
| RevokeWorkerIdentity | 仅操作域管理员持 `grant.write` 调用；要求管理员当前 Bindings 覆盖 worker 的全部资产范围。原子撤销身份、全部短期令牌和 worker 范围授予，并写审计链；重复撤销幂等 |
| RegisterWorker | 持 workload access token 与客户端证书登记能力/Schema/模型版本、并发与容量；请求 `worker_id/worker_kind` 必须与 token 固定声明一致。请求自报的 Bindings 一律忽略；档案 Bindings 只取服务端授予。自报能力只参与兼容性调度，不产生可见性 |
| RequestWorkerEnrollment | 外部 agentd 在本机生成私钥、证书请求和一次性 X25519 激活密钥对，主动提交 worker 标识、主机、公钥指纹、操作系统、处理器架构和沙箱能力；不产生 Bindings，返回的高熵 `enrollment_id` 是批准前轮询结果的唯一定位符。同一 `worker_id + public_key_fingerprint` 重复登记时返回数据库中既有的 `enrollment_id`、`public_key_fingerprint` 与真实 `state`，不得把已批准或拒绝误报为 `pending`；重复请求携带不同激活公钥仍返回 `already_exists` |
| GetWorkerEnrollmentResult | 登记客户端在尚无工作负载身份时以 `enrollment_id` 轮询状态。批准后只返回绑定该登记标识与登记时 X25519 公钥的加密激活包、包引用、批准清单摘要、沙箱挑战标识和到期时间；明文引导令牌与证书不得出现在响应字段或审计中。密文到期前允许重复取得，便于网络中断恢复 |
| ListWorkerEnrollments / DecideWorkerEnrollment | 持 `worker.enroll` 的管理员核对待注册主机与公钥指纹，且只能授予自己 Bindings 内的精确资产。注册请求、批准、拒绝和证书轮换都写审计哈希链，但审计详情不得包含证书请求、证书正文、引导令牌或私钥；批准响应只返回激活包引用、批准清单摘要和证书到期时间。`DecideWorkerEnrollmentResponse.bootstrap_token`、`client_certificate`、`certificate_chain` 是兼容保留字段，服务端禁止填充 |
| AcknowledgeWorkerActivation | 目标 worker 完成一次性引导、取得工作负载身份并把刷新令牌原子写入私有状态目录后，以工作负载访问令牌、客户端证书、`enrollment_id` 与 `activation_bundle_ref` 确认；服务端校验 worker 主体完全一致后原子清除激活密文。同一 worker 对同一登记与包引用的重复确认在 `acknowledged_at` 已写且密文已清除时幂等成功，不重复追加审计，保证首次确认后客户端崩溃仍可恢复；其他主体或错误引用继续失败关闭 |
| RenewWorkerCertificate | 工作负载身份携新证书请求主动轮换 24 小时客户端证书；独立 signer 持工作负载证书机构私钥，brain 不读取该私钥 |
| ListWorkers | 按当前有效授予中的用户 Tools × Bindings 返回已登记 worker 的平台、可验证沙箱能力、服务端计算的 `investigation_eligible` / `missing_sandbox_capabilities`、并发与最后心跳；可见性以 worker 当前有效授予的资产范围为准，不读取登记时档案快照，且必须在分页前过滤，隐藏项不消耗页容量或产生下一页令牌；不返回密钥或令牌，控制台不得用“能力数组非空”自行猜调查资格 |

工作负载身份路径冻结如下，不留给实现自行选择：

1. 部署调用同一服务端预置函数，或管理员调用 `CreateWorkerBootstrap` 生成一次性 `worker_bootstrap`；服务端只记录哈希，并绑定精确 `worker_id + worker_kind + public_key/certificate fingerprint + expiry`。同一 `worker_id` 已有有效身份或未过期引导时返回 `already_exists`；未使用引导过期后，管理员可在自身当前范围内重新签发，不得借此越权扩大 Bindings。不接受部署级共享未绑定令牌；请求身份与绑定值不符返回 `permission_denied` 且不消耗令牌，匹配注册成功后原子消费，并发或后续复用返回 `unauthenticated`。
2. workload access token 的 `aud=worker`、`sub=worker_id`，并固定 `worker_kind` 与客户端证书指纹；它不能调用 AgentControlService 的身份/指令 RPC、RunService、SessionService、用户/单元域接口。只有 `RUN_SUPERVISOR` 可按下一条带当前工作项能力令牌调用 AgentControlService 的 Turn 推进 RPC。
3. `RUN_SUPERVISOR` 只有在代持当前 run 工作项能力令牌、且 `capability.azp == workload_access.sub` 时，才可代表 run 调 `Generate` / `InvokeTool` / 回执；能力令牌仍是 `sub=run_id, azp=worker_id`。
4. 服务端授予增加 `subject_kind=worker`；RegisterWorker 每次从有效授予重建档案 Bindings，撤销或过期立即清空，不信数据库旧快照和请求自报。
5. agentd 默认只接受 WorkerService 签发的 `RUN_SUPERVISOR` 身份；旧 Agent 身份仅在显式 `-dev-agent-compat` 开关下用于开发兼容，不能进入生产部署。

网络上的 agentd 只主动连接 brain；不得使用单元令牌、用户会话或 NATS 凭据。注册、刷新、登记档案、领取、续租和完成均校验相互传输层安全协议（mTLS）客户端证书绑定，不能让 `RegisterWorker` 裸注册即获得领取权。

**run 车道（现有 `WorkItem`）**

| RPC | 说明 |
|---|---|
| PollWork | 仅长轮询领取 run 工作项；只返回工作项资产 Bindings ⊆ 该 `RUN_SUPERVISOR` 档案 Bindings 的条目。Brain 在领取事务内计数该 worker 未过期的已租工作，达到服务端 `max_concurrency` 时不再派发；多个本地领取槽只提供候选并发，不能绕过批准上限 |
| ExtendLease | 正常续租只延长 `lease_deadline`，保持 `lease_id` / `lease_epoch`，并返回持久化的 `cancel_requested`；所有权释放后的重新领取才换 id、增加 epoch 并吊销旧令牌 |
| ReportProgress | 绑定类型化补偿事务计划，或提交带步骤序号、阶段、守卫摘要、回执和错误的类型化步骤回执；通用日志只把阶段和载荷摘要写入权威审计链，原文仍只作观察信息 |
| CompleteWork | run 成功交付：`work_id`、`result_ref`、`receipt`；结算步骤预留；无验证步骤不得标业务成功。调查工作必须提交与冻结票据摘要一致的类型化 `InvestigationReceipt` |
| FailWork | run 失败交付：`work_id`、`error_code`、`message`、`compensation_hint`；结算已消耗额度并保留未知额度；调查失败由 brain 生成持久终态回执，不信任客户端自报状态 |

`WorkItem` 目标字段：`work_id`、`run_id`、`turn_id`、`agent_role`、`plan_ref`、`toolset`、`budget_id`、`budget_snapshot`、`ttl`（只返回墙钟剩余量）、`execution_deadline`、`bindings`、`capability_token`、`lease_id`、`lease_epoch`、`lease_deadline`、`saga_snapshot`，以及调查工作专有的 `investigation_input`。该输入携带完整冻结 `CheckTicket`、`ticket_digest` 和可选 `cluster_id`；服务端每次领取都从不可变票据表按事件标识与摘要重新装配，普通 run 不得伪造该字段。领取工作项在同一事务按 `work_id` 幂等预留一步；租约过期重领复用该预留，不增加额度。正常续租只轮换令牌，不改变预算上限、用量或墙钟截止时间。

通用 `ReportProgress.stage` 只接受 1–128 字节的 ASCII 审计标签，字符限字母、数字、点、下划线、冒号和连字符；载荷只写摘要。需要保留正文的观察日志走独立日志系统，不得借 `stage` 或 `payload_ref` 把原文塞进审计链。

agentd 独占 worker 访问令牌、刷新令牌、客户端证书与工作项能力令牌。外部 agentd 的一次性激活包必须以 0600 权限交付；客户端校验包内证书与本机私钥、worker 标识、客户端用途和证书链完全绑定。一次性引导成功后，刷新令牌必须以 0600 权限原子写入 agentd 独占状态目录，激活包立即删除；重启先轮换该令牌，状态缺失、损坏或身份不符均失败关闭，不回退复用一次性引导令牌。`yufeng-run` 只继承已连接的本地监督通道、只读的监督存活信号、只读的取消信号、`work_id` 与一次性随机数；Linux/macOS 使用本地套接字与管道，Windows 使用命名管道与命名事件。调查输入也只经该已连接通道读取，不进环境变量或命令行。Generate、工具、进度和结果都经 agentd 本地监督代理转发。代理按本地连接绑定的 `work_id` 附加双令牌，保持能力令牌 `sub=run_id`、`azp=worker_id`。agentd 丢失租约时主动终止完整子进程树；agentd 被强制杀死时，本地存活信号关闭，由 `yufeng-run` 的独立监视器终止自己的进程组或作业对象。墙钟到期由本地监督器立即杀树，brain 同时拒绝新预留并把 run 收成可查询的失败终态。

活跃监督期间不得把领取时的 worker 访问令牌冻结到整个 run：代理转发的 Generate / 工具调用和 agentd 发出的进度、补偿事务、续租与终态回执每次都读取同一 `AccessSession` 的当前代次。任一调用收到 `unauthenticated` 时，只允许该会话串行轮换一次刷新令牌、以 0600 权限原子持久化新值，再用新访问令牌重试原调用一次；非认证错误不得触发刷新或自动重试。服务端已经轮换刷新令牌但本地持久化失败时必须取消根工作循环，使在途 run 进入既有取消与补偿路径并返回持久化错误；不得继续使用内存新令牌，也不得拿已经失效的旧刷新令牌再次续期。

Brain 不在 WorkerService 中创建模型分析工作，不把 `CheckTicket` 或原始流量交给 run worker，也不接受 run worker 提交 `ModelInference`。数据库中的历史分析记录只读保留；新模型结果由 §21.5 的 `ModelResultService.UploadResults` 入账。

### 18.4 ToolGatewayService（Agent 与 run 的受控工具调用）

| RPC | 说明 |
|---|---|
| ListTools | 返回当前能力令牌工具白名单与已加载技能子集可见的短工具描述：名称、描述、版本、`schema_digest`、`effect`、`replay`；传输必须同时携带访问令牌与能力令牌 |
| DescribeTool | 请求 `tool_name`、`tool_version`、`schema_digest`；返回完整 JSON Schema。成功后把摘要钉死到当前 Turn，Schema 漂移返回 `failed_precondition` |
| InvokeTool | 按下述调用坐标和参数调用；ToolGateway 原子完成鉴权、预算预留与 intent 落账；传输必须同时携带访问令牌与能力令牌 |
| ListSkills | 要求能力令牌的工具白名单含 `skill.list`；仅返回当前对象绑定可见的技能稳定标识、名称、短描述和版本，不返回正文或资源 |
| LoadSkill | 要求 Tools 含 `skill.load` 且 Bindings 含 `skill:<stable_skill_id>`；请求钉死 `turn_id`、版本与正文摘要，响应正文、资源和与当前令牌相交后的工具子集 |

**传输契约**：ToolGateway 的全部远程过程调用必须同时携带以下两个超文本传输协议请求头；令牌不得放入请求消息字段。当前生产写路径已经执行该双令牌校验。

```http
Authorization: Bearer <access_token>
X-Yufeng-Capability: Bearer <capability_token>
```

`Authorization` 证明正在发起请求的 Agent 或 worker 进程身份。`X-Yufeng-Capability` 证明该进程对当前 instruction 或 work item 获得的最小业务权限。HTTP 请求头名称不区分大小写；值必须严格使用单个 `Bearer` 凭证。代理、结构化日志、审计事件和模型上下文必须同时脱敏这两个请求头。

worker 身份只允许 `worker_kind=RUN_SUPERVISOR` 且能力令牌属于当前 run 工作项。Agent 身份、run worker 身份与 ModelSide 工作负载身份使用不同 token audience，服务端不得只按 token 形状或共同 `sub` 前缀互相接受。

能力令牌的 `sub` 表示被授权的任务主体：Agent instruction 使用 `agent_id`，run work item 使用 `run_id`；新增标准 JSON Web Token（JWT）声明 `azp`（authorized party）表示实际持有访问令牌并领取租约的 `agent_id` 或 `worker_id`。brain 必须校验 `access_token.sub == capability_token.azp`，并继续校验能力令牌 `aud == "tools"`、租约、吊销和权限声明。

失败语义固定如下：任一请求头缺失、格式错误，或任一令牌无效、过期、撤销时返回 `unauthenticated`；两张有效令牌的 `sub`/`azp` 不一致时返回 `permission_denied`，响应不得泄露另一主体；能力令牌用途域错误、租约不匹配、工具或对象越权也返回 `permission_denied`。以上失败均不得执行工具、扣减预算或写入幂等成功结果。

`ListTools` 只能按能力令牌工具白名单与当前已加载技能的工具子集裁剪；列表时还没有具体参数，**不得**声称已完成对象绑定过滤。`InvokeTool` 在参数已知后按对象绑定做对象级校验。`DescribeTool` 返回的模式也不能改变令牌工具白名单。完整模式不随每次列表重复传输。生产目录只读取验签成功且处于 `shadow`、`canary` 或 `enforce` 的工具描述；`signed` 尚未激活，不可见，`retired` 不可见。原语绑定必须命中中台启动时注册的服务端实现；修复程序绑定必须引用另一份已验签、已激活的修复程序制品。描述载荷和技能正文都不能注册或执行新代码。

目标 `InvokeTool` 请求字段：

```text
thread_id
turn_id
step_id
call_id
tool_name
tool_version
schema_digest
arguments_json
arguments_digest
lease_id
lease_epoch
expected_item_sequence
```

`call_id` 由 brain 在持久化 ModelResponse 工具调用项时分配；供应商的标识只进 `provider_call_id`。Agent 自造未知 `call_id` → `invalid_argument`。幂等域固定为 `budget_id + turn_id + call_id`：同一调用与相同 `arguments_digest` 返回首次结算，不重复执行、不重复扣预算；同一调用换参数摘要 → `failed_precondition`。旧 `idempotency_key` 只服务尚未迁移的确定性演示工具，不得作为新座架调用的权威主键。

副作用状态固定为：

```text
CALL_PROPOSED
→ INTENT_RECORDED
→ EFFECT_STARTED
→ SETTLED(SUCCEEDED | FAILED | DENIED | CANCELLED | OUTCOME_UNKNOWN)
```

ToolGateway 在 `InvokeTool` 内完成访问令牌、能力令牌、租约 epoch、Schema、Tools、Bindings 校验，并在同一事务预留 `budget_id` 额度、写 `INTENT_RECORDED`。Agent 不另发登记 intent 的远程过程调用。brain 内数据库工具把业务变更、settlement、预算结算与审计摘要放进同一事务；host 或连接器等外部工具把 intent、预算预留与事务发件箱放进同一事务后才跨越副作用边界。settlement 只由 brain 根据内部事务或权威外部回执写，Agent 自报成功无效。

外部副作用不承诺严格恰好一次。工具描述的 `replay` 为 `NEVER_REPLAY` 且已 `EFFECT_STARTED`、没有 settlement 时，返回 `OUTCOME_UNKNOWN` 并禁止自动二次执行。第一阶段所有工具串行；后续只有只读且 `SAFE` / `IDEMPOTENT` 的工具可并行，写、委派与审批工具永远串行。**Agent 进程不直接连 PostgreSQL、NATS 或边缘**。

人机交付不得用“只带用户会话、不带能力头”的 `InvokeTool` 冒充活栈写路径：缺能力头返回 `unauthenticated`。活栈应验证操作域 `ProposeArtifact` 返回 `permission_denied`，因为引导管理员的系统授予不包含 `govern.propose`。当前实现分别传递访问令牌和能力令牌，并校验能力令牌的授权参与方等于访问令牌主体。

### 18.5 SessionService（人 ↔ 贾维斯）

| RPC | 说明 |
|---|---|
| CreateSession | 创建会话，返回 `session_id`；不得让客户端自选处理方 |
| SendMessage | 用户发送普通消息；brain 在一个事务内写消息、创建钉死 `message_sequence` 的 AgentTurn、入队 `payload_ref=turn_id` 的 SESSION_MESSAGE 指令 |
| PollMessages | 控制台长轮询增量消息（贾维斯不走本 RPC，回写走 `session.reply`）；`cursor` + `long_poll_seconds`（默认 `SessionLongPollDefault`=30s，上限 `SessionLongPollMax`=60s；省略或 0 按默认；超过上限 `invalid_argument`） |
| ListMessages | 历史消息分页 |

`PollMessages.long_poll_seconds` 默认与上限见上表，实现引用 `lib/kernel` 的 `SessionLongPollDefault` / `SessionLongPollMax`。有消息或超时才返回。

**授权**：`CreateSession` / `SendMessage` / `PollMessages` / `ListMessages` 是操作域 RPC，只认 `Login.token`（用户会话）+ 会话属主。**不**查授予表 Tools × Bindings，也不进入 §6.1 工具表。引导完成后管理员没有 `govern.*` 也能聊天。跨用户碰别人的 `session_id` → `permission_denied`。

会话指令按服务端身份白名单投递（当前配置 `-jarvis-agent-id` / `JarvisAgentID`），不是按角色广播。`CreateSession` 只创建会话并返回 `session_id`，请求不得指定处理方。`sender` 由服务端从认证身份推导，客户端字段忽略。`SendMessage.content` 与 `session.reply` 的 `content` 均为非空、UTF-8 最长 8192 字节，超限 `invalid_argument`。贾维斯回写必须走 `session.reply`（§18.1.1），禁止用用户态 `SendMessage` 冒充。用户原文只作为已签发 `SESSION_MESSAGE` 的附件数据，不是第二条命令通道：`SendMessage` 不得改发 `EVENT_TRIAGE` / `PLAN_REQUEST` 或带 `govern.*` 的指令。会话能力令牌不得含 `govern.*` 写工具，L1 自动推进不得以模型提案为输入。

同一 `session_id` 第一阶段只允许一个活动 Turn。活动 Turn 存在时，新 `SendMessage` 创建下一个排队 Turn，不能暗中并入当前 Turn；当前 Turn 的显式追加只走下节的 `SteerTurn` / `AppendFollowUp`。

### 18.5.1 AgentInteractionService（登录用户 ↔ AgentTurn）

此服务是操作域，不是 Agent 域。只接受 `AuthService.Login` 的用户会话令牌，**不得**接受 Agent access token 或 capability token；同理，AgentControlService 的推进接口不得接受用户会话令牌。

| RPC | 语义 |
|---|---|
| GetThread / GetTurn / ListTurnItems | 返回用户可见投影；隐藏模型推理原文、密钥、能力令牌、内部 checkpoint 与未授权内容引用 |
| SteerTurn | 把输入明确投给当前活动 Turn，在下一个安全 checkpoint 消费；不得修改已经出网的 ModelRequest |
| AppendFollowUp | 只在 Turn 准备进入终态前消费；消费后可再开一个 Step，不得插入在途 Generate |
| AnswerUserInput | 只回答该 Turn 当前未决的 `RequestUserInput`；写入后在同一事务唤醒原 instruction / work item |
| CancelTurn | 先持久化 `cancel_requested`，再取消可取消的 Generate / 工具；已开始的外部副作用必须对账或补偿 |
| DecideApproval | 对 `approval.request` 产生的冻结请求批准或拒绝；必须校验操作域授权、Bindings、职责分离、摘要与过期时间，不能作为普通用户回答处理；证据审批要求 `evidence.approve` 覆盖请求资产，中央执行池容量审批要求 `worker.capacity.approve` 覆盖其关联案例资产；证据与执行池容量的请求、批准、拒绝、过期均在业务事务内追加审计哈希链，只记录标识、状态、预算和非敏感原因 |
| GetApproval | 按 `approval_id` 返回证据或中央执行池扩容的冻结投影；证据审批按 `case.read` 与资产 Bindings 裁剪，容量审批按 `worker.capacity.approve` 与其关联案例的资产 Bindings 裁剪；跨资产读取统一返回 `permission_denied`，且当前没有审批列表接口可暴露越权投影；只返回模型主机、模型名、配置摘要、字段、预算、状态和到期时间，不返回证据正文或密钥 |

用户并发输入单独追加到 `agent_turn_inputs`，字段固定为：`turn_id`、`input_sequence`、`kind`（`TURN_INPUT_KIND_STEER` / `TURN_INPUT_KIND_FOLLOW_UP` / `TURN_INPUT_KIND_USER_ANSWER`）、`content_ref`、`received_at`、`consumed_at`。用户写入以 `turn_id + input_sequence` 做幂等，不携带也不推进 `expected_item_sequence`。Agent 在 step 边界且无在途 Generate 的安全 checkpoint 内，以一个事务标记输入已消费，并按当时 `expected_item_sequence` 追加对应 AgentItem。这样模型响应写回与用户输入到达可以并发，不会互相抢执行序号。

线程来源为用户会话时，读、steer、follow-up、answer 只允许会话属主；线程来源为研判或 run 时，读侧还须对应资产 Bindings，取消还须 `run.cancel`。本阶段不允许用户向研判或 run Thread 注入 steer / follow-up。终态 Turn 的迟到输入返回 `failed_precondition`，不得静默改投下一个 Turn。普通 `SendMessage`、steering、用户回答与审批是四种不同操作，不能共用一个 RPC 或 Item kind。

### 18.5.2 CaseService、EvidenceService 与 ModuleCatalogService

`CaseService.ListCases` / `GetCase` / `PollCaseActivities` 提供资产绑定的调查案件；`ResolveCase` / `ReopenCase` / `RecordCaseFeedback` 是要求 `case.manage` 的显式人工处置。案件状态只许 `OPEN`、`WAITING_EVIDENCE_APPROVAL`、`QUEUED`、`INVESTIGATING`、`FINDING_READY`、`SHADOW_OBSERVING`、`RESOLVED`、`FAILED`、`EVIDENCE_EXPIRED`，终态另带类型化 resolution；列表、活动与写入均按资产 Bindings 裁剪。审批拒绝直接以 `EVIDENCE_DENIED` 解决，重新调查必须显式 reopen。跨资产聚类只能写活动关联，不得合并案件或授权。

`EvidenceService.PollEvidenceRequests` / `SubmitEvidenceBundle` 只接受单元身份。证据请求必须冻结案件、资产、候选句柄、字段闭集、总字节上限、模型配置摘要、批准标识和十五分钟到期时间。每个冻结句柄必须至少提交一个获批字段片段；Edge 在模型输入上限内按句柄公平截断，任一句柄过期、缺失或没有可用字段都失败关闭，不得以不完整样本集继续调查。批准在敏感模型尝试进入 `EFFECT_STARTED` 时原子消费；任何冻结字段变化均返回 `failed_precondition`。brain 只允许在有界内存中继保存原文字节，禁止写 PostgreSQL、日志、审计正文和磁盘。

`CASE_REVIEW` 是 Jarvis 的确定性案件编排指令，不调用通用对话模型决定固定控制流：先以 `case.get` 读取脱敏案件；`OPEN` 时调用 `case.request_evidence` 后结束当前指令；Edge 成功提交已批准证据时，Brain 必须在同一业务事务内持久化一条以该敏感引用去重的后续指令；`QUEUED` 时后续指令以 `run.create` 创建短命调查。领取指令时必须重新从案件冻结的受管 Agent 档案签发 `case:<id>` 与 `asset:<id>` 范围的能力，每个案件工具调用同时校验精确案件与资产绑定，禁止同资产的案件之间串权，也禁止回落为会话工具。重复提交、Brain 重启或 Jarvis 重试最多产生一个有效 run；短命调查领取后案件从 `QUEUED` 进入 `INVESTIGATING`。模型分析仍只发生在短命 run 中，Jarvis 不读取证据正文。

短命调查 run 的能力令牌保留 `case:<id>` 作为对象边界；worker 档案仍只授予精确 `asset:<id>`。调度兼容性只用 run 中的资产绑定核对 worker 档案，`case:<id>` 由 Brain 按案件到资产的权威关系派生，不要求 worker 获得第二种授权。结论为“疑似漏报”且确定性协调器已建立 Shadow 候选时，案件可以 `SHADOW_OBSERVING` 作为有效调查结论状态完成 run；后续 Shadow 观察不得被 run 成功回执误改为失败。

`ModuleCatalogService.ListModules` 返回编译期已注册模块及其版本、所需生产能力、案件活动 Schema 和可用界面表面。`DefenseModule.active` 只有在至少一个最近两分钟内成功心跳的 Edge，其 `ProducerCapabilities.module_capabilities` 同时包含模块全部要求时才为真；历史注册但已离线的 Edge 不得继续激活模块界面。能力字符串只表达协议实现能力，不产生资产 Bindings 或读取授权；目录是全局能力目录，具体案件和统计仍由各自服务按 Tools × Bindings 裁剪。目录不得携带远程脚本、任意布局或可执行正文；未知模块由控制台通用案件渲染器降级显示。

### 18.5.3 AgentProfileService（登录用户 ↔ 受管短命 Agent）

受管 Agent 是带稳定 `agent_id` 的业务主体，但不创建常驻网络进程。Jarvis 只创建绑定该 Agent 冻结配置的 run，`agentd` 启动的 `yufeng-run` 才执行调查；run、模型、工具、结论和审计必须记录 Agent 标识与配置摘要。`ListAgentProfiles` 保留原服务名以兼容旧客户端，要求 `console.read`，并只返回至少一个资产 Binding 与调用者范围相交的 Agent；其余写方法要求 `agent.manage`。列表继续只返回与调用者相交的 `bindings`，避免泄露越权资产标识；同时返回只读 `can_manage`，仅当调用者含 `agent.manage` 且档案完整原始资产集合都在调用者范围内时为真。客户端必须使用 `can_manage` 控制单项写入口，不能从已裁剪的 `bindings` 反推完整范围。每个返回项还投影执行模式、配置摘要、活动 run 数、最近执行时间，以及最近一次实际领取其 run 的 Worker 标识和操作系统/处理器架构；这些执行字段只读，不允许由更新请求伪造。

首版 `kind` 固定为 `AGENT_PROFILE_KIND_TRAFFIC_REVIEW`。工具闭集固定为 `case.get`、`case.request_evidence`、`run.create`、`case.complete`；前三项是完成脱敏读取、证据审批和短命调查闭环的必需工具，`case.complete` 可选。任何数据库、通用事件、边缘原文、治理推进或任意工具名均以 `invalid_argument` 拒绝。Bindings 只接受精确 `asset:<id>`，禁止空值和 `*`。`BatchUpdateAgentProfiles` 以同一份工具和资产集合原子覆盖所选档案，任一档案不存在或越权则整批失败。

`DeleteAgentProfile` 不接受 Jarvis 标识，也不能删除 Jarvis；删除写墓碑并禁止新委派，历史案件、活动、run 和审计记录保留。Create、Update、BatchUpdate、Delete 都必须使用幂等账本并在同一数据库事务写配置与审计哈希链；接口不签发常驻 Agent access token、bootstrap token、worker 证书或模型凭据。

新流量案件只能分派给资产 Binding 精确覆盖该案件且状态为 enabled 的档案；Brain 选择当前未结案件最少的合格档案，并把 `agent_id`、显示名、工具、Bindings 和配置摘要冻结到案件。Jarvis 的案件指令只获得该快照内的案件工具，不获得 `model.generate`，也不得使用全局固定案件工具集；真正的模型能力只签发给短命调查 run。没有合格档案时案件保持 `OPEN` 并追加“等待 Agent 配置”的活动；后台协调器会在档案新增或恢复后重新匹配。分派和指令入队通过同一 PostgreSQL 事务中的待投递记录衔接，Brain 重启或 Jarvis 尚未注册时可安全重试，且每个案件最多产生一条有效初始委派。

事件与审计的存储来源不得在界面上混写：`ConsoleService.ListEvents` 读取中台 PostgreSQL 的不可变事件账，其中流量/传感事件主要来自单元 `TelemetryService.UploadEvents`，也可包含 brain 确定性派生的 Agent/情报事件；它不是全量流量湖。`AuditService` 读取 Brain 在控制面事务内追加的 `audit_entries` 哈希链，记录用户、Agent、治理、worker 和系统动作，不由 Edge 上传。流量统计窗、审查候选和案件分别走 `traffic` schema 与案件表，不复制成普通事件或审计正文。

取消尚未 `EFFECT_STARTED` 的调用可结算为 `CANCELLED`；已跨越外部副作用边界的调用必须完成对账或补偿，之后 Turn 才能进入 `CANCELLED`。无法判定结果时进入 `OUTCOME_UNKNOWN`，不得为了满足取消请求伪造成功或取消。

### 18.6 CommandService（执行单元，字段级草案）

| RPC | 说明 |
|---|---|
| PollCommands | host 长轮询；请求 `unit_id`、`long_poll_seconds`；响应 `Command[]` |
| ReportStep | 逐步回执：`command_id`、`step_index`、`status`、`receipt`、`error` |

`Command` 字段：`command_id`、`run_id`、`procedure_ref`、`artifact_ref`、`target_asset_id`、`steps[]`（原语、参数、守卫、确认标记）、`deadline`、`rollback_ref`、`idempotency_key`。`PollCommands` / `ReportStep` 的 `unit_id` 必须等于令牌单元。未实现原语必须 `unimplemented` 或 `failed_precondition`，不得报 `SUCCEEDED`。变更步骤的成功只是执行器声称；命令状态「已验证」只能由程序中另一条验证步骤回执给出。

Host 必须以 `Command.deadline` 收窄制品读取、`service.reload` 与 `verify.service_active` 等可能长期等待的执行上下文；到期按步骤失败结算。若到期发生在副作用开始之后，写回失败状态和补偿必须改用未被该截止时间取消的父上下文，不能因执行上下文已超时而跳过回滚或终态回执。

### 18.7 独立进程如何做到指令级轮询

```text
yufeng-jarvis ──PollInstructions(long_poll 默认 AgentLongPollDefault=30s，上限 AgentLongPollMax=60s)──▶ brain
brain 队列空：挂起请求，最多 AgentLongPollMax 返回空
控制台 SendMessage ──▶ brain 写入 SESSION_MESSAGE 指令
brain 立即唤醒挂起请求 ──▶ 返回 instruction + capability_token
jarvis ──ToolGateway/InvokeTool(access + capability)──▶ brain 校验、记账、审计
jarvis ──AckInstruction──▶ brain 关闭指令租约

yufeng-agentd ──WorkerService/PollWork──▶ brain 返回 WorkItem
agentd 孵化 yufeng-run；run 只连本地 supervisor broker
run 每个动作 ──本地 IPC──▶ agentd ──ReportProgress(意图/副作用边界/结算)──▶ brain
取消请求 ──▶ brain 持久化 cancelling ──续租返回取消标记──▶ agentd 取消管道 ──▶ run 逆序补偿
run 结束 ──本地 IPC──▶ agentd ──CompleteWork/FailWork──▶ brain 校验补偿事务后落终态

edge ──本地有界非阻塞队列──▶ yufeng-modelside ──独立有界结果队列──▶ ModelResultService/UploadResults
edge / yufeng-modelside ↛ Jarvis / PostgreSQL / NATS；原始流量 ↛ brain
```

关键点：进程独立不意味着必须用回调或双向流；**持久队列 + 长轮询 + 租约**就能获得指令级交互。本地监督进程间通信（IPC）是进程监督边界，不是第二个网络对等体；run 没有网络对等体，agentd 的唯一网络对等体是 brain。租约过期重领时，agentd 把 `saga_snapshot` 留在本地代理，run 先读取快照再执行；进程内事件切片和日志不得作为跳过、重放或补偿的依据。

### 18.8 Agent 与 worker 身份认证协议

```text
Agent bootstrap ──RegisterAgent──► aud=agent access / refresh
worker bootstrap ──RegisterWorkerIdentity──► aud=worker access / refresh
                                                   │ 固定 RUN_SUPERVISOR
                                                   ▼
                                  instruction / run work capability
                                  Ed25519 JWT：sub/azp/Tools/Bindings/
                                  budget_id/lease_epoch/exp/jti

ModelSide 工作负载凭据 ──► aud=modelside，只允许 UploadResults
```

规则：

1. `bootstrap_token` 由部署系统或管理员预生成。Agent 引导绑定唯一 `agent_id`；worker 引导绑定精确 `worker_id + worker_kind + public key / client certificate fingerprint`。请求身份与绑定值不同时返回 `permission_denied` 且不得消耗令牌；匹配注册成功后原子标记已使用，同一令牌并发或后续复用返回 `unauthenticated`。生产拒绝未绑定的部署级共享令牌。
2. `refresh_token` 不进入模型上下文、不写日志；刷新时旧 refresh 立即失效并轮换（refresh token rotation）。
3. `access_token` 只证明进程身份，且 Agent / worker / ModelSide audience 不互认。Agent 或 `RUN_SUPERVISOR` 的业务工具/生成必须再带更窄的 `capability_token`；ModelSide 没有工具能力，只能上报与签名模型档案一致的结果批。
4. `capability_token` 由 brain 在 `PollInstructions` / run `PollWork` 响应中下发，TTL 不超过允许的租约窗口。初次领取及租约释放后的重新领取换 `lease_id`、增加 `lease_epoch`，返回新 capability token 并吊销旧租约令牌。正常 `ExtendInstructionLease` / `ExtendLease` 保持 id 与 epoch，只延长 deadline；若轮换 token，新旧令牌必须引用同一 epoch 与 `budget_id`，旧令牌可在原 `exp` 前完成同 epoch 已在途请求，不能因续租制造假失败。
5. 能力令牌是 Ed25519 签名的 JSON Web Token（JWT）：`iss`（治理内核 key id）、`sub`（被授权的 agent_id/run_id）、`azp`（实际领取租约并持访问令牌的 agent_id/worker_id）、`role`、`aud`、`scopes`、`tools`、`bindings`、`max_calls`、`budget_id`、`lease_epoch`、`exp/nbf/iat`、`jti`。`jti` 只标识令牌实例、短期调用上限与吊销，不是跨续租预算或副作用幂等主键。权威额度在 `budget_id` 账户；模型与工具调用分别按 `generation_id/attempt_id` 和 `turn_id/call_id` 幂等与结算。ToolGateway 的双令牌承载与错误语义以 §18.4 为准。
6. 传输层必须 TLS，生产缺证书拒绝启动。双向 TLS 加在 Agent/worker 主动发起的连接上（brain 服务器证书 + 注册公钥对应的客户端证书），不改变「中台不拨号」。指令或工作信封目标上按注册公钥加密；无公钥不得入队。
7. 续期或轮询必须对得上注册公钥与客户端证书身份。访问令牌被盗后的并行假冒窗口不超过该令牌 TTL；refresh 轮换不能代替公钥/证书绑定。

上述规则是 §5.4–§5.5 的目标协议。当前生产实现已经覆盖一次性引导、刷新令牌轮换、短期访问令牌、租约续期、吊销、失败重试，以及引导凭证绑定唯一 `agent_id`。该身份闭环只证明智能代理控制面身份成立，不能替代部署、安全、故障与客户现场变更证据。

### 18.9 攻击面分析与对策

对策一律复用已有格子（令牌、绑定、验签、长轮询），不新开权限产品、不让 brain 反向连接 Agent。

| 攻击面 | 风险 | 设计对策 |
|---|---|---|
| 中间人窃听/篡改 | 令牌、指令或流量副本泄露 | 贾维斯、agentd 与 ModelSide 只主动连接；run 无网络连接；生产双向 TLS；Edge 与同机 ModelSide 优先 Unix 域套接字，跨主机必须相互传输层安全协议认证并限制在受控防御网络；无明文回退 |
| 引导/刷新令牌被盗 | 冒充该 `agent_id` | 引导令牌绑定 `agent_id` 且一次性；refresh 轮换；注册公钥参与续期 |
| 访问/能力令牌重放 | 旧令牌重复执行 | 双令牌 `sub==azp` + 当前 `lease_epoch` + 持久 `budget_id` + 业务调用标识。`jti` 只做令牌实例吊销；失窃后同 epoch 剩余权限仍是接受窗口，不是“已解决” |
| 工作项匿名抢领 | 任意 worker 领走别人的能力令牌 | 工作项 Bindings ⊆ 档案 Bindings；空档案不能领 |
| 提案人自喂门槛 | 一人提案并用自己的边缘把 L1 推上 enforce | 自动晋升排除提案人关联单元的计数/事件；手动推进须另一用户持 `promote_*` |
| 授予自我扩权 / 通配符 | 管理员给自己加全域 enforce | 不能授给自己；禁止 `*`；被授范围 ⊆ 授予者范围 |
| 请求里的角色/工具/绑定 | 客户端自报放大权限 | CreateRun 等字段是提案，服务端按授予裁剪 |
| 会话横向读 / 伪造 sender | 读他人对话、冒充贾维斯 | 属主校验；`sender` 服务端推导；回写走 Agent 工具 |
| 提示词注入 / 外界通道 | 用户字变成治理写或对外连接 | Agent 无外界套接字；命令只从中台来；会话令牌无 `govern.*`；L1 推进不吃模型提案 |
| 用户三角色过粗 | 偷一个 `USER_ROLE_OPERATOR` 会话全站 L1 | 角色只是模板；写看 Tools × Bindings；默认无 `promote_enforce`；propose 与 enforce 分人 |
| 单元/资产抢注 | 先占 `unit_id`、upsert 他人资产 | 引导令牌不能覆盖已有单元；已有 `asset_id` 不可被抢走；令牌 `unit_id` 与资产绑定校验进遥测/拉制品/指令 |
| 心跳/误报博弈 | 客户端计数器骗过晋升或清窗口 | `generation` 不抹服务端基线；晋升不得只信自报计数 |
| 执行器自报成功 | 账本显示已修、现场未动 | 未实现原语拒绝；「已验证」只认验证步骤回执 |
| Agent 直连敏感后端 | 绕过审计 | 无 PostgreSQL、NATS 或边缘凭据；网络只放行 brain |
| ModelSide 冒充 run 或直接影响闸 | 获得工具能力或改变当前请求 | 独立 `aud=modelside`；只接收异步流量并上报类型化结果；无 ToolGateway、Gate、数据库、消息服务器或单元凭据；结果永不回改当前请求 |
| 能力广告自我授权 | 单元或 worker 自报标签后看见其它资产 | 广告只做兼容调度；Bindings 只取服务端授予；模型阈值和采样只取签名模型档案 |
| 远端 ModelSide 互订或直连消息服务器 | 绕开 Brain 入账，形成平行总线 | 只暴露 Brain Connect 批量结果接口；不发 NATS 凭据；跨站联动只读写中台账本 |
| run 窃取 worker 身份 | 子进程拿到 access/refresh/cert 后绕过监督 | 凭证只在 agentd；run 仅持已连接本地套接字、`work_id` 与一次性随机数；文件描述符绑定工作项；丢租约关闭代理并杀进程树 |
| 续租使合法在途调用失效 | 每次 Extend 都换所有权代次与吊销令牌，造成重复生成/工具重试 | 正常续租保持 `lease_id` / `lease_epoch`；只有释放后重新领取才增加 epoch；轮换令牌引用同一 epoch / budget，并允许旧令牌在原到期前完成同 epoch 在途请求 |
| steering 与模型响应争序号 | 用户输入先占 `expected_item_sequence`，合法 ModelResponse 写回失败 | 用户输入走独立 `input_sequence`；只在安全 checkpoint 物化为 AgentItem |
| 模型请求重复计费 | 供应商收到请求后 brain 崩溃，恢复误判为未出网 | 逻辑 ModelGeneration 与物理 ModelAttempt 分离；每 attempt 预留/结算预算；未知结果显式落账；transcript 至多接受一个响应，不宣称物理出网恰好一次 |
| 拒绝服务 | 长轮询打满 | 单元/Agent 分域限流；长轮询有并发上限，不只看每秒请求数 |

**结论**：当前生产实现使用“唯一对等体 + 同一套 Tools × Bindings + 领取时确认主体”的身份与授权模型。单站点企业试点的软件版本必须由 `VERSION`、公开 GitHub Release 与精确提交证据共同证明；第一阶段演示与局部后端测试仍不能替代真实部署和客户现场变更记录。

### 18.10 统一 Agent 座架协议

本节冻结自主认知循环的持久对象、生成、预算、委派、审批、技能与恢复语义。现有 `CompleteChat(messages[] → text)` 不具备这些语义，只作为迁移期纯文本接口和连通性探测；新座架不得在客户端内自行拼接出网、工具调用或恢复逻辑。

#### 18.10.1 对象、来源与状态

```text
AgentThread
  └─ AgentTurn
      └─ AgentStep
          ├─ AgentItem
          └─ ModelGeneration
              └─ ModelAttempt
```

| 来源 | AgentThread 钉死字段 | 每个 AgentTurn 钉死字段 |
|---|---|---|
| 用户会话 | `session_id` | `message_sequence` |
| 事件研判 | `cluster_id` | `cluster_version` 或明确 `event_cutoff` |
| 认知型 run | `run_id` | `work_id` + `plan_digest` |

`session_id`、`cluster_id`、`run_id` 都不是 Turn 主键。Thread 保存长期来源，Turn 保存一次处理的输入游标；恢复只按钉死游标读取。当前聚类、最新会话消息或后来更新的计划都不能悄悄改变已打开 Turn。

Turn 线上状态闭集：

```text
AGENT_TURN_STATE_PENDING
AGENT_TURN_STATE_RUNNING
AGENT_TURN_STATE_WAITING_TOOL
AGENT_TURN_STATE_WAITING_CHILD
AGENT_TURN_STATE_WAITING_APPROVAL
AGENT_TURN_STATE_WAITING_INPUT
AGENT_TURN_STATE_COMPACTING
AGENT_TURN_STATE_CANCELLING
AGENT_TURN_STATE_COMPLETED
AGENT_TURN_STATE_FAILED
AGENT_TURN_STATE_CANCELLED
AGENT_TURN_STATE_OUTCOME_UNKNOWN
```

合法边：

```text
PENDING → RUNNING
RUNNING → WAITING_TOOL | WAITING_CHILD | WAITING_APPROVAL | WAITING_INPUT | COMPACTING
RUNNING → COMPLETED | FAILED | CANCELLING | OUTCOME_UNKNOWN
WAITING_* → PENDING | CANCELLING
COMPACTING → PENDING | FAILED
CANCELLING → CANCELLED | FAILED | OUTCOME_UNKNOWN
```

其它迁移返回 `failed_precondition`，不追加 Item、不扣预算。`COMPLETED` / `FAILED` / `CANCELLED` / `OUTCOME_UNKNOWN` 是终态。外部副作用已开始但结果无法判定时用 `OUTCOME_UNKNOWN`；禁止从该状态自动回到 `RUNNING`。

贾维斯 Turn 只借现有 `agent_instructions` 调度，run Turn 只借现有 `work_items` 调度；两者的认知历史写只追加账本，禁止再建一条 Agent Poll。Agent 执行写必须携带 `instruction_id` 或 `work_id`、`lease_id`、`lease_epoch`、`expected_item_sequence`。序号必须等于“当前最大 Item 序号 + 1”；旧 epoch 或错误序号 → `failed_precondition`，不得出网、执行工具或预留预算。

AgentItem 至少覆盖：输入引用、模型请求、模型响应、工具调用提议、工具结算、steering、follow-up、用户回答、待回答问题、子 run 结果、审批结果、压缩项、取消请求和终态摘要。隐藏思维链原文不入账；可审计 reasoning summary 必须标明是模型摘要，不得当事实。

#### 18.10.2 Generate 请求、落账与恢复

`ModelGatewayService.Generate` 传输使用与 ToolGateway 相同的两个请求头，并额外校验当前 `lease_id` / `lease_epoch`；能力令牌 Tools 必须显式含 `model.generate`。`Authorization` 可以是贾维斯的 `aud=agent` access，或 agentd 的 `aud=worker, worker_kind=RUN_SUPERVISOR` workload access；后者必须与工作项能力令牌 `azp` 一致。ModelSide 工作负载身份即使伪造 capability 头也一律 `permission_denied`。

流量证据使用 `GenerateInputItem.sensitive_content_ref`，不得复制进普通 `content`。引用必须对应尚未消费的证据批准、当前案件和模型配置摘要；数据库只落引用、内容摘要、批准标识和费用。敏感生成固定 `trust_level=untrusted_traffic`、结构化输出、无工具定义，默认最多 8192 输入 token 和 1024 输出 token。供应商结果必须验证为 `TrafficFinding` 闭集并经过原文回显检查；失败只保存结果摘要。结果未知的敏感尝试不可自动重放。

`GenerateRequest` 目标字段：

```text
thread_id
turn_id
step_id
generation_id
expected_item_sequence
context_manifest
input_items[]
tool_definitions[]
generation_limits
lease_id
lease_epoch
```

`GenerateResponse` 目标字段：

```text
generation_id
accepted_attempt_id
output_items[]
finish_reason
usage
actual_model
provider_request_id
retry_class
optional_reasoning_summary
```

工具调用输出项必须含平台 `call_id`、供应商 `provider_call_id`、名称、`arguments_json`、`arguments_digest`、`schema_digest` 与 `output_index`。`call_id` 由 brain 在接受 ModelResponse 时分配；Agent 原样带回 InvokeTool。

`ModelGeneration` 表示一次逻辑生成，状态为 `MODEL_GENERATION_STATE_PENDING/RUNNING/COMPLETED/FAILED`。每次物理出网由 brain 分配新的 `attempt_id`，`ModelAttempt` 状态为：

```text
MODEL_ATTEMPT_STATE_INTENT_RECORDED
→ MODEL_ATTEMPT_STATE_EFFECT_STARTED
→ MODEL_ATTEMPT_STATE_SETTLED

EFFECT_STARTED → MODEL_ATTEMPT_STATE_OUTCOME_UNKNOWN
```

每个 attempt 出网前把规范化请求、`ContextManifest`、生成上限和预算预留放入同一事务；真正调用供应商前写 `EFFECT_STARTED`。响应到达后先持久化候选响应、usage 和 attempt 预算结算，再以条件写入选定该 generation 唯一接受响应，最后返回调用方。迟到响应或并发重复响应只进审计，不能进入 transcript。

同一 `generation_id` 已有接受响应时直接回放，不再次出网。只有 `INTENT_RECORDED`、尚未 `EFFECT_STARTED` 的 attempt 可以安全继续；已 `EFFECT_STARTED` 且无结算的 attempt 先标 `OUTCOME_UNKNOWN`。供应商支持幂等键时由 `generation_id` 派生稳定键；不支持时，是否创建新 attempt 由显式重试策略与剩余预算决定，且可能重复请求或计费。御锋**不承诺模型供应商物理出网恰好一次**，只承诺 transcript 至多接受一个响应、每个 attempt 与成本可对账。

#### 18.10.3 ContextManifest 与压缩

每次 Generate 必须保存可重建当时模型输入字节的配方，至少包括：

```text
有序 AgentItem id 与内容摘要
system_prompt_version
role_profile_version
已加载 skill_id / version / content_digest
tool_catalog_version
loaded_schema_digests[]
compaction_entry_ids[] 与覆盖的 Item 序号区间
model_slot_id / dialect / adapter_version
capability_projection_digest
内容来源信任等级
```

内容来源信任等级至少区分平台、用户、技能、外部工具与子 run。压缩只在 step checkpoint 执行，前提是无在途 Generate、无未结算外部调用。不得摘要掉能力限制、审批拒绝、待回答问题、证据引用、预算状态、子 run 结果或任何未结算工具。压缩本身是计入预算的 ModelAttempt；失败时保留原窗口，不能静默丢上下文。大正文放受控内容存储，认知账本和审计链存内容摘要与引用。

#### 18.10.4 权威预算账户

每个 Turn / run 必须引用一个持久 `budget_id`。能力令牌只引用该账户；换令牌、正常续租、等待后重新领取、压缩或模型重试都不得新开额度。账户至少含：

```text
max_steps
max_model_calls
max_input_tokens
max_output_tokens
max_tool_calls
max_tool_result_bytes
max_cost_microunits
max_active_time
execution_deadline
```

每次模型 attempt、工具调用和子 run 创建都先 reserve，响应或 settlement 后 settle；超过任一余额返回 `resource_exhausted`，不产生副作用。等待状态暂停 `max_active_time` 累计，但 `execution_deadline` 按墙钟继续。供应商不返回 usage 时按 generation limit 或可证明的保守上限入账，禁止记 0。

子 run 额度从父账户同一事务预留，预留后父不可再消费该部分；兄弟预留之和不能超过父余额。子终态只归还已确认未使用、且不属于未知结果或未结算调用的额度。服务端预算策略必须给出每个字段的实际值；字段缺失不能解释为无限。

#### 18.10.5 技能与工具目录

技能使用现有工具白名单与对象绑定组成的授权格子：`skill.list` / `skill.load`，对象绑定为 `skill:<stable_skill_id>`。只有已签名且显式激活的版本可见：通用发布状态机中的 `signed` 表示签名完成，`shadow` / `canary` / `enforce` 表示已激活，`retired` 表示已撤销，因而签名、激活、撤销仍是三条不同状态边。技能清单至少含：稳定标识、版本、名称、短描述、正文引用与摘要、资源引用及大小与媒体类型、建议工具、必需工具、兼容岗位、最低运行时版本、模型能力、最大上下文字节和发布者密钥标识。正文和资源字节属于签名载荷，但 `ListSkills` 不返回；每个引用必须等于对应字节的内容摘要。

初始上下文只放名称、短描述和版本；`skill.load` 才按内容地址取正文与资源，并把版本与摘要钉死到当前认知回合。必需工具有任一不在当前能力令牌内时加载失败，不自动补权；响应中的可用工具只能是技能清单建议或必需集合与能力令牌工具白名单的交集。优先级为平台约束 > 能力令牌 > 当前任务 > 技能。技能正文不得由贾维斯或执行实例当脚本执行；需要执行的步骤必须成为签名修复程序。普通新版本或因取代而退休的旧版本不改变已打开的认知回合；人工撤销、回滚或到期在下一个生成检查点使对应钉死引用失败。

生产代码中固定的只有 `ListTools`、`DescribeTool`、`ListSkills`、`LoadSkill` 四个目录引导操作和服务端实现注册表；它们不作为可调用 ToolDescriptor 注入模型。其余模型可见工具一律来自已验签、已激活的 ToolDescriptor。演示工具清单只存在于测试或带 `yufeng_dev` 构建标签的开发目标，不得作为生产交付证据。

#### 18.10.6 审批

Agent 发起人审只调用工具 `approval.request`；`APPROVAL_REQUEST` 是 brain → Agent 的保留指令种类，本档仍禁止用它反向表达 Agent 请求。登录用户通过 `AgentInteractionService.DecideApproval` 决定，结果写 `ApprovalDecided` Item，并唤醒原 instruction / work item。

审批记录至少冻结：`approval_id`、请求主体、`thread_id/turn_id/tool_intent_id`、工具名/版本、参数摘要、计划与动作下标、制品标识/摘要、资产世代、资产集合、`budget_id` 与本次预留、过期时间、决定人、职责分离依据和状态。批准产生的 grant 在写 `EFFECT_STARTED` 时原子消费；参数、制品摘要、资产世代或目标集合任一变化，grant 作废并重新审批。

L3 执行前对完整修复计划/Procedure 的审批，与程序执行中单个“须人工确认”步骤的审批是两个对象，不得合成一次永久授权。`WAITING_INPUT` 是向用户提问，`WAITING_APPROVAL` 是等待授权；二者的权限、界面与 Item kind 必须分开。

#### 18.10.7 委派

父 Agent 只可调用 `run.create`、`run.get`、`run.join`、`run.cancel`，不得用 Agent access token 直调 RunService。`run.create` 从当前 ToolIntent 派生父血缘，并与父预算预留同一事务；请求中的父 id 不作授权。子上下文是显式快照，不继承整份父 transcript；子结果按不可信认知输入处理，不能直接成为检测键、证据或制品事实。

`run.join` 不阻塞 HTTP，也不占租约：子未终态时登记等待，父 Turn 转 `WAITING_CHILD` 并 Yield；子终态时 brain 写 ChildResult Item，并在同一事务唤醒父 instruction / work item。禁止 detached child；父进入终态前所有子必须终态或已确认取消，否则父只能处于 `CANCELLING` / `FAILED`。

#### 18.10.8 沙箱、模型上下文协议连接器与发布门禁

生产 L2/L3 危险 Procedure 必须在 Linux Landlock + seccomp 沙箱执行，并具备默认无网、显式环境允许列表、关闭无关文件描述符、内容地址制品只读挂载、一次性工作目录、中央处理器/内存/进程数/文件数/输出字节/墙钟上限和整棵进程树终止。只有 rlimit 或沙箱不可用时返回 `failed_precondition`，禁止裸执行。macOS Seatbelt 或降级夹具只用于开发验证。

模型上下文协议（Model Context Protocol，MCP）只经 brain 的受控连接器桥。稳定工具名为 `mcp.<connector_id>.<tool_name>`；brain 只暴露管理员审核后的内部 ToolDescriptor，不透传外部服务器目录。连接器、用户、租户和 secret 隔离；Schema 漂移进入 quarantine；数据出网前按分类检查参数，能力令牌、密钥和私有证据不得进入 URL 或参数。stdio 服务器只能来自运维安装的签名包并运行在独立沙箱；凭据由桥注入，御锋令牌永不传给 MCP 服务。外部结果一律不可信，超限结果改写为内容引用，禁止截断出非法 JSON。

实现发布门禁要求下列计数均为 0：无 intent 的副作用、未审批 L3 副作用、越 Bindings 的读/调用、不可重放工具在未知结果后的二次执行、已接收 steering/用户回答丢失、陈旧 `lease_epoch` 写入成功、预算超扣、已接受 Turn 既非终态也非 `OUTCOME_UNKNOWN`。测试必须覆盖三种模型方言的同一 golden AgentItem 轨迹、状态机性质、每个 intent/effect/settlement 崩溃点、恶意用户/技能/工具结果/子 run/MCP、L1 `triage.complete` 回放、L2 补偿序和 L3 审批消费。普通 `make test` 不得依赖公网；真模型成本与时延评测走独立发布门禁。

---

## 20. 字段级契约维护规则

1. 改网络 API 的行为或状态语义：先改本文档，再改 proto service，最后生成代码。
2. 改数据消息的字段或编码：先改 `proto/yufeng/*/v1/*.proto`，再同步本文相关表并执行 `make generate`。
3. 本文负责网络行为与状态语义，`proto/` 负责可编译的线上字段与编码；两者冲突时必须阻止合入并人工对齐，不允许任一侧自动胜出。

---

## 21. 数据面入口、覆盖度与证据

本节定义数据面入口、覆盖度、证据与异步模型旁路的网络和状态语义。字段已进 `proto/`（`IngressPosture`、`UnitListenPlan`、`EvidencePolicy`、`EvidenceDigest`、`ForwardPolicy`、`CheckTicket`）。活路径已按 Inspect/Gate 与覆盖度状态码落地；转发策略 `AGENT_INVESTIGATE` 由中台创建短命调查执行实例，不回改本次请求。术语见[术语表](glossary.md)中的检测器、闸、入口姿态、单元监听计划、证据策略、证据摘要、转发策略和检查票据。

### 21.1 入口姿态与单元监听计划

入口姿态闭集（线上全名 `INGRESS_POSTURE_*`，散文可用中文）：反代拦截、外部授权拦截、侧载只告警、镜像或 SPAN 只观察。

单元监听计划是单元作用域、已签名的制品：声明该 `unit_id` 的壳、流量键、监听地址、回源目标、跟随关系和 Edge 模型输入缓存窗口。**不**编进资产世代，资产世代出现 `KIND_LISTEN_PLAN` 必须拒绝整代。Brain 签发前必须把缺省窗口规范化为 4096 条、128 MiB、2 秒；Edge 不从空字段推断另一套运行默认值。

单元用自己的注册身份调用 `ArtifactService.ListUnitListenPlans(unit_id, since_version)`。服务端只返回该身份的目标单元，按 `version` 升序；每份计划的确定性 proto 字节（排除 `signature`）由制品签名根签名。边缘只接受目标 `unit_id` 相符、签名有效、`version` 严格递增且全部约束通过的计划；先将计划持久到本地缓存，再切换处理器。监听地址不变时原子替换处理器；监听地址变化时边缘以专用错误退出，由容器重启策略按新地址重新绑定，期间允许秒级中断。断网只有已验证世代与已验证监听计划两份缓存均存在才继续服务；坏签名、错单元或倒退版本均保留旧计划。首次启动没有已验证监听计划时，边缘不得开放业务监听。

首个企业试点由客户入口终止业务传输层安全协议（Transport Layer Security，TLS）；御锋处理已解密 HTTP，不持有业务证书或私钥。未解密 TLS 不进规范请求视图，HTTP 检查面为 `UNSUPPORTED`。控制面 TLS 参数只保护 brain 契约，不得复用为业务证书配置。

约束：

- 一个 `unit_id` 恰好一种壳；
- 同一流量键至多一个拦截单元；
- 一台机可以多个单元（例如反代 + 侧载）拉同一资产世代；
- 观察单元必须装完整世代，才能产出 `would_have_blocked`；
- `blocks_total` 只计真 403；自动晋升只数拦截单元。
- 反向代理姿态必须有绝对 `http`/`https` 上游 URL；外部授权姿态不得伪造业务上游；
- 监听地址只能来自已验证计划，验签前业务口不监听。

客户端来源策略同样属于单元监听计划：

- `trusted_proxy_cidrs` 为空时只信任 TCP 直接对端，忽略所有转发来源头；
- 直接对端命中可信 CIDR 时，只解析 `X-Forwarded-For`，把多个头行按 HTTP 顺序连接后从右向左剔除可信代理，第一个不可信地址是客户端来源；
- 任一空值、非法 IP、带端口值或非 IP 标识使整条 `X-Forwarded-For` 失效，回退直接对端；不写入日志；
- 首个试点不支持 `Forwarded`、自定义头或 PROXY protocol；
- 来源地址只送入 Coraza 连接信息，不进 `CanonicalRequestView`、`DetectionKey`、策略范围或世代摘要。

边缘上行前以 HMAC-SHA-256 对规范 IP 字节生成 `h1.<key-id>.<digest>`假名。密钥是每个部署作用域独立的 256 位随机秘密，只由文件挂载给边缘；重启不变。轮换时原子替换文件并重启边缘，新事件立即换用新 `key-id`，不双写、不保留新旧假名映射；因此轮换主动中断跨代关联。密钥及原始地址不进监听计划、资产世代、遥测、日志或模型投影。

403 只允许四件事同时成立：姿态允许拦截 ∧ 发布状态允许 block ∧ 策略或形状为 block ∧ 覆盖度满足 `coverage_requirement`。观察壳缺第一项，硬闸不得写 403。

心跳在已声明流量键上连续两个周期无请求、同键拦截单元在跑 → 资产健康 `tap_silent`。已声明跟随关系的观察流与拦截流，方法×路由模板 Top-N 集合 Jaccard < 0.5 且双方请求数都 ≥ 100 → `tap_skew`。镜像单元 `body_full_rate` 长期为 0 却报 HTTP `FULL` → 该面强制 `UNSUPPORTED`。控制台必须展示「执行面可能看不见」，不得写成「很安全」。

### 21.2 边缘观察、研判映射与覆盖度状态码

边缘观察与中台研判不是同一套枚举。线上只使用 `proto/yufeng/common/v1/v1.proto` 中的全名：

| 边缘观察 | 中台研判 |
|---|---|
| 存在检测键 | 主观察为 `OBSERVATION_STATE_SYNC_DETECTED`；无对应 enforce 策略时为 `TRIAGE_REASON_DETECTED_UNMITIGATED`，检测键不属自动治理五类时为 `TRIAGE_REASON_DETECTED_UNMAPPED`。检测键优先于覆盖不足，但覆盖度仍须随事件记录 |
| 无检测键，检测器加载、超时或执行失败 | `OBSERVATION_STATE_INSPECTION_ERROR` → `TRIAGE_REASON_DETECTOR_FAILURE`，不创建 Agent 指令 |
| 无检测键，必查面截断、部分解析或不受支持 | `OBSERVATION_STATE_INSPECTION_PARTIAL` → `TRIAGE_REASON_INSPECTION_INCOMPLETE`，不创建 Agent 指令 |
| 无检测键，配置要求的检查面均完整或不存在 | `OBSERVATION_STATE_SYNC_NO_DETECTION`；进入签名统计与代表选择，不创建 Agent 指令 |

`InspectionCoverage` 对路径、查询、请求体和请求头分别使用五态：`COVERAGE_STATUS_FULL` 表示完整检查，`COVERAGE_STATUS_PARTIAL` 表示截断或部分解析，`COVERAGE_STATUS_ABSENT` 表示该面不存在，`COVERAGE_STATUS_UNSUPPORTED` 表示引擎不支持，`COVERAGE_STATUS_ERROR` 表示解析或执行失败。`ABSENT`、`UNSUPPORTED` 和 `ERROR` 都不得伪装成“完整检查且无发现”。拦截姿态采用严格规范：覆盖不足不当 503，不当“无发现放行”。

| 覆盖情况 | 反代拦截 | 外部授权拦截 | 侧载只告警 | 镜像/SPAN |
|---|---|---|---|---|
| 正向命中，即使其余 `PARTIAL` | **403** | 网关 **403** | 200 + `would_have_blocked` | 只记事件 |
| 负向/完整性谓词且该面非 `FULL` | 该策略不参与 | 同左 | 同左 | 同左 |
| `ABSENT` | 不罚 | 同左 | 同左 | 同左 |
| `UNSUPPORTED` | 不 503；依赖该面的策略跳过 | 同左 | 同左 | TLS 未卸时 HTTP 面默认 `UNSUPPORTED` |
| 超体 → `PARTIAL` | **413**，不转发上游 | 网关传来超体：**403**。没传 body：`ABSENT`，跳过 body 策略，**200** | 200，标不完整 | 丢/记不完整 |
| `ERROR` / 规范视图 `Rejected` | **400**，不转发 | 已拿到的字节畸形：**403**。通道超时：仍 200 | 200，标 `INSPECTION_ERROR` | 记错误 |
| 引擎崩溃 / 无 `request_id` | **503** | 单次 200；窗内超时率熔断才 503 | 丢样本，禁止 503 伤业务 | 丢样本 |

合法大上传只能在世代里给**该路由**加大 `engine_body_limit_bytes`，禁止业务路径前缀隐式跳过。同步在途超过 `EdgeInFlight` 时，拦截壳对新连接 503（我们真过载）。

反向代理的转发结果闭集如下：检查放行后，上游 1xx/2xx/3xx/4xx/5xx 状态与响应体由代理透传；连接、域名解析或握手失败返回 502，响应不得暴露上游地址；同步在途超限在访问上游前返回 503。带升级头的请求先按普通 HTTP 请求检查方法、路径、查询与请求头；允许后可透传 101 升级和后续双向字节流，升级后的流不再属于 HTTP 检查覆盖面，不得宣称已持续检查。

Envoy 的 HTTP 外部授权请求必须保留原方法、路径与查询，发送 `Host`、`Content-Type`、`X-Forwarded-For`、`X-Forwarded-Proto` 和网关生成的 `X-Request-Id`，并在签名监听计划允许的可信代理网段内调用边缘。首个试点不向授权服务转发业务 `Authorization` 或 `Cookie`。请求体缓冲上限必须与 `EngineBodyLimitBytes` 相同；正文超过上限时，网关发送上限内前缀并置 `X-Envoy-Auth-Partial-Body: true`，边缘将正文面标为 `PARTIAL` 并返回 403。未发送正文时标为 `ABSENT`，依赖正文的策略跳过且返回 200。

### 21.3 外部授权半开熔断

常量：窗 `ExtAuthzTimeoutRateWindow`、跳闸 `ExtAuthzTimeoutRateTrip`、恢复 `ExtAuthzTimeoutRateRecover`、保持 `ExtAuthzTimeoutRateRecoverHold`、半开 `ExtAuthzHalfOpenPerSec`。

1. 熔断后默认 503（避免 50ms 窗口买裸奔）；
2. 每秒放行 `ExtAuthzHalfOpenPerSec` 个真请求进闸并记账；
3. 窗内超时率 ≤ 恢复阈值保持 `RecoverHold` → 合闸回失败即开；
4. 探测仍超时 → 重置保持计时，维持 503；
5. 禁止用合成健康检查当探测。

熔断后每秒放行半开真请求进闸并记账；滞回不再是死代码。

Envoy 的授权服务超时必须大于 `ExtAuthzTimeout`，参考配置为 100ms。边缘内部单次检测达到 50ms 时主动返回 200，因此仍是失败开放；边缘熔断后返回的 503 必须被网关拒绝，不能转成放行。网关不得启用 `failure_mode_allow`：边缘断连、连接超时或响应无效时，由网关返回 503，且不得访问业务应用；此处的失败关闭只表示授权服务不可用，不改变边缘内部单次检测超时的失败开放语义。

### 21.4 证据策略与摘要

`EvidencePolicy` 是世代成员，默认 `home`：

| 档 | 谁可以看见什么 | 进模型车道？ |
|---|---|---|
| `home` | Agent / 大语言模型：检测键、覆盖度、路由模板、参数名、长度、字符类、命中跨度的摘要。人：到该 `unit_id` 取证据环 | 否 |
| `private` | 在 `home` 之上：持 `evidence.pull` 且 Bindings 覆盖该资产的操作员，经审批拉结构化跨度。每次拉取写审计哈希链 | 否 |
| `break-glass` | 第二人授权 + 时限 + **仍只在该单元本地**取环。中台只记授权与摘要，不存原文 | 否 |

破窗：`evidence.break_glass`（默认角色没有）→ 另一用户批准 → 令牌绑定该单元与 `request_id` + TTL → 人到边缘管理面取环 → 审计谁申请、谁批、是否命中环。

流量审查扩展使用 `TrafficReviewPolicy` 与加密证据库：每候选最多 8 KiB、每案件最多五个样本和 40 KiB，总库默认 256 MiB、保存 24 小时。Jarvis 与短命 run 不读取原文；持 `evidence.approve` 且 Bindings 覆盖资产的用户可以批准一次敏感模型调用。批准界面必须展示实际模型主机、模型名、配置摘要、字段和字节预算。该通道不改变破窗人工本地取证语义，也不允许把 §21.5 的本地流量副本转发给 Brain。

`EvidenceDigestV1` 在世代签名范围内：

| 字段 | 说明 |
|---|---|
| `algorithm` | 闭集：`span_sha256` / `ngram3_hash` / `charset_hist` |
| `max_span_bytes` | 跨度上限 |
| `fields[]` | 如 method、route_template、selector、span_hash、charset_class |

换算法 = 新世代 + 重回放。证据环上限 `EvidenceRingMaxEntries` / `EvidenceRingMaxBytes`，先到先丢。Agent 研判票据只带与摘要同一函数的特征投影；模型旁路另按 §21.5 将有界正文所有权一次转移给邻近 ModelSide，禁止为 Brain 或智能代理再次复制。

### 21.5 Edge 邻近模型异步旁路

异步深度学习检测从已解密的 HTTP 请求副本开始。支持的入口只有反向代理、HTTP 外部授权和其它已经完成业务传输层安全协议终止并能提供规范请求字段的复制入口；不包含交换机镜像抓包、传输控制协议重组或加密流量解密。`yufeng-edge` 负责接收与规范化真实流量，`yufeng-modelside` 负责异步推理，Brain 只接收类型化结果。

#### 21.5.1 签名模型档案

每份可用于模型旁路的资产世代必须含恰好一份 `KIND_MODEL_PROFILE` 制品，载荷 `ModelProfile` 至少包含：

| 字段 | 约束 |
|---|---|
| `profile_id` / `model_group` / `model_type` / `model_version` | 均非空；结果必须原样引用 |
| `alert_threshold` | `[0,1]`，且严格大于 `review_floor` |
| `review_floor` | `[0,1]` |
| `review_window_seconds` | 正整数；0.1.0 基线由 Brain 签为 300 秒 |
| `max_review_per_unit` | 正整数；0.1.0 基线为每单元每窗 4 个代表 |
| `max_review_per_route` | 正整数；0.1.0 基线为同方法与路由每窗 1 个最高风险代表 |
| `dedupe_rule` | 0.1.0 只许 `MODEL_DEDUPE_RULE_METHOD_ROUTE_HIGHEST_SCORE` |
| `allowed_headers` | 规范化小写、去重升序；未列出的请求头禁止进入模型 |
| `max_body_bytes` | 正整数且不超过 Edge 同路由检查正文上限 |
| `review_new_routes` / `review_insufficient_coverage` | 是否因新路由或检查覆盖不足产生复核样本 |

阈值、采样窗、上限、去重规则、模型版本和可见字段全部来自已验签世代；ModelSide 缺档案、档案验签失败、模型版本不匹配或权重未成功装载时失败关闭该旁路并计数，不得使用进程内默认阈值继续上报。

新世代不得签发任何把模型推理交给 Brain 或通用 worker 的转发策略。`NONE` 只影响事件研判派发；`AGENT_INVESTIGATE` 仍只作用于脱敏 `CheckTicket` 和短命调查 run，不接收请求正文。

#### 21.5.2 `NormalizedTraffic` 与 Edge 队列

`NormalizedTraffic.schema_version` 当前固定为 `normalized-http/v1`，并至少包含：`request_id`、`unit_id`、`asset_id`、`generation_id`、`generation_seq`、`model_profile_id`、`model_profile_digest`、`method`、`route`、允许进入模型的请求头、查询参数、正文、`content_type`、原始 `body_length`、`body_truncated` 和逐检查面的 `coverage`。查询参数保留重复值与规范顺序；路由使用不含敏感值的模板。禁止携带 Cookie、Authorization、客户端原始地址、业务传输层安全协议密钥、Edge/Brain 凭据或未被签名档案允许的请求头。

请求路径在同步 Inspect/Gate 完成后，只尝试把规范视图持有的有界正文切片连同元数据**转移所有权一次**给 Edge 模型输入缓存窗口；成功后请求路径不得再读取或复制该切片。该正文不得为了 Brain、Jarvis、普通 Event、`CheckTicket`、日志或磁盘缓冲再次复制。窗口按条数、实际保留字节和排队年龄同时限界，排队与在途项都计入前两项；达到上限时请求路径在同一短临界区内淘汰最旧的可排队项，保留新流量。单项本身超过有效字节上限，或全部可排队项淘汰后仍因在途容量无法容纳时，丢弃新项并按原因计数。旁路关闭或任何丢弃都不得等待队列、执行推理、访问 Brain、同步写文件或等待任何消费者。

Edge 用一个后台批次组装器按相同模型档案聚合最多 32 条或 4 MiB，首项最多等待 10 毫秒；两个发送器并发调用 `ModelSideIngressService.SubmitTraffic`，单次调用超时 2 秒且不重试。批次编码后的请求必须低于 ModelSide 10 MiB 接收上限。`accepted + dropped == len(items)`；传输失败或 ModelSide 拒绝均视为至多一次丢失并分项计数。

ModelSide 输入端只保留不少于两个、默认不超过推理线程数两倍的浅层批次交接槽；一次提交的批次直接交给一个推理线程，不再拆成逐条业务队列。ModelSide 满载立即返回批次丢弃而不是拖住连接。Edge 的模型输入缓存窗口与 ModelSide 的结果队列彼此独立，任何一端满载都只丢旁路并计数，不改变当前请求裁决。旁路关闭、ModelSide 空闲或满载、Brain 断连、Brain 磁盘变慢时，同步请求路径均不得出现同步模型或存储依赖。

#### 21.5.3 ModelSide 推理、采样与结果队列

`yufeng-modelside` 是独立 Python 服务包与容器镜像；不进入 Go Edge 二进制，不拥有 Gate、工具、Agent、数据库或消息服务器权限。它按 `model_group + model_type + model_version` 加载签名档案指定的权重，推理只产生 `[0,1]` 风险分数和模型元数据，永不回调当前请求。

分类规则固定如下：

- `score >= alert_threshold`：生成 `MODEL_ALERT`，每条都尝试进入结果队列，不受复核采样上限限制；队列压力下先逐出尚未发送的 `REVIEW_SAMPLE`，仍无容量时丢弃告警并增加独立的高优先级丢弃计数；
- `review_floor <= score < alert_threshold`，或签名档案启用且命中新路由、检查覆盖不足条件：生成 `REVIEW_SAMPLE`；
- 其余结果只计推理指标，不上报。

复核采样按签名 `review_window_seconds` 使用事件时间窗。每个单元每窗最多 `max_review_per_unit` 个代表；同一 `method + route` 最多 `max_review_per_route` 个，0.1.0 的唯一去重规则只保留风险最高者。较高风险结果可在尚未发送前替换较低风险代表。ModelSide 负责第一层有界采样，Brain 在接收事务中按同一签名档案再次校验窗口、上限与去重，防止被篡改客户端放大。

ModelSide 将结果写入独立有界内存队列，由后台批量调用 `ModelResultService.UploadResults`。Brain 断连不停止本地推理；发送器指数退避，队列满时按上述优先级丢弃并计数。标准路径不把原始流量、结果队列或重试状态同步写磁盘，因此磁盘变慢不会反压 Edge；进程崩溃允许丢失尚未上报的旁路结果，不能为追求持久化破坏请求延迟边界。

#### 21.5.4 `ModelResultService.UploadResults`

`ModelResult` 只允许：`result_id`、`request_id`、`unit_id`、`asset_id`、`generation_id`、`generation_seq`、`kind`（`MODEL_ALERT` / `REVIEW_SAMPLE`）、`score`、模型档案标识与摘要、模型组/类型/版本、方法、路由、覆盖度、复核原因、`occurred_at`。它不得包含正文、查询参数值、请求头值、Cookie、Authorization 或任意原始流量。单批最多 100 条；响应满足 `accepted + deduped + rejected == len(results)`，并按 `result_id` 幂等。

ModelSide 以独立 `aud=modelside` 工作负载身份主动连接 Brain。`PutEdgeEnrollment` 事务预声明确定性的 `${unit_id}-modelside`，并把它绑定到精确单元与资产；首次合法结果批次把该身份钉到相互传输层安全协议客户端证书的 SHA-256 指纹，后续指纹不一致失败关闭。服务端逐条验证身份绑定、历史世代签名、模型档案摘要、模型版本、分数范围、分类阈值、采样窗和去重键；客户端自报的身份或类型不能产生新 Binding，也不能绕过阈值。生产跨主机连接必须相互传输层安全协议认证。

Brain 接受 `MODEL_ALERT` 时必须在同一 PostgreSQL 事务内：

1. 占用 `result_id` 幂等收据；
2. 创建不可变 `MODEL_ALERT` 事件；
3. 追加与该事件和签名档案绑定的 `ModelInference`；
4. 冻结不含原始流量的模型结果检查票据；
5. 聚类进现有开放案件或创建新案件；
6. 写事务发件箱；
7. 为冻结案件创建或唤醒一次安全研判指令。

任一步失败整条回滚，客户端保留重试。重复结果返回 `deduped`，不得重复事件、推理、案件、发件箱或 Agent 指令。

Brain 接受 `REVIEW_SAMPLE` 时执行同一身份、世代、档案、幂等与案件聚合校验，但只进入案件聚合和事务发件箱；不得为每条样本创建或唤醒 Jarvis 指令。达到案件级聚合门槛后的统一唤醒由案件协调器决定，不由单条上传触发。

#### 21.5.5 部署与性能验收

Edge 与 ModelSide 同机时默认连接 Unix 域套接字；套接字目录只允许对应服务账户访问。跨主机时两端必须使用相互传输层安全协议认证，服务端名称、证书用途和受信任证书机构均失败关闭，并用防火墙限制在同一受控防御网络。原始流量不得发送给 Brain，即使 Brain 与 Edge 同一物理节点也不例外。

发布门禁必须运行真实 Coraza Web 应用防火墙检查链，以旁路关闭为同进程基线，覆盖 ModelSide 空闲、稳定消费、满载和不可达四种旁路负载；每种负载还要交叉小正文、接近检查上限正文，以及 4096 条 / 128 MiB 默认窗口和 16384 条 / 256 MiB 本机默认硬上限窗口。Brain 断连和 Brain 磁盘变慢继续作为结果上报隔离场景，不得影响 Edge 请求路径。

每个组合由 64 个并发发生器在每秒 2000 个 HTTP 请求下预热后连续测量至少 60 秒并重复三次，记录计划请求、发生器丢失、实际吞吐、请求路径第 50 / 95 / 99 百分位延迟、Edge 进程中央处理器时间与常驻内存、窗口排队与在途条数和字节、最老排队年龄、各原因丢弃、ModelSide 批次深度及结果上报重试。发生器丢失必须为零；启用旁路相对关闭旁路基线的第 99 百分位延迟增量不得超过 1 毫秒，Edge 中央处理器占用增量不得超过 5 个百分点，Edge 常驻内存不得超过 512 MiB；同时不得观察到请求处理协程等待窗口、模型、Brain 或磁盘。

任一硬门槛失败即停止扩大进程内窗口的实现路线：不得通过放宽门槛、隐藏丢弃或增加请求路径阻塞规避；后续设计改为 Edge 外置消息队列，并重新评估原文边界、持久化语义与部署开销。
