# 修复连续谱 L1 单站点生产试点交付证据

> [修复连续谱 L1](glossary.md#repair-continuum)指虚拟补丁与流量拦截。本文件定义版本与机器证据口径，并保留历史完整活栈基线；它不代替客户现场变更单。仓库声明的目标版本只读取根目录 [`VERSION`](../VERSION)，对外已发布版本只读取 [GitHub Releases](https://github.com/ZionOVO/YuFeng/releases/latest)。

## 1. 版本证据索引

`VERSION` 是发布流程的唯一版本来源，但它本身不是“已发布”或“验收通过”的证明。只有同名注解标签指向最新 `main`、GitHub Release 已由草稿转为公开、平台制品与证据提升资产全部通过远端复核，才可宣称该版本的软件发布和机器验收闭环。

| 版本 | 证据属性 | 可以宣称的结论 |
|---|---|---|
| `v0.0.2` | 2026-08-21 历史完整活栈基线；精确数据见本文第 5 至第 8 节 | 只证明当时提交、环境与范围，不证明后续版本 |
| `v0.0.3` | 中间文档与历史证据归档版本 | 没有归档新的精确提交完整活栈基线 |
| `v0.0.4` | 中间发布机制版本，补充发布来源、主干内容和已有持续集成证据门禁 | 没有归档新的精确提交完整活栈基线 |
| `v0.0.5` | 中间发布机制版本，补充注解标签的远端标签对象校验 | 没有归档新的精确提交完整活栈基线 |
| `v0.1.0` | 前一目标版本；没有可继承给后续提交的精确活栈证据 | 不证明 `v0.2.0` 的数据面重构或模型旁路 |
| `v0.2.0` | 根目录 `VERSION` 当前声明的人工 Edge 生命周期与 Edge 邻近模型旁路目标 | 精确结果只由同名 GitHub Release 中的三项证据提升资产承载；Release 仍是草稿、资产缺失或复核失败时不得宣称 `v0.2.0` 已通过或已发布 |

版本递增不继承活栈证据。同一版本内，昂贵验收绑定[Git 树内容身份](glossary.md#git-tree-identity)，提交谱系由[证据提升](glossary.md#evidence-promotion)单独绑定：最终 `develop` 的 Git 树、两个父提交、[发布环境指纹](glossary.md#release-environment-fingerprint)、预检有效期或远端持续集成任一不匹配都会使旧归档失效；只有提交标识变化而 Git 树和所有约束仍完全相同时，不重跑昂贵验收。

## 2. `v0.2.0` 精确证据资产

正式 Release 必须同时包含下列三项本机证据资产：

- `yufeng-v0.2.0-live-evidence.tar.gz`：保留静态预检报告、清单、日志和五次热路径基准原始输出，并加入最终 `develop` 的一次活栈、恢复、性能、环境复核和提交谱系；
- `yufeng-v0.2.0-live-evidence.tar.gz.sha256`：只包含上一项归档的精确 SHA-256 校验记录；
- `yufeng-v0.2.0-live-evidence.json`：外部清单，模式固定为 `yufeng.release-evidence/v2`，把静态预检来源和最终归档绑定到版本、最终 `develop` 合并提交、Git 树、两个父提交、精确提交持续集成和内部报告。

外部清单的公开字段如下；不允许添加另一套“当前版本”或人工抄写的替代摘要。

| 字段 | 约束 |
|---|---|
| `schema` | 精确等于 `yufeng.release-evidence/v2` |
| `release-version` | 等于 `VERSION` 和注解标签 |
| `evidence-commit` | 等于已通过远端持续集成的最终 `develop` 合并提交，也是发布合并提交的第二父提交 |
| `evidence-tree` | 同时等于预检树和 `git rev-parse <evidence-commit>^{tree}` |
| `evidence-sha256` | 等于加入一次活栈结果后的最终公开归档 SHA-256；预检归档摘要由 `preflight.archive-sha256` 单独保存 |
| `evidence-result` | 精确等于 `passed` |
| `archive-asset` / `checksum-asset` | 等于同一 Release 中的实际资产名 |
| `report-path` / `report-sha256` | 绑定归档内 `yufeng-evidence/report.json` 及其摘要 |
| `ci-url` / `generated-at` | 记录精确提交远端集成确认链接和协调世界时生成时间 |
| `preflight` | 绑定冻结的 `develop` 基线、发布分支提交、预检树、预检清单摘要、环境指纹、生成时间和过期时间 |
| `merge-parents` | 精确等于最终 `develop` 合并提交的第一父提交和第二父提交，并与 `preflight` 的基线和发布分支提交一致 |

发布拉取请求正文和注解标签消息必须各自恰好携带一次以下机器可读字段：

```text
release-version=v0.2.0
evidence-commit=<exact-develop-commit>
evidence-tree=<exact-git-tree>
evidence-sha256=<archive-sha256>
evidence-result=passed
```

`scripts/release-metadata.py` 拒绝缺失、重复、格式错误或不匹配的字段。发布拉取请求门禁还要求 `evidence-commit` 等于拉取请求头提交，并现场计算最终 `develop` Git 树与预检树是否完全相同；标签和 Release 正文必须保持同一组值。

## 3. 合并前发布预检合同

从冻结的 `origin/develop` 创建 `release/v0.2.0`。准备推送最终发布分支提交前，在该分支干净工作树运行：

```sh
export YUFENG_EDGE_UNIT=<unit-id>
export YUFENG_MODELSIDE_ID=<modelside-id>
export YUFENG_MODELSIDE_WEIGHTS_DIR=<absolute-weights-directory>
make preflight-release-evidence
```

这三个环境设置必须在预检和合并后活栈归档中保持不变。`scripts/preflight-release-evidence.sh` 以冻结 `develop` 和发布分支提交计算无冲突的预期合并 Git 树，并在两个不同路径的临时工作树中先证明环境指纹不依赖检出目录，再验收该树。它本机完整运行竞态测试、`yufeng_dev` 构建标签测试、格式化、golangci-lint 综合静态分析、漏洞扫描、Protocol Buffers 消息契约、控制台、交叉编译、Apple M4 Pro 热路径基准和秘密扫描。完整竞态已经覆盖普通 Go 测试；`production-end-to-end.sh`、`fault-injection-end-to-end.sh` 与五个 `*-live.sh static` 仍可用于修改期间的定向复跑，但不作为第二套执行器重复运行。预检不启动 Docker 活栈，也不读取持续集成 URL。

发布分支修复期间必须先跑受影响模块与失败项；提交前至少运行 `make build test vet` 和相关发布合同测试。准备推送与合并时才完整运行本节静态预检，使远端拉取请求负责确认本机已经证明可合入的提交，而不是替本机发现明显失败。

预检输出由本机保存的归档、校验文件和 `yufeng.release-preflight/v1` 清单组成，不上传到最终 Release。预检归档至少记录：版本、冻结基线提交、发布分支提交、预期合并 Git 树、干净临时工作树、开始和完成时间、默认 72 小时有效期、Apple 硬件、操作系统、Go / Node.js / npm / Buf / Docker / Docker Compose 版本、Compose 配置摘要、模型权重摘要、环境指纹、每条静态命令及退出码、日志摘要，以及九组热路径当前实现与原型各五次、每次 250 毫秒测量窗口的 `benchmem` 原始输出。

打包前后都由 `scripts/release-evidence.py scan` 扫描环境中已配置的秘密值，以及模型密钥、口令、访问或刷新令牌、私钥材料和 Bearer 认证头形状。任何命中都会使归档失败。`scripts/release-evidence.py verify-preflight` 还会拒绝不安全路径、链接、重复成员、环境指纹不一致、证据过期、日志摘要不一致或任何非零静态命令结果。

## 4. 一次合入、证据提升与双阶段发布

1. 本机静态预检成功后，推送发布分支并让精确提交通过 `pull-request.yml` 快速门禁；审批 `release/v0.2.0 → develop` 拉取请求并使用合并提交，本次只合入一次，不删除证据仍引用的发布分支提交。
2. 最终 `develop` 推送持续集成只确认提交确为带两个父提交的合并、Git 树等于发布分支父提交、该父提交存在成功的同仓库拉取请求门禁，并留下精确提交 URL；不再运行 Go、Protocol Buffers、控制台、交叉编译或其它测试套件。
3. 远端持续集成成功后，在最终 `origin/develop` 干净工作树执行 `scripts/archive-live-evidence.sh`：它重新计算环境指纹，验证提交恰有两个父提交且分别等于预检基线与发布分支提交、最终 Git 树等于预检树、预检未过期、远端运行属于最终提交；在任何活栈命令之前对现有试点 PostgreSQL 做独立逻辑备份，然后复用当前 Docker 数据卷运行一次 Docker 活栈、数据库恢复和五场景性能。它不设置 `YUFENG_LIVE_RESET`，不删除数据卷，源备份不进入可上传归档；内部报告只记录备份摘要、大小与时间戳。
4. 归档把静态预检日志与这一次活栈结果装配为 `yufeng.release-evidence/v2` 清单、校验文件、`release-pr-body.md` 和 `release-tag-message.txt`。它不得重跑普通或竞态测试、构建标签、格式化、静态分析、漏洞、消息契约、控制台、交叉编译或热路径基准。性能必须在旁路关闭、ModelSide 空闲、ModelSide 满载、Brain 断连和 Brain 磁盘变慢五种场景下复验每秒 2000 个请求；每个场景的第 99 百分位延迟增量不得超过 1 毫秒，并记录 Edge 输入队列、ModelSide 结果队列、丢弃和重试。
5. 以生成的 `release-pr-body.md` 创建 `develop → main` 发布拉取请求。发布门禁现场比较最终 `develop` Git 树与预检树，并验证来源、主干差异、精确持续集成、提交和证据摘要；审批后必须使用合并提交。
6. 创建同名注解标签，标签消息使用生成的 `release-tag-message.txt`。标签工作流验证标签指向最新 `main`，且发布合并提交的第二父提交就是证据提交，然后构建平台制品并创建 Draft Release。
7. 本机只把三项提升后的证据资产上传到该 Draft Release；源数据库备份和本机预检清单不得上传。
8. 手动触发 `.github/workflows/release.yml` 的发布阶段。远端重新下载全部 15 项资产，其中包括原生 Edge、原生 Python ModelSide、人工部署包以及 Edge 与 ModelSide 的 Linux amd64 容器镜像归档；校验平台制品校验和、Release 正文、注解标签、`develop` 父提交、证据清单、归档、内部报告和秘密扫描记录；只有全部通过才把 Release 转为公开并标为 latest。

发布分支修复期间可按影响范围只重跑失败项；准备合并前必须让精确头提交重新通过快速门禁并完整生成一次预检归档。此后若 `develop` 移动、预期与最终 Git 树不同、两个父提交不同、工具链或环境指纹变化、证据过期、远端持续集成失败，旧预检和发布元数据全部作废，必须回到发布稳定分支重新预检。其它发布门禁失败时保持 Draft 或不创建标签，不得用再次合入 `develop` 代替修复证据链。

## 5. `v0.0.2` 历史范围与规格

以下第 5 至第 8 节只归档 `v0.0.2`，不代表 `VERSION` 当前值。候选证据提交为 `3489673`；发布拉取请求 #6 合入 `main` 后的合并提交为 `144c1c2`，注解标签 `v0.0.2` 解析后指向同一提交。

| 字段 | 历史实测值 |
|---|---|
| 单元 | `local-1` |
| 入口姿态 | `INGRESS_POSTURE_REVERSE_PROXY` |
| 流量键 | `enterprise-site` |
| 业务监听 | `:18080` |
| 上游 | `http://testapp-b:8080`，只用于验收，现场不得复用 |
| 可信代理网段 | `0.0.0.0/0`，只用于本机验收，现场必须收紧 |
| 部署规格摘要 | `sha256:db366e313b8f82f8ec3fc5e6688130f1c923d203554d122df0a1351d8ef27d0b` |
| 边缘镜像标识 | `sha256:88bb3f41130209ccee3d568581b1f6521c3b1ccda9e9194ae7eb5d25b0845f31` |

实际规格、规格摘要、镜像标识、客户入口上一版配置和负责人始终必须写入现场变更单。

## 6. `v0.0.2` 历史机器门禁

| 证据 | 历史结果 |
|---|---|
| Go 构建、测试、竞态、格式化、静态分析和漏洞扫描 | `3489673` 的干净候选工作树通过 |
| Protocol Buffers 消息契约格式、兼容性和生成代码无差异 | 通过 |
| 控制台测试、规则、类型与 Connect 交付构建 | 通过 |
| 发布提交远端持续集成 | [`144c1c2` 的运行 32447599070](https://github.com/ZionOVO/YuFeng/actions/runs/32447599070)全部通过 |
| 反向代理真实上游和人机引导 | 通过 |
| 身份、秘密和数据泄漏负向演练 | 通过 |
| 断网自治、无效世代、补传与回退 | 通过 |
| 固定环境容量和过载 | 通过 |
| Envoy 外部授权允许、拒绝、部分正文、超时、熔断与断连 | 通过 |
| 全新数据库恢复 | 通过 |

历史候选工作树以分离头指向 `3489673`，`git status --porcelain` 无输出；验收复用了当时现有持久数据库，没有清理数据卷、重置令牌或绕过证书指纹校验。

## 7. `v0.0.2` 历史容量与恢复

实测环境为 Docker Desktop 27.5.1、Apple arm64、12 个逻辑处理器、8.217 GB Docker 内存、Go 1.27.0、Coraza 开放全球应用安全项目核心规则集 4.25.0、偏执级别 1。历史试点上限为每秒 2000 个请求、2048 个在途请求；目标负载实际每秒 2000.00 个请求，边缘第 99 百分位延迟 2.770 毫秒，目标容器内存 136,629,452 字节。更高饱和吞吐不是交付容量，硬件、规则集、上游或请求分布变化后必须重测。

2026-08-21 的逻辑备份恢复到独立全新 PostgreSQL 16 实例耗时 35 秒；当时比较 60 张公开表、44,107 行和序列状态，已提交行丢失为零。该历史演练早于当前独立 `traffic` schema 恢复合同，不能替代 `v0.2.0` 对治理与流量 schema、序列、恢复时限和源数据库保持不变的重新验证。

## 8. 回退与现场未闭环项

反向代理回退只恢复客户入口上一版回源池，不依赖中台。Envoy 回退只恢复上一版监听器配置并移除外部授权过滤器。完整命令见 [部署场景](deployment-scenarios.md#53-失败与恢复)、[`deploy/reverse-proxy/`](../deploy/reverse-proxy/) 和 [`deploy/envoy/`](../deploy/envoy/)。

软件 Release 无论是否公开，都不能代替现场负责人填写并关闭变更记录。精确代理网段、真实上游、证书和网络核对结果，以及入口切换、回退、密钥轮换和备份恢复责任人必须有真实记录；在此之前只能声明相应软件版本的机器验收状态，不能宣称客户上线完成。
