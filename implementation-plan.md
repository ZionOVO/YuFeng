# 0.1.0 Edge 人工生命周期与异步模型旁路实施计划

> 状态快照：2026-08-24。架构权威为 [`docs/architecture.md`](docs/architecture.md)，网络行为权威为 [`docs/api.md`](docs/api.md)，流量入口语义权威为 [`docs/design.md`](docs/design.md) 第 4 节。本计划只记录实现定位、验收和发布收尾，不另立产品语义。

## 执行规则

1. 架构变化先进入 `docs/architecture.md`；网络行为与状态先进入 `docs/api.md`，再改 `proto/`；数据消息字段先改 `proto/` 再同步文档。
2. 每个条目按消息、字段、数据库、服务、客户端、部署、测试与产品文档全仓追踪；定位不是修改闭集。
3. 每批提交运行 `make build test vet`。契约变更另运行 Buf 格式、静态检查、兼容检查与生成；控制台变更另运行测试、静态检查、类型检查和生产构建。
4. 勾选只证明仓库验收完成，不代表 GitHub Release 已公开或客户现场变更单已关闭。

## Edge 生命周期归还技术人员

- [x] **由 Brain 确定性签发人工部署规格。**
  - 定位：`docs/api.md` 第 19 节；`proto/yufeng/onboarding/v1/onboarding.proto` `PutDeploymentSpecification`；`lib/brain/onboarding_deployment_specification.go` `publishDeploymentSpecification`；`lib/brain/baseline_generation.go`；`lib/store/migrations/00045_edge_modelside_bypass.sql`。
  - 验收：管理员提交稳定单元、资产、入口目标、可信代理网段和模型档案；Brain 在同一事务创建或复用资产、预声明单元与 ModelSide 身份、签发下一监听计划和完整基线世代；相同规范规格幂等；不等待贾维斯、Edge、ModelSide、容器或探针。

- [x] **用 Edge 主动注册与心跳替代反向部署和探测。**
  - 定位：`docs/api.md` 第 2、19 节；`proto/yufeng/registry/v1/registry.proto` `HeartbeatRequest.current_listen_plan_version`；`lib/brain/registry.go`；`lib/brain/onboarding.go` `edgeReady`；`cmd/yufeng-edge/brain.go`。
  - 验收：Edge 主动注册并拉取签名监听计划、资产世代和策略；只有最近心跳中的单元、监听计划版本、世代标识和序号全部匹配时 `edge_ready=true`；Brain 不连接 Edge 管理口或业务路由。

- [x] **删除贾维斯和 Brain 的 Edge 部署能力。**
  - 定位：`agents/runtime/loop.go`；`lib/brain/agent_catalog.go`、`toolgateway.go`、`onboarding_service.go`；`proto/yufeng/worker/v1/worker.proto`；`console/src/pages/setup/SetupPage.tsx`；全仓生产符号与文档检索。
  - 验收：生产工具、指令和运行时不含 Edge 安装、启动、重建、基线发布或探测流程；部署规格不依赖 `jarvis_online`；贾维斯只保留安全研判和治理建议工具；兼容线缆若必须为消息兼容保留，只返回未实现且不进入产品文档或客户端。

- [x] **把可选本机监督器降为人工只读服务。**
  - 定位：`cmd/yufeng-dataplane/main.go`；`lib/dataplane/server.go`；`lib/dataplane/supervisor_test.go`；`deploy/compose.yaml`。
  - 验收：监督器只读取技术人员配置的本机服务状态，不持 Docker、不创建或重建 Edge、不接收 Brain 或 Agent 控制；正式 Compose 不启动监督器、不挂 Docker 套接字。

- [x] **同时交付 Edge 原生二进制和独立容器镜像。**
  - 定位：`.github/workflows/release.yml`；`deploy/edge.Dockerfile`；`deploy/edge/`；`deploy/compose.edge-modelside.yaml`；`deploy/compose_contract_test.go`。
  - 验收：发布包含各支持架构的 `yufeng-edge` Go 二进制和人工安装、升级、卸载物料；Edge 镜像使用独立入口；控制面 Compose 与人工数据面 Compose 分离；同机或远端 Brain 拓扑均有明确参数与相互传输层安全协议边界。
  - 结果：Darwin arm64 本机已分别构建 Linux arm64 与发布工作流实际归档的 Linux amd64 镜像；两者入口冒烟成功，目标镜像标签为 `v0.1.0`，运行用户为 `65532:65532`。

## Edge 邻近 ModelSide

- [x] **定义版本化规范流量与专用批量结果协议。**
  - 定位：`docs/api.md` 第 21.5 节；`proto/yufeng/modelside/v1/modelside.proto`；`proto/yufeng/artifact/v1/v1.proto` `ModelProfile`；`proto/yufeng/event/v1/v1.proto` 模型事件种类。
  - 验收：规范流量包含请求、单元、资产、世代、方法、路由、允许头、查询参数、有界正文、内容类型、原始长度、截断状态和检查覆盖度；结果不含头、查询值或正文；结果车道只有 `MODEL_ALERT` 与 `REVIEW_SAMPLE`。

- [x] **在 Edge 请求路径建立一次所有权转移的非阻塞旁路。**
  - 定位：`lib/edgecore/model_bypass.go` `NormalizedModelTraffic`、`ModelIngressQueue`；`lib/edgecore/release_proxy.go`；`cmd/yufeng-edge/modelside.go` 与后台运行循环。
  - 验收：只处理反向代理、外部授权或其它已解密 HTTP 请求副本；请求路径不推理、不访问 Brain、不写文件、不等待消费者；正文只向本地队列转移一次；队列条目或字节满时丢旁路并计数，不改变当前请求。

- [x] **交付独立 Python `yufeng-modelside` 服务包与镜像。**
  - 定位：`components/modelside/pyproject.toml`、`Dockerfile`、`yufeng_modelside/inference.py`、`runtime.py`、`server.py`、`cli.py`；`deploy/modelside/`。
  - 验收：沿用参考 TensorFlow 字符编码、双向长短期记忆网络形状、权重清单与摘要校验；不复用日志接入、Redis、消费者或旧上报代码；同机 Unix 域套接字、跨主机相互传输层安全协议；无 Gate、数据库、消息服务器或 Agent 权限。
  - 结果：6 项 Python 合同、采样与断连测试通过，`yufeng_modelside-0.1.0-py3-none-any.whl` 构建成功；Linux arm64 和 Linux amd64 TensorFlow 镜像均完成构建与入口冒烟。依赖使用跨平台 `tensorflow` 发行包，并以源码合同测试禁止回退到缺少 Linux arm64 wheel 的 `tensorflow-cpu`。

- [x] **只按签名模型档案分类和采样。**
  - 定位：`lib/kernel/model_profile.go`；`components/modelside/yufeng_modelside/contracts.py`、`runtime.py`；`lib/brain/model_result.go`。
  - 验收：进程参数不能覆盖模型版本、告警阈值、复核下限、窗口、每单元上限、每路由上限或去重规则；初始档案为五分钟窗口、每单元最多四个复核代表、同方法和路由只保留最高风险代表；Brain 按同一签名档案重新校验。

- [x] **把 ModelSide 身份钉到部署、资产和客户端证书。**
  - 定位：`docs/api.md` 第 19.2、21.5.4 节；`lib/brain/onboarding_deployment_specification.go` `ensureModelSideIdentityDeclaration`；`lib/brain/model_result.go` `authenticate`、`bindModelSidePrincipal`；迁移 `00045_edge_modelside_bypass.sql`。
  - 验收：部署规格预声明 `${unit_id}-modelside`；首次合法批次固定相互传输层安全协议客户端证书指纹；跨单元、跨资产、自造身份或换证冒用失败关闭；ModelSide 凭据不能调用 Agent、Worker、工具或单元接口。

## Brain 结果入账与案件调度

- [x] **原子入账高风险模型告警。**
  - 定位：`lib/brain/model_result.go` `ingestResult`；`lib/brain/check_ticket.go`；`lib/brain/case_cluster.go`；`lib/brain/outbox.go`；迁移 `00045_edge_modelside_bypass.sql`；`lib/brain/model_result_test.go`。
  - 验收：同一 PostgreSQL 事务创建不可变事件、追加推理、冻结检查票据、聚类或创建案件、写事务发件箱并创建或唤醒研判；任何一步失败整条回滚；`result_id` 重试不重复副作用。

- [x] **让复核代表先聚合而不逐条唤醒贾维斯。**
  - 定位：`components/modelside/yufeng_modelside/runtime.py` `ReviewSampler`；`lib/brain/model_result.go` `reserveReviewWindow`、`storeReviewRepresentative`；`lib/brain/model_result_test.go`。
  - 验收：低于告警阈值但达到复核下限、新路由或覆盖不足才有资格；窗口和单元/路由上限有界；相同方法与路由只保留最高风险；单条 `REVIEW_SAMPLE` 不创建贾维斯指令。

- [x] **保持两条旁路队列相互独立并允许 Brain 断连。**
  - 定位：`lib/edgecore/model_bypass.go`；`components/modelside/yufeng_modelside/runtime.py` `ResultQueue`、`_upload_loop`；Python 运行时测试。
  - 验收：Edge 队列满只影响旁路；ModelSide 结果队列满优先让告警替换复核代表并分别计数；Brain 断连时推理继续、后台退避重试；磁盘不进入标准队列路径。

## 容量、发布与远端集成

- [x] **验证五种异步旁路场景的每秒 2000 请求预算。**
  - 定位：`docs/api.md` 第 21.5.5 节；`lib/kernel/limits.go` `EdgeThroughputRPS`、`ModelBypassP99Budget`；`lib/edgecore/model_bypass_performance_test.go`；`scripts/performance-live.sh`；`docs/performance-baseline.md`。
  - 验收：旁路关闭、ModelSide 空闲、ModelSide 满载、Brain 断连和 Brain 磁盘变慢分别记录吞吐、第 99 百分位延迟、两队列深度、普通与告警丢弃、重试；所有场景达到每秒 2000 请求，第 99 百分位不超过关闭旁路基线加预算，且无请求协程等待旁路。

- [x] **删除失效活栈脚本中的自动 Edge 部署假设。**
  - 定位：`scripts/onboarding-live.sh`、`resilience-live.sh`、`security-live.sh`、`performance-live.sh` 及其 Go 契约测试；`Makefile` Compose 目标。
  - 验收：脚本只调用 `PutDeploymentSpecification`；技术人员动作由脚本中的显式 Docker Compose 命令模拟并单独记录；不调用旧部署入口，不等待贾维斯部署，不检查数据面控制令牌或 Docker 套接字持有者。

- [x] **把仓库目标版本与发布物料更新到 0.1.0。**
  - 定位：根目录 `VERSION`；`console/package.json` 与锁文件；`.github/workflows/release.yml`；`docs/delivery-evidence.md`；发布契约测试。
  - 验收：版本来源唯一；发布资产包含 Edge 原生服务物料、ModelSide Python 包与两种容器镜像说明；未存在同名公开 Release 和精确证据前只称目标版本，不称已发布。

- [x] **缩短无价值的 Go 测试耗时并保持行为覆盖。**
  - 定位：`go test -json ./...` 与包级耗时报告；慢测试对应源码和语义断言；`.github/workflows/ci.yml`。
  - 验收：逐项证明耗时来源；只删除重复等待、重复大样本或可用假时钟替代的时间，不降低竞态、事务、协议和请求路径覆盖；记录优化前后包级耗时。
  - 结果：同一主机、`-count=1` 无数据库全仓计时由 17.7 秒降到 9.4 秒；`agents/runtime` 由 16.633 秒降到 5.530 秒，`cmd/yufeng-agentd` 由 15.967 秒降到 3.223 秒。启用隔离 PostgreSQL 后，`lib/brain` 全套由约 104.890 秒降到 32.552 秒：同一测试进程只迁移一次唯一 schema，每例只清空实际有行的表，并只构建一次 `yufeng-run`；数据库竞态套件仍以 145.160 秒通过。保留真实二进制孵化、监督进程死亡回收进程树、刷新凭据持久化失败触发补偿、事务回滚和协议幂等等行为断言；删除的是重复迁移、重复构建和与被测状态无关的固定等待。

- [ ] **用一次构建的软件制品集发布 v0.1.0。**
  - 定位：`docs/architecture.md` 架构决策记录 038；`docs/delivery-evidence.md` 第 2 至 5 节；`scripts/build-release-assets.sh`、`scripts/release-artifacts.py`、`scripts/verify-release-assets.sh`；`.github/workflows/ci.yml` 与 `.github/workflows/release.yml`；`v0.1.0` Release 和工作流记录。
  - 验收：精确 `main` 提交的 `continuous-integration / required` 成功；发布工作流只构建一次 11 个归档并在实际字节上生成清单和校验和；完整 13 文件制品集先保存为不可变工作流制品，再创建注解标签和草稿 Release；远端重新下载复核通过后才公开；失败只允许按原工作流运行标识恢复，不得重新构建、覆盖资产或提交代码刷新证据。部署资格证据单独运行，不改变软件 Release 状态。

## 客户现场仍需完成

- [ ] **关闭真实站点变更记录。**
  - 定位：`deploy/pilot-change-record.md`；`docs/delivery-evidence.md`；客户现场网络和证书清单。
  - 验收：填写真实上游、精确代理网段、Edge 与 ModelSide 主机、服务端名称、证书指纹、防火墙核对、切换与回退负责人、密钥轮换和备份恢复责任人；软件仓库门禁不能代填这些事实。
