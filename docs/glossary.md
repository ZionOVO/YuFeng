# 术语表（glossary）

> 全仓唯一术语出处。代码注释与各文档只做一句话级说明，名词的完整解释以本表为准；
> 注释里用 Go 文档链接指向本表（写法见 [AGENTS.md](../AGENTS.md) 第 4 节）。
> 每个词条带稳定英文锚点，改锚点前先全仓搜引用。

<a id="protocol-and-implementation-terms"></a>
## 协议与实现术语

下表只保留代码、线上字段、行业标准或上游产品要求使用的缩写。普通叙述优先使用中文全称；同一篇文档首次出现时写“全称（英文全称，缩写）”，后续才使用缩写。

| 缩写或名称 | 完整表述 | 在御锋中的含义 |
|---|---|---|
| API | 应用程序编程接口（Application Programming Interface） | 对外服务方法、请求字段与状态语义的总称 |
| RPC | 远程过程调用（Remote Procedure Call） | Connect 服务的方法调用方式 |
| HTTP / HTTPS | 超文本传输协议 / 经传输层安全协议加密的超文本传输协议 | 业务入口、上游回源和 Connect 调用的应用层协议 |
| IP | 互联网协议（Internet Protocol） | 地址、可信代理网段与来源解析使用的网络层协议 |
| ID | 标识符（Identifier） | 资产、单元、发布、会话和其他资源的稳定身份字段 |
| TLS / mTLS | 传输层安全协议 / 相互传输层安全协议 | 控制面加密，以及客户端与服务端双向身份验证 |
| JSON | JavaScript 对象表示法（JavaScript Object Notation） | 请求、工具参数和部分制品正文的文本编码 |
| NDJSON | 逐行 JavaScript 对象表示法（Newline-Delimited JSON） | 遥测缓冲和结构化日志使用的逐行对象编码 |
| URL | 统一资源定位符（Uniform Resource Locator） | 中台、模型端点和上游目标地址 |
| CIDR | 无类别域间路由（Classless Inter-Domain Routing） | 签名监听计划中的可信代理网段表达 |
| HMAC | 基于哈希的消息认证码（Hash-based Message Authentication Code） | 边缘生成不可逆、作用域隔离的来源假名 |
| SHA-256 | 安全哈希算法 256 位摘要（Secure Hash Algorithm 256-bit） | 内容地址、制品清单和证据摘要 |
| OWASP CRS | 开放全球应用安全项目核心规则集（Open Worldwide Application Security Project Core Rule Set） | Coraza 装载的固定检测规则集 |
| BPF / eBPF | Berkeley Packet Filter / 扩展 Berkeley Packet Filter | 后续运行时约束可选的内核机制 |
| TCP | 传输控制协议（Transmission Control Protocol） | 被动观察入口需要重组的传输层协议 |
| SSH | 安全外壳协议（Secure Shell） | 远程资产执行通道 |
| SQL | 结构化查询语言（Structured Query Language） | PostgreSQL 查询与结构化查询语言注入检测 |
| NATS / JetStream | NATS 消息服务器及其持久流子系统 | 中台受控域内的事务发件箱投递实现 |
| IPC | 进程间通信（Inter-Process Communication） | 监督进程与短命执行实例之间的本地套接字边界 |
| JWT | JSON Web Token | 访问令牌与能力令牌的传输格式 |
| RBAC | 基于角色的访问控制（Role-Based Access Control） | 只作默认授权模板；实际写权限仍由 Tools 与 Bindings 决定 |
| TTL | 存活时限（Time To Live） | 令牌、租约与临时修复的有效期限 |
| QPS | 每秒查询数（Queries Per Second） | 接口限流与吞吐测量单位 |
| SLO | 服务级别目标（Service Level Objective） | 延迟、吞吐与可用性预算 |
| WAF | 网络应用防火墙（Web Application Firewall） | 数据面流量检查产品类别；御锋以检查器与闸门拆分其检测和处置职责 |
| DSL | 领域特定语言（Domain-Specific Language） | 正向请求形状等受限规则表达，不允许任意程序执行 |
| LLM | 大语言模型（Large Language Model） | 中台模型网关调用的生成模型 |
| MCP | 模型上下文协议（Model Context Protocol） | 未来外部工具连接器的受控接入协议 |
| OSINT | 开源情报（Open-Source Intelligence） | 情报摄取组件的一类外部来源 |
| CVE | 通用漏洞披露编号（Common Vulnerabilities and Exposures） | 漏洞报告和情报制品中的公共漏洞标识 |
| SPA | 单页应用（Single-Page Application） | 中台在 `/app` 托管的控制台形态 |
| ORM | 对象关系映射（Object-Relational Mapping） | 架构负面清单禁止引入的数据库抽象 |
| OIDC / SSO | OpenID Connect 身份协议 / 单点登录（Single Sign-On） | 当前未实现的企业身份接入候选，不属于本地账户交付闭环 |
| CI | 持续集成（Continuous Integration） | 远端自动执行构建、测试与发布检查的流程 |
| CSS | 层叠样式表（Cascading Style Sheets） | 控制台视觉样式语言 |
| YAML / XML | YAML 配置编码 / 可扩展标记语言（Extensible Markup Language） | 部署配置、协议样本和解析差异语料可能使用的文本格式 |
| UTF-8 / ASCII | 八位统一码转换格式 / 美国信息交换标准代码 | 接口文本编码与审计标签字符范围 |
| RFC | 互联网征求意见文档（Request for Comments） | 协议规范及其编号的权威来源 |
| ADR | 架构决策记录（Architecture Decision Record） | `architecture.md` 中按顺序保存的稳定设计决定 |
| PR | 拉取请求（Pull Request） | 短命工作分支合入 `main` 的受审合并单元 |
| TLCP | 传输层密码协议（Transport Layer Cryptography Protocol） | 政企入口可先终止的国密传输协议 |
| SOC | 安全运营中心（Security Operations Center） | 控制台视觉主题和运营使用场景 |
| KEV / EPSS | 已知被利用漏洞清单 / 漏洞利用预测评分系统 | 历史产品构想中的漏洞情报来源与风险评分 |
| GOOS / GOARCH | Go 目标操作系统 / Go 目标处理器架构 | 零 C 语言互操作交叉编译使用的工具链环境变量 |
| GC | 垃圾回收（Garbage Collection） | 同进程异步工作可能影响请求路径延迟的运行时机制 |
| RPM | RPM 软件包管理器的软件包格式 | 非 Debian 系设备的冷补丁包格式 |
| NAS | 网络附加存储（Network-Attached Storage） | 历史产品构想中的无官方补丁设备示例 |
| WASM | WebAssembly | 尚未启用的动态装载候选机制 |
| NFS | 网络文件系统（Network File System） | 架构负面清单禁止分析器直连的共享存储 |
| CPU | 中央处理器（Central Processing Unit） | 执行实例与容器资源预算 |
| RAM | 随机存取存储器（Random Access Memory） | 容器内存和容量测量 |

<a id="connect-es"></a>
### Connect-ES

Connect 协议的 TypeScript 客户端生成与运行库。控制台后续用它生成类型安全客户端；当前手写适配层仍可交付。

安全检测类别 `sqli`、`xss`、`path_traversal`、`ssrf`、`cmdi` 与 `unmapped` 是线上闭集键，不是自由缩写；字段语义见 [`api.md`](api.md) 第 18.1.2 节。修复连续谱的 L0–L3 和自治等级的 T0–T2 分别见本表的[修复连续谱](#repair-continuum)与[自治等级](#autonomy-tier)。

## 平台角色

<a id="agent-role-layers"></a>
### 角色分层

“角色”必须按下列三层使用；同名字符串也不代表同一权限空间。

| 层次 | 当前命名 | 权威载体 | 授权含义与状态 |
|---|---|---|---|
| 平台账户角色 | `USER_ROLE_ADMIN` / `USER_ROLE_OPERATOR` / `USER_ROLE_VIEWER` | proto 的 `UserRole` | 封闭枚举，只决定**默认 Tools 模板**；写操作还要授予表展开的 Tools × Bindings，空 Bindings 拒绝写（引导未完成时 `docs/api.md` §19.5 白名单除外） |
| Agent 授予模板 | `orchestrator` / `worker` | `lib/kernel.Claims.Role` | 由中台签发；开放字符串，只提供默认模板，实际权限由 Tools、Scopes、Bindings、MaxCalls 决定 |
| 执行实例岗位 | `triage` / `investigator` / `verifier` / `strategist` / `operator` / `reporter` | `agents/roles` 目标词表 | 描述执行实例内的工作分工；当前尚未类型化，也未形成运行时授权依据 |

因此，平台账户的 `USER_ROLE_OPERATOR`、Agent 授予模板和执行实例岗位中的 `operator` 不得互换；代码、接口与文档都必须写出所属层次。人与 Agent 的写路径共用同一套格子，不另建对象级权限产品。

<a id="kernel"></a>
### 治理内核（kernel）
中台里不可插件化的核心：制品签名与验证、能力令牌签发与记账、治理管道状态机、审计哈希链、凭据保险库。它是全系统唯一信任根——这些职责交给任何插件，插件就能替换信任本身。

<a id="jarvis"></a>
### 贾维斯（Jarvis）
运行在中台服务器或独立主机上的长驻编排 Agent 进程（虚拟安全工程师），与后端只通过网络契约交互。只编排不修复：轮询指令、读态势、派生执行实例、提交研判结论与报告、经 `approval.request` 请人审批。每个动作过能力令牌。网络上**只允许连接** `yufeng-brain`：不持模型密钥、不直连公网模型、不持 Docker 套接字、不改主机防火墙、不直连边缘或被保护资产，也不安装、启动、升级、卸载或探测 `yufeng-edge`。

<a id="managed-agent-profile"></a>
### 受管 Agent 档案（Managed Agent Profile）
控制台“Agent 管理”中由用户创建的短命分布式调查主体，拥有稳定 `agent_id`、显示名、启停状态、受限工具集、精确资产 Bindings 和配置摘要。首版种类固定为 `traffic_review`，只消费 brain 已形成的流量审查案件，不能读取 PostgreSQL、边缘原始证据或任意事件流；证据仍须案件绑定的一次性人工批准。Agent 不对应长驻网络进程，也不是 worker 或贾维斯副本；Brain 按资产匹配并冻结其配置，Jarvis 只负责编排，agentd 再启动使用该身份和冻结配置的短命 `yufeng-run`。模型、工具、结论、run 与审计均归属该 Agent。删除采用墓碑停用，只阻止新委派，不删除或改写历史案件、活动、run 或审计记录。

<a id="run"></a>
### 执行实例（run）
由独立监督进程 yufeng-agentd 按任务孵化的一次性执行进程：委托书（修复计划或程序引用 + 目标 + 约束）+ 工具投影 + 预算 + 存活时限，做完即焚。run 不持访问令牌、刷新令牌、客户端证书或能力令牌，也不建立网络连接；只通过已连接的本地监督代理请求模型、工具与回执。程序中途失败由实例内的操作员岗位分析上报；不可逆动作永远回治理审批。

<a id="strategist"></a>
### Strategist
产出修复计划的 Agent 角色。决策写成纯函数：给定漏洞行为特征、资产能力矩阵与攻击压力，在枚举空间内选出最快的多层组合拳。

<a id="capability-token"></a>
### 能力令牌
brain 为每条 Agent instruction 或 run work item 签发的短期最小权限声明；没有它，Agent 与 run 的生成、工具调用和 Turn 推进一概拒绝。声明约束角色模板、工具白名单、对象范围、短期调用上限、租约 epoch 与权威预算账户引用，以 Ed25519 私钥签名并按 JSON Web Token（JWT）格式流转。`sub` 表示被授权的 Agent 或 run；`azp`（authorized party）表示实际领取租约并持访问令牌的 Agent/worker 进程；双令牌调用必须验证访问令牌 `sub` 等于能力令牌 `azp`。`jti` 只标识令牌实例与吊销，不能替代跨续租的 `budget_id` 或业务幂等键。登录用户不持 Agent 能力令牌，而是用用户会话 + GrantService 展开的 Tools × Bindings；人与 Agent 共用的是授权格子，不是同一种凭证。

<a id="workload-identity"></a>
### 工作负载身份（Workload Identity）
独立服务连接 brain 时使用的进程身份，与贾维斯的 Agent 身份分开。agentd 的一次性 bootstrap token 绑定精确 `worker_id + RUN_SUPERVISOR + 公钥/客户端证书指纹`；注册后换取 `aud=worker` 的可轮换 refresh token 和短期 access token，只有代持当前 run 能力令牌时可代表 run 调 Generate / ToolGateway。`yufeng-modelside` 使用独立 `aud=modelside` 身份；部署规格确定性预声明 `${unit_id}-modelside` 并绑定精确单元与资产，首次合法上报再固定客户端证书指纹，只能上报类型化结果。自报能力、身份或种类不能产生 Bindings。

## Agent 座架

<a id="agent-harness"></a>
### Agent 座架（Agent Harness）
把模型、工具、技能、持久上下文、租约、预算、审批、委派和崩溃恢复组合成自主认知循环的基础设施。御锋座架由独立 `agents/runtime` 壳层和 brain 权威账本共同构成；模型摘要或进程内存都不是恢复真相。它不同于修复 Procedure 的确定性执行器，也不同于只做 `messages[] → text` 的聊天补全。

<a id="agent-thread"></a>
### AgentThread
一个来源下的持久认知上下文。来源只钉死用户 `session_id`、研判 `cluster_id` 或认知型 `run_id`；会变化的消息、聚类和工作游标属于 AgentTurn。AgentThread 不等于 `SessionService` 用户会话。

<a id="agent-turn"></a>
### AgentTurn
一次输入驱动的完整处理，钉死 `message_sequence`、`cluster_version/event_cutoff` 或 `work_id + plan_digest`。Turn 可跨进程和租约恢复，等待时释放租约；完成、失败、取消与结果未知是终态。

<a id="agent-step"></a>
### AgentStep
Turn 中一次逻辑模型生成及其工具调用批次的结算边界。steering、follow-up、压缩和撤销只在无在途生成的 step checkpoint 生效。

<a id="agent-item"></a>
### AgentItem
Agent 认知账本里的有序、只追加记录，承载输入引用、模型请求/响应、工具调用与结算、用户输入、子 run/审批结果、压缩和终态摘要。`item_sequence` 是执行条件写入序号；用户并发输入另有独立 `input_sequence`。

<a id="lease-epoch"></a>
### 租约所有权纪元（lease_epoch）
隔离旧执行者的单调栅栏。初次领取及释放后的重新领取才递增；同一持有者正常续租只延长到期时间，不改变 `lease_id` 或 epoch。任何旧 epoch 的账本写、模型生成或工具调用都失败关闭。

<a id="agent-budget"></a>
### Agent 预算账户（budget_id）
Turn 或 run 的持久额度账户，约束步骤、模型调用和 token、工具调用和结果字节、成本、活动时间与墙钟截止时间。能力令牌只引用账户；换令牌、续租、恢复、压缩和重试不能重置额度。模型 attempt、工具和子 run 都先预留、后结算。

<a id="model-generation-attempt"></a>
### ModelGeneration 与 ModelAttempt
ModelGeneration 是一次逻辑生成，transcript 至多接受一个响应；ModelAttempt 是一次物理供应商调用，分别记录 intent、出网边界、usage、预算和结果。供应商不支持幂等时重试可能重复请求或计费，因此御锋不承诺物理出网恰好一次，只承诺所有尝试可对账。

<a id="supervisor-broker"></a>
### 本地监督代理（supervisor broker）
yufeng-agentd 向其孵化的 run 提供的本地进程间通信边界。agentd 独占网络凭证和工作项能力令牌，按已连接文件描述符绑定 `work_id` 代理 Generate、工具和回执；丢租约即关闭代理并终止子进程树。它不是网络服务，也不是 run 的第二个网络对等体。

<a id="skill"></a>
### 技能
签名、可激活、可撤销的渐进披露能力制品，由技能清单、正文和内容地址资源组成。技能提供知识、工作法和工具建议，只能收窄当前能力令牌，不能补权；正文不得由智能代理当脚本执行，可执行步骤必须成为签名修复程序。

<a id="mcp-connector-bridge"></a>
### 模型上下文协议连接器桥（MCP connector bridge）
brain 对模型上下文协议（Model Context Protocol，MCP）服务器的受控适配边界：只发布审核后的内部工具描述，隔离连接器凭据，检查数据外发与 Schema 漂移，并把外部结果标为不可信。Agent 不直连任意 stdio 或远程 MCP 服务器。

## 修复体系

<a id="repair-continuum"></a>
### 修复连续谱（L0–L3）
| 层 | 手段 | 能修什么 | 授权 |
|---|---|---|---|
| L0 | 只出报告 | 一切（不修） | 自动 |
| L1 | 虚拟补丁 · 流量拦截 | 检测键策略；仅带证据的漏检走形状语言 | 精确且证据合格的策略可门槛自动；形状、未映射、宽范围必须另一用户推进 |
| L2 | 虚拟补丁 · 运行时约束 | "系统调用行为"类 | 沙箱验证 + shadow 后自动 |
| L3 | 冷补丁 | 一切 | 永远人工授权 |

每层只表达漏洞行为的一个子空间；组合拳由 Strategist 决策。

<a id="autonomy-tier"></a>
### 自治等级（T0–T2）

| 等级 | 自动执行边界 | 必要约束 |
|---|---|---|
| T0 | 报告、观察和只读调查 | 不产生修复副作用 |
| T1 | 可逆的流量拦截修复 | 短存活时限、先观察、秒级回滚；自动晋升还要求精确或路由范围、合格证据与回放覆盖 |
| T2 | 可逆的运行时约束修复 | 短存活时限、沙箱验证、先观察、秒级回滚 |

自治等级描述允许自动执行到什么程度，不替代[修复连续谱](#repair-continuum)。冷补丁属于不可逆高风险动作，始终要求人工授权。

<a id="cold-patch"></a>
### 冷补丁
直接修复业务软件的真补丁：先出报告，人工授权后执行，可含重启。

<a id="virtual-patch"></a>
### 虚拟补丁
不改动目标软件的临时修复，分两种：流量拦截（L1）与运行时约束（L2）。带存活时限，被冷补丁取代后自动退休——虚拟补丁的生命周期管理本身就是产品。

<a id="repair-plan"></a>
### 修复计划（RepairPlan）
针对一个漏洞发现的多层修复动作组合（L1/L2/L3 各若干）。每动作声明修复面、制品引用、目标生效状态、存活时限、是否需人工授权与取代指针；决策叙述（rationale）是人审时的主要阅读材料。

<a id="procedure"></a>
### 修复程序（Procedure）
在一种设备上安全执行修复或施加约束的程序：信封（目标画像、前置要求、回滚引用，proto 强类型）+ 步骤体（JSON 数组，按 procedures/steps.schema.json 校验）。步骤可标记"须人工确认"。新设备族 = 新程序包，平台零发版。

<a id="primitives"></a>
### 能力原语
宿主程序提供的最小受控操作。首版正式闭集是 `sys.probe`、`artifact.stage`、`file.atomic_replace`、`service.reload`、`verify.file_digest`、`verify.service_active`；每个原语按命令租约记录意图、副作用边界、结算和补偿回执。`exec`、包管理、任意网络访问、自升级和远程执行不在交付能力内。原语集合年级别才变一次；变化更快的步骤参数与组合写进已签名制品。

<a id="access-mode"></a>
### 资产接入模式（embedded / remote / network）
资产消息为兼容历史客户端保留三个枚举值；正式执行能力只有 `embedded`：Linux/OpenWrt 资产安装非特权 `yufeng-host`，执行六个本机白名单原语。`remote` 与 `network` 不产生 Host 执行能力；旁路资产只能由 `yufeng-edge` 获得第一层流量保护。安全外壳协议、厂商接口和远程自动安装不在首版范围。

<a id="effect-semantics"></a>
### 生效语义（live / spawn-time）
手段何时对目标进程起作用：live = 对运行中进程立即生效（eBPF、nftables）；spawn-time = 仅进程启动时生效（seccomp、LD_PRELOAD），只适用于可重启场景。免重启承诺只由 live 手段承担。

## 制品与治理

<a id="artifact"></a>
### 制品（Artifact）
签名的数据包：策略、形状规则、画像、修复程序、技能、工具描述、eBPF 目标文件、seccomp 配置、nftables 规则、检测器清单、规范化配置档。身份 = 内容寻址：门禁通过后，对排除 id 与 signature 的确定性 proto 序列化取 sha256；取代关系为单指针；发布前必须过回放门禁。边缘装载的原子单位是资产世代，不是单条发布。一切易变物走制品，代码面保持小而稳。

<a id="release"></a>
### 发布（Release）
一次制品的治理生命周期实体：从 ProposeArtifact 生成 release_id 开始，经 draft→signed→shadow→canary→enforce→retired。release_id 是治理 API、统计、时间线与取代关系的主键；artifact_id 是签名信封的内容地址。

<a id="artifact-pipeline"></a>
### 治理管道
制品的发布流水线：提案意图 → 服务端编译 → 验证（沙箱演练 / 回放门禁）→ shadow → canary → 编入资产世代 → 监控 → 回滚或退休。仅精确检测键策略可门槛自动推进；形状规则与宽范围策略必须另一用户推进。全流程进审计哈希链。

<a id="release-modes"></a>
### 生效状态（shadow / canary / enforce）
制品发布的三档：shadow 只记录不生效（观察误伤）；canary 按稳定分片小比例生效（默认键为单元标识，禁止用单次请求随机标识分桶）；enforce 全量生效。治理管道无论目标为何都从 shadow 起步。

<a id="ledger"></a>
### 三本账
三本持久账本：事件账（不可变、追加式）、资产账（units/assets/unit_assets）、发布账（release 生命周期 + 制品内容寻址）；审计哈希链贯穿三者。事件账是全系统改起来最贵的结构。

<a id="contracts"></a>
### 五契约
五种稳定的治理网络交互类别：前四种是中台与两类边缘之间的注册（身份/能力矩阵/心跳）、制品下发、遥测上行和指令执行（逐步回执）；第五种是人—控制台—贾维斯会话，由 `SessionService` 实现。Agent 控制面是服务族，其中 `AgentControlService`、`RunService`、`WorkerService` 与 `ToolGatewayService` 不计入五种边缘契约。Edge 与 ModelSide 的规范流量入口和 ModelSide 到 Brain 的结果上报属于数据面内部模型旁路，不得复用 run `PollWork` 的载荷，也不改变治理契约类别。边缘只说 HTTP。

<a id="resource-flow"></a>
### 资源流转（Resource Flow）
边缘生产、中台投影与路由、受授权消费者领取的完整治理路径。`yufeng-brain` 是唯一治理投影者和路由器：边缘的 `UploadEvents` 没有下一跳；贾维斯和调查 run 不订边缘，也不彼此订阅。原始流量是唯一例外，只能从 Edge 经有界旁路进入邻近 ModelSide；ModelSide 再把无原文结果交回 Brain。多站点关联只通过事件账、[攻击活动](#campaign)和新资产世代完成。

<a id="producer-capabilities"></a>
### 生产能力广告（Producer Capability Advertisement）
单元在注册与心跳时声明自身能产生的关键事件、普通采样、票据所需特征、投影版本、入口姿态、传感类型、编译期模块协议能力和容量上限；缓冲、丢弃与投影失败另作生产健康。广告只供 brain 做兼容性检查、世代编译与模块目录激活，不能改变资产绑定、Tools、Bindings、发布范围或任何消费者读取资格；消费可见性只由服务端 worker / Agent 档案、Tools × Bindings、投影和租约决定。

<a id="edge-units"></a>
### 两类边缘
数据面单元：钉在入口机，制品驱动、遥测上行、断网自治（缓存制品继续拦截）。执行单元：钉在被保护设备，指令驱动、逐步回执。两者都不需要中台在线才能工作。

<a id="unit"></a>
### 单元（Unit）
运行 yufeng-edge 或 yufeng-host 的进程身份，拥有持久化 unit_id、心跳状态与注册令牌。单元与资产是两个概念：unit_assets 表达绑定关系；v1 每单元绑定一个主资产，schema 支持一单元多资产。

<a id="user-account"></a>
### 用户账户（User Account）
操作域登录身份：v1 为用户名 + 密码 + 固定平台账户角色（`USER_ROLE_ADMIN` / `USER_ROLE_OPERATOR` / `USER_ROLE_VIEWER`），登录后持用户会话令牌调用控制台与治理接口。用户管理只属于 `USER_ROLE_ADMIN`；对象级授权已经统一为 GrantService 展开的 Tools × Bindings，不靠角色名放行。Agent 审批的暂停、决定与一次性消费仍按 `docs/api.md` §18.5.1、§18.10.7 的目标契约实施。

<a id="detection-key"></a>
### 检测键（DetectionKey）
一条同步发现的精确定位：检测器标识与版本、清单摘要、规则标识、阶段、目标位置、参数选择器、规范化配置档摘要。策略匹配只认检测键，不认五类攻击标签。攻击类只用于控制台、聚合和 Agent 技能选择。

<a id="inspection-coverage"></a>
### 检查覆盖度（InspectionCoverage）
对路径、查询、请求体、请求头分别声明本次检查到了哪里：`FULL` 完整、`PARTIAL` 截断或部分解析、`ABSENT` 该面不存在、`UNSUPPORTED` 引擎不支持该面、`ERROR` 解析或执行失败。`ABSENT` / `UNSUPPORTED` / `ERROR` 不得记成「无发现」。负向或完整性谓词必须 `FULL` 才可拦截。拦截姿态下超体与畸形请求的状态码见 `docs/api.md` 第 21 节，不得把覆盖不足当成「无发现放行」。

<a id="asset-generation"></a>
### 资产世代（AssetGeneration）
针对单一资产的原子发布单位：单调序号、检测器清单、规范化配置档、策略集、形状规则、分类映射器、[采样策略](#sampling-policy)、[证据策略](#evidence-policy)、[证据摘要](#evidence-digest)、[转发策略](#forward-policy)、最低边缘版本与签名。边缘验签并编译成功后原子替换 `activeGeneration`；任一 enforce 依赖失败则保留上一份已验证可用世代。禁止「新策略 + 旧检测器」的混合状态。入口姿态不进世代，见[单元监听计划](#unit-listen-plan)。

<a id="http-inspection-profile"></a>
### HTTP 检查配置档（HttpInspectionProfile）
冻结四种入口壳共用的 HTTP 规范化规则（路径、百分号解码、重复查询键与头、`Content-Length` 与 `Transfer-Encoding` 冲突、JSON 重复键、multipart、解压上限等）。反代拦截、外部授权拦截、侧载只告警、镜像或 SPAN 只观察都必须先变成同一规范请求视图（CanonicalRequestView）再进入边缘核。不是「只有反代与外部授权两种入口」。

<a id="request-shape-dsl"></a>
### 请求形状语言（RequestShape DSL）
漏检旁路闸使用的受限正向描述：方法、路由、参数选择器、长度与字符类。由中台编译，过宽或不稳定的表达式被拒绝。生产不得接受任意正则。仅当研判原因是带独立证据的 `SUSPECTED_MISS` 时才允许提案。

<a id="proposal-intent"></a>
### 提案意图（ProposalIntent）
操作域用户或 brain 确定性协调器提交给治理内核的类型化编译入口，不是制品字节。生产 Agent 不直接填写 ProposalIntent；它只提交 [TriageDecision](#triage-decision)，协调器再从可信账本派生提案人、资产、检测键、检测器摘要、证据引用与范围，编译为策略或形状规则。操作域用户提供的检测键和形状也只是待验证断言，必须与聚类钉死版本一致；客户端不得自由提交 `created_by`、可信摘要、证据引用或任意 JSON 制品。

<a id="triage-decision"></a>
### 研判结论（TriageDecision）
生产研判 Agent 经 `triage.complete` 提交的非可信认知结果：聚类标识、处置建议、理由与可选形状草案。它不能携带可信检测键、资产、检测键目标选择器、可信证据、创建主体、范围风险或证据类；形状草案中的参数选择器也只是待验证断言，必须存在于钉死投影。brain 的确定性协调器校验后决定只报告、升级人工或编译 ProposalIntent。

<a id="triage-reason"></a>
### 研判原因
中台对边缘观察的解释，与边缘观察状态、检查覆盖度五态都不是同一套枚举。**线上 / JSON 只许 proto 全名** `TRIAGE_REASON_*`。边缘观察：`SYNC_DETECTED` / `SYNC_NO_DETECTION` / `INSPECTION_PARTIAL` / `INSPECTION_ERROR`（可叠加）。中台研判：`TRIAGE_REASON_DETECTED_UNMITIGATED`（有键无闸）、`TRIAGE_REASON_DETECTED_UNMAPPED`（有键但不属五类，默认只出报告）、`TRIAGE_REASON_SUSPECTED_MISS`（无键但有独立证据）、`TRIAGE_REASON_INSPECTION_INCOMPLETE` / `TRIAGE_REASON_DETECTOR_FAILURE`。散文可省略前缀，门禁与代码不得用短名。有键时 `SYNC_DETECTED` 优先于覆盖度。普通 `SYNC_NO_DETECTION` 只采样入账，不创建 Agent 指令。映射与独立证据闭集见 [`api.md`](api.md) 第 18.1.2 节。

<a id="evidence-class"></a>
### 证据类（evidence_class）
策略自动晋升的证据来源：`crs_mapped` / `crs_unmapped` / `human` / `replay` / `intel` / `model`。只有 `crs_mapped`、`human`、`replay` 可与 `scope_risk ∈ {exact, route}` 以及合格回放覆盖度联合自动晋升。

<a id="taxonomy-mapper"></a>
### 分类映射器（TaxonomyMapper）
世代内已签名制品，把原始规则标识映到闭集 `sqli` / `xss` / `path_traversal` / `ssrf` / `cmdi` / `unmapped`。边缘与模型 worker 禁止手写正则贴类。`REQUEST-91x` / `920` / `913` 不得标成可自动治理类。

<a id="canonical-request-view"></a>
### 规范请求视图（CanonicalRequestView）
任一入口壳按同一 HTTP 检查配置档解析后的请求。[检测器](#inspector) 与[闸](#gate) 只接受该视图及与之并列的[客户端来源](#client-source)元数据。四种壳必须先变成同一视图再进核；来源不参与规范化摘要或检测键。

<a id="client-source"></a>
### 客户端来源（Client Source）
边缘从 TCP 直接对端和已签名可信代理策略确定的规范 IP。它只作检测元数据：Coraza 可用于连接信息，上行事件只保留 HMAC 假名；原始地址不进检测键、日志、账本或模型投影。

<a id="grant-missing"></a>
### 授予缺失（grant_missing）
人与 Agent 的写路径未命中授予表，或 Bindings 为空。返回 `permission_denied`，细节 `grant_missing`，不写 draft、不扣预算。

<a id="outbox"></a>
### 事务发件箱（outbox）
与业务行同一数据库事务写入的待投递记录。独立循环写入 NATS JetStream 持久流；消费者按 `dedupe_key` 幂等。禁止先入账再尽力发。

<a id="scope-risk"></a>
### 策略范围风险（scope_risk）
策略自动晋升用的范围档，与自治分级 T0–T2 不是同一轴。只描述铺开半径，不是完整晋升档。自动生效还必须同时满足证据类（`crs_mapped` / `human` / `replay`）与回放覆盖度。`prefix` / `asset_wide` / `class_only` 以及 `crs_unmapped` / `model` 必须另一用户推进或直接拒收自动通道。与自治分级 T0–T2 / L3 不是同一轴。

<a id="review-hard-expiry"></a>
### 复核时间与硬过期
`review_at` 到点只产生复核任务，默认不自动卸闸。`hard_expires_at` 才按声明的过期行为卸闸或告警后保留。禁止用单一存活时限同时表示复核与失效。

<a id="signer"></a>
### 签名器（Signer）
治理内核对外的签名接口：只签已通过确定性校验的结构体和能力令牌。生产私钥由密钥管理服务、公钥密码标准 11 或独立套接字持有，`yufeng-brain` 进程不直接读取私钥文件。

<a id="detector-tiers"></a>
### 检测器档位（五层眼睛）
检测方法按代价分五层，不是「同步 / 异步」两档旗标：

1. **同步**：[检测器](#inspector)，`yufeng-edge` 进程内纯 Go，微秒档，计入第 99 百分位额外延迟预算；唯一默认可影响本次请求，且必须再过[闸](#gate)。
2. **[本地异步旁路](#local-async-bypass)**：请求路径只做非阻塞所有权转移与排队，微秒档，不等待消费者。
3. **Edge 邻近深度学习**：[异步检测执行实例](#async-detection-worker)按签名模型档案推理，毫秒到秒，只影响后续案件。
4. **大语言模型复核**：只在案件审批后由短命调查 run 经 Brain 模型网关调用，秒档；禁止读取未批准原文或回改当前请求。
5. **调查执行实例**：brain 创建、`yufeng-agentd` 孵化的短命 `yufeng-run`；只读投影，不回改本次请求。

大模型永远不出现在同步档。异步结果永远够不到本次请求。

<a id="inspector"></a>
### 检测器（Inspector）
数据面进程内同步眼睛：只对规范请求视图及与之并列的客户端来源元数据产出发现（完整检测键）与每面检查覆盖度，**不**返回拦截动作。接口刻意不进 proto。新实现必须经编译期注册表与世代清单点名才能装载。活路径若仍是返回 `Action` 的 `Detector`，那是待拆的旧接口，不是目标。

<a id="gate"></a>
### 闸（Gate）
唯一能对本次请求给出处置动作（含 403）的纯函数。只认当前 `activeGeneration` 内已签名的检测键策略、仍在役演示规则旁路表与形状规则，并与[入口姿态](#ingress-posture)、发布生效状态、检查覆盖度合取。引擎命中本身不是闸。

<a id="ingress-posture"></a>
### 入口姿态（IngressPosture）
这个进程怎么挂到线上：反代拦截、外部授权拦截、侧载只告警、镜像或交换机端口镜像（SPAN）只观察。不回答策略是否全量生效。观察姿态永远不得回 403。编进[单元监听计划](#unit-listen-plan)，不进资产世代。首个企业试点中的姿态都只处理客户入口已解密的 HTTP；业务 TLS 证书和私钥不属于入口姿态配置。

<a id="unit-listen-plan"></a>
### 单元监听计划（UnitListenPlan）
单元作用域的签名入口合同，包含单调版本、目标单元、入口姿态、流量键、监听地址、回源目标、跟随关系与可信代理 CIDR。单元用自己的注册身份通过 `ArtifactService.ListUnitListenPlans` 逐版拉取，验签且全部约束通过后原子替换。它不进资产世代；世代含 `KIND_LISTEN_PLAN` 时拒绝整代。一台机可以「反代 + 侧载」并存、拉同一资产世代。同一流量键不得两个拦截单元；一个单元标识不得两种壳。签的是「壳该怎么挂」，签不了「网关真的把流量拷过来了」。

<a id="sampling-policy"></a>
### 采样策略（SamplingPolicy）
brain 从资产标签与部署档案编译、随资产世代签名下发的普通流量处理规则。支持 `TrafficReviewPolicy` 的 Edge 以完整计数和有界代表替代逐条随机入账；关键事件仍保证完整计数。资产标签只影响下一份世代，不能成为运行中的未签名开关；`NoDetectionSampleRate=1%` 只供旧 Edge 兼容，不是第二个权威配置源。

<a id="evidence-policy"></a>
### 证据策略（EvidencePolicy）
世代成员，三档：`home`（默认）/ `private` / `break-glass`。默认只允许脱敏投影；`private` 须持 `evidence.pull` 且绑定覆盖该资产，经审批拉结构化跨度。流量调查还允许持 `evidence.approve` 的用户对单案件批准一次敏感模型调用，原文只经 brain 有界内存中继到当前模型槽，不进入中台持久化。破窗仍须第二人授权、时限，且只在该单元本地取证据。

<a id="traffic-review-policy"></a>
### 流量审查策略（TrafficReviewPolicy）
资产世代内签名的流量统计与代表选择上限：五分钟窗口、以有界重频算法维护的近似前 32 个方法×路由组合、每单元每窗最多四个候选、每候选 8 KiB 证据，以及证据有效期和库容量。启用档位为 `OFF`、`STATISTICS_ONLY`、`REDACTED_CASES`、`EVIDENCE_ON_APPROVAL`、`SHADOW_CANDIDATES` 的闭集，只允许逐级扩大功能，任意降档可以立即执行。它只控制生产量与证据留存，不能指定消费者或授予权限。

<a id="investigation-case"></a>
### 调查案件（InvestigationCase）
绑定单一资产的 Agent 工作单位，冻结模块、聚类版本、候选摘要、优先级和活动时间线。跨资产相似性只能建立攻击活动关联；案件的读取、审批和处置始终按本资产的 Tools × Bindings 裁剪。

<a id="evidence-digest"></a>
### 证据摘要（EvidenceDigest）
世代签名范围内的研判摘要函数：算法闭集（`span_sha256` / `ngram3_hash` / `charset_hist`）、跨度上限与字段列表。换算法等于新世代并重回放。Brain 与 Agent 的检查票据只带与此同一函数的特征投影；Edge 邻近模型旁路使用另一份签名模型档案约束可见字段。

<a id="forward-policy"></a>
### 转发策略（ForwardPolicy）
世代成员，选择已入账脱敏事件是否进入 Agent 调查：新世代只许 `NONE` 或 `AGENT_INVESTIGATE`。它不包含具体 worker 地址、订阅主题或数据库定位符，也不负责模型推理。Edge 邻近模型旁路由同一世代中的[签名模型档案](#signed-model-profile)约束。

<a id="check-ticket"></a>
### 检查票据（CheckTicket）
事件的完整脱敏研判投影，不是模型流量契约。Brain 接受 Event 时，必须使用该 Event 与其钉死的历史资产世代确定性冻结票据，并与事件账和发件箱同事务提交；缺少投影材料进入可审计隔离，禁止回退固定摘要或让消费者只拿 `event_id` 回库取字段。供案件聚合、贾维斯与调查执行实例消费；不含原始请求和单元运维日志。

<a id="signed-model-profile"></a>
### 签名模型档案（Signed Model Profile）
随资产世代签名的 ModelSide 运行合同，固定模型组、类型、版本、告警阈值、复核下限、采样窗口、每单元与每路由上限、去重规则、允许进入模型的请求头及正文上限。Edge 与 ModelSide 只认验签档案，不能以环境变量或进程默认值改写这些策略。

<a id="normalized-model-traffic"></a>
### 规范化模型流量（Normalized Model Traffic）
Edge 从已解密 HTTP 请求副本构造的版本化模型输入，包含请求归属、世代、方法、路由、签名档案允许的头和查询参数、有界正文、内容类型、原始正文长度、截断状态与检查覆盖度。正文所有权只从请求视图向本地模型队列转移一次；该合同不得发送给 Brain、贾维斯或调查 run。

<a id="triage-object"></a>
### 研判对象（TriageObject）
brain 从钉死聚类版本冻结给贾维斯的只读投影，包含研判原因、代表票据、聚合计数、检测与世代轨迹以及已追加推理记录。生产贾维斯通过 `triage.get` 读取它，不使用通用 `event.get/list`；它不含原文、证据环定位能力或单元日志与指标。

<a id="local-async-bypass"></a>
### 本地异步旁路
请求路径把[规范化模型流量](#normalized-model-traffic)交给 Edge 本地有界非阻塞队列，再由后台发送器传给邻近 `yufeng-modelside`。满则丢旁路并计数，已发出的状态码不变。禁止为 Brain 或 Agent 再拷原文；不得在请求路径推理、访问 Brain、同步写文件或等待消费者。Python 始终位于独立 ModelSide 进程，不编进 `yufeng-edge`。

<a id="async-detection-worker"></a>
### 异步检测执行实例
Edge 亲和的独立 `yufeng-modelside` Python 服务：从 Edge 接收[规范化模型流量](#normalized-model-traffic)，按[签名模型档案](#signed-model-profile)加载权重、异步推理与有界采样，再只向 Brain 批量上报无原文的 `MODEL_ALERT` 或 `REVIEW_SAMPLE`。它不进任何平台 Go 二进制，不持 Gate、Agent、工具、消息服务器或数据库权限，也不训练。同机优先使用 Unix 域套接字，跨主机必须使用相互传输层安全协议并限制在同一受控防御网络。

<a id="evidence-ring"></a>
### 证据环
边缘本机短期原文存储的兼容称呼。即时路径仍可使用内存环；流量调查候选写入加密分段 `EvidenceVault`，默认 256 MiB、24 小时、固定候选优先于新低风险证据。Jarvis 永不读取；只有绑定案件的一次性批准可让短命调查 run 经 brain 模型网关消费指定片段。

<a id="distillation"></a>
### 蒸馏环
"贵模型发现、便宜模型拦截"：异步档（含大模型）发现的新攻击模式，仅当同步已有检测键时可转化为精确策略；同步无发现时只能产生 `SUSPECTED_MISS`，走形状规则或升级同步检测器，不得编造可执行策略。

<a id="campaign"></a>
### 攻击活动（campaign）
跨时间、跨资产的攻击行为聚合。自适应指数（失败后切换速度 × 节奏规律性 × 载荷风格）用于归因是否为人工智能驱动的自动化渗透。

<a id="replay-gate"></a>
### 回放门禁
制品发布前的历史语料回归验证：恶意样本必须全拦、良性样本必须零误伤（或低于门槛）。不通过不放行——这是"提案再聪明也要确定性验证"的落点。

## 通用

<a id="implementation-route"></a>
### 实施路线
契约定稿 → 中台与数据面基座 → 流量拦截生产闭环 → 按客户证据引入画像检测 → 资产侧执行链路 → 情报感知与漏洞报告 → 运行时约束和冷补丁 → 平台化交付。完整依赖顺序见 [`architecture.md`](architecture.md) 第 12 节。

<a id="adr"></a>
### ADR（架构决策记录）
Architecture Decision Record：架构决策记录，按编号存档决策与一句话理由，见 [architecture.md](architecture.md) 附录（当前 001–038）。

<a id="git-tree-identity"></a>
### Git 树内容身份（Git tree identity）
Git 提交中由根树对象摘要标识的完整文件内容，不包含提交消息、作者、时间和父提交谱系。两次提交的根树摘要完全相同时，仓库受版本控制的文件逐字节相同；软件发布只把它作为源码来源记录，不能用它代替最终上传文件的摘要。

<a id="release-acceptance-contract"></a>
### 发布验收合同（release acceptance contract）
版本变更拉取请求中冻结的有限阻断条件集合。条件只覆盖已声明的安全边界、数据完整性、网络契约、构建失败与制品不可用；智能代理后来发现但未违反这些条件的问题必须报告并延期，不能自行把它升级为当前版本的发布阻断项。

<a id="software-release-artifact-set"></a>
### 软件发布制品集（software release artifact set）
发布工作流从持续集成成功的精确 `main` 提交一次构建出的全部二进制、控制台、Python 包、部署包和容器镜像归档。清单记录每个文件的名称、字节数与安全哈希算法 256 位摘要；验证、恢复上传和公开 Release 必须复用同一批文件，禁止重建或覆盖同名资产。

<a id="deployment-qualification-evidence"></a>
### 部署验收证据（deployment qualification evidence）
把已公开软件发布清单中的制品摘要绑定到某个试点或客户环境的活栈、恢复、容量、安全与变更责任记录。它证明该批制品在该环境中的运行结果，不改变软件版本的发布状态，也不能作为覆盖 GitHub Release 资产的理由。

<a id="modelgateway"></a>
### 模型网关（modelgateway）
大语言模型聊天补全的**唯一出网口**：生产生成与连通性探测都从 `yufeng-brain` 发起，一条槽持 `base_url` + `model` + [模型方言](#model-dialect) + 服务端保存的密钥。不是多供应商并行代理。贾维斯、run 与浏览器都不持密钥、不直连模型端点；现有人机交付纯文本走 `CompleteChat`，统一 Agent 座架走带 Turn、租约、ContextManifest、工具项和预算的 `Generate`。引导完成前改槽走 `PutModelConfig`；完成后管理员走 `GetModelGateway` / `UpdateModelGateway` / `ProbeModelGateway`（`docs/api.md` 第 19.4 节），不退引导状态。`agents/modelgateway` 只提供客户端协议适配，确定性模型提供者仅存在于 `_test.go` 测试编译单元，不进入交付二进制。流量深度学习走 Edge 邻近的独立 ModelSide，只读取已签名模型档案与本地权重，不使用聊天凭据槽，也不把原始流量送入 Brain。Google Agent Development Kit 只借“模型 + 工具 + 会话 + 运行器”的形状，**禁止**作为平台进程内运行时（ADR-003）。

<a id="model-dialect"></a>
### 模型方言（model dialect）
brain 模型网关出网时使用的供应商 HTTP 协议。线上只许 proto 枚举全名：`MODEL_DIALECT_OPENAI_CHAT`（`POST {base_url}/chat/completions`）、`MODEL_DIALECT_OPENAI_RESPONSES`（`POST {base_url}/responses`）、`MODEL_DIALECT_CLAUDE_MESSAGES`（`POST {base_url}/messages`）。同时一条槽只持一种方言。省略或 `MODEL_DIALECT_UNSPECIFIED` 按 OpenAI Chat 解释。贾维斯不选择方言、不直连供应商。

<a id="onboarding"></a>
### 初次配置引导（onboarding）
单机交付后、主控制台可用前的强制向导。库里一行部署状态；管理员登录若未到 `ONBOARDING_STATE_COMPLETED`，整站只能走 `https://127.0.0.1:9050/app/setup`。界面固定 **六步**。未失败时禁止跳步；`TestModelConnectivity` 三次失败后可跳过配置模型与设置防御资产，进入授权值守账户。权威定义见 `docs/api.md` §19.0。第三步提交部署规格并由 Brain 确定性签发监听计划、基线世代和模型档案；第四步由技术人员人工安装并启动 Edge 与可选 ModelSide；第五步设置防御资产；第六步授权值守账户。Brain 与贾维斯不创建、启动或探测 Edge。非管理员在完成前只看到「等待管理员完成初次配置」。密钥只写不回读。

<a id="onboarding-state"></a>
### 引导状态（OnboardingState）
部署状态机的封闭枚举。**线上唯一写法是 proto 枚举全名**（JSON 同名）。库中恰好一行，禁止并行引导。

| proto 枚举 | 含义 | 由 §19.0 哪一步写入（权威） |
|---|---|---|
| `ONBOARDING_STATE_PENDING` | 刚装完，未配模型 | 初态，不是某一步的产物 |
| `ONBOARDING_STATE_MODEL_CONFIGURED` | 密钥已存，探测尚未成功 | 步骤 1 `PutModelConfig` |
| `ONBOARDING_STATE_MODEL_LIVE` | 探测成功 | 步骤 2 `TestModelConnectivity` |
| `ONBOARDING_STATE_EDGE_LIVE` | 人工部署的 Edge 已主动注册并在心跳中确认装载期望监听计划与资产世代 | 步骤 4 等待 Edge 主动回执（步骤 5 设置防御资产不改此态） |
| `ONBOARDING_STATE_COMPLETED` | 引导结束，主控制台开放 | 步骤 6 `CompleteOnboarding` |
| `ONBOARDING_STATE_FAILED` | 最近一步失败，保留原因码 | 任一步失败；从本态出发的重试边见 `docs/api.md` §19 合法边表，不丢已配密钥 |

`ONBOARDING_STATE_COMPLETED` 的服务端谓词**只认** `docs/api.md` §19.1，本文不另写一套。引导完成前贾维斯令牌不得含 `govern.propose` / `govern.promote_*`。

<a id="model-egress"></a>
### 模型出口
[模型网关](#modelgateway) 的别称，专指 **brain 进程内** 的出网补全。文档若写「模型出口」即本词条，不是贾维斯进程内的 `agents/modelgateway` 客户端适配器。

<a id="manual-edge-lifecycle"></a>
### Edge 人工生命周期（Manual Edge Lifecycle）
`yufeng-edge` 的安装、启动、升级、回退和卸载由技术人员通过操作系统服务管理器或 Docker Compose 显式执行。Brain 只在管理员提交部署规格时确定性签发监听计划、基线世代与模型档案；Edge 启动后主动注册、拉取并用心跳回执已装载版本。Brain、贾维斯和智能代理工具均不持 Docker、进程管理、Edge 安装或管理口探测权限。

<a id="human-delivery"></a>
### 人机交付闭环
操作员用 Docker Compose 或原生包安装中台，登录后提交部署规格；技术人员再按[Edge 人工生命周期](#manual-edge-lifecycle)安装 Edge 与可选 ModelSide，Edge 主动注册并取得签名制品；控制台可授予并由另一操作员人工推进策略。该闭环的软件版本只有在根目录 [`VERSION`](../VERSION)、同名公开 [GitHub Release](https://github.com/ZionOVO/YuFeng/releases/latest) 与精确提交证据一致时才算通过机器验收，客户现场仍须填写真实网络参数和变更责任记录。

<a id="enterprise-pilot"></a>
### 企业试点（Enterprise Pilot）
单站点交付形态：一个客户站点、一个中台、一个拦截单元；客户入口终止业务传输层安全协议（Transport Layer Security，TLS），御锋反向代理已解密的超文本传输协议（Hypertext Transfer Protocol，HTTP），策略由非提案操作员人工批准生效。软件发布与机器验收完成后，只能声明版本可供部署；现场负责人关闭真实网络参数、切换、回退和恢复责任记录后，才能声明客户现场交付完成。

<a id="ebpf"></a>
### eBPF
extended Berkeley Packet Filter，内核可编程技术：在不重启进程的情况下对运行中的系统施加约束（live 生效语义）。

<a id="saga"></a>
### Saga（补偿事务）
把长流程拆成带补偿动作的步骤，任一步失败就逐步回退已完成的步骤。修复程序的执行实例按此模式运转。

<a id="llm"></a>
### LLM
Large Language Model，大语言模型。

<a id="jwt"></a>
### JWT
JSON Web Token：一种带签名的声明信封格式。能力令牌的载体。
