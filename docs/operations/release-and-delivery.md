# 软件发布与交付证据

本文把软件版本发布和具体环境验收拆成两个互不冒充的证明域。仓库声明版本只读取根目录 [`VERSION`](../../VERSION)；对外已发布版本只读取 [GitHub Releases](https://github.com/ZionOVO/YuFeng/releases/latest) 中非草稿、资产集合完整且发布清单复核通过的 Release。

`v0.1.0` 已于 2026-08-24 [正式公开](https://github.com/ZionOVO/YuFeng/releases/tag/v0.1.0)，下列 13 文件合同保留为该版本的冻结发布记录。公开软件 Release 不代表任何客户现场已经完成网络、证书、切换和回退验收。

## 1. 软件发布的信任对象

软件发布证明的对象是[软件发布制品集](../glossary.md#software-release-artifact-set)，不是 Git 分支图、合并父提交、本机工作树或某次客户现场运行。

发布任务必须满足以下不变量：

1. 只接受远端最新 `main`，且该精确提交的 `continuous-integration / required` 已成功；
2. `VERSION`、`console/package.json`、`console/package-lock.json` 与 `components/modelside/pyproject.toml` 声明同一版本；
3. 一个工作流运行只构建一次全部软件制品；验证、工作流归档、Draft Release 和最终公开 Release 复用逐字节相同的文件；
4. 验证完成前不创建版本标签或 Release；构建完成后先保存不可变工作流制品，再创建注解标签和 Draft Release；
5. 禁止覆盖同名 Release 资产。上传中断只能从原工作流运行下载同一制品集恢复；
6. 远端重新下载 Draft Release 的全部资产，按发布清单和校验文件复核成功后才公开；
7. 已公开标签和资产不可移动、删除或覆盖。任何修复使用递增补丁版本。

Git 提交、Git 树和工作流运行标识只记录来源。最终信任根是发布清单中每个实际上传文件的名称、字节数和安全哈希算法 256 位摘要。

## 2. `v0.1.0` 冻结发布验收合同

[`VERSION`](../../VERSION) 当前声明为 `v0.1.0`。本节记录该已发布版本冻结时仅有的自动阻断条件；准备下一版本时，版本拉取请求必须先用新版本合同替换本节，不能在发布执行期间临时扩张门禁。

- 精确 `main` 持续集成中的 Go 构建与测试、`yufeng_dev` 构建标签、格式化、综合静态分析、漏洞检查、Protocol Buffers 消息契约、控制台测试与构建、零 C 语言互操作交叉编译任一失败；
- 四个版本源不一致，目标版本不是合法语义版本，或同名远端标签、Draft Release、公开 Release 已存在；
- 下列任一软件资产缺失、多出、无法读取、包含不安全归档路径，或摘要与清单不符；
- Linux amd64 原生程序不能作为 Go 可执行文件读取或不能显示帮助；Python wheel 不能读取；控制台缺少入口；容器镜像归档不能加载，或者开放容器倡议标签中的版本与源码提交不符；
- 发布工作流无法证明准备上传的文件与保存的不可变工作流制品逐字节相同。

新发现若不违反上述条件，智能代理必须报告并记录到后续版本，不能自行加入本版本阻断清单。若用户明确把它升级为当前版本阻断项，必须先通过普通拉取请求修改本节，再重新触发发布任务。

## 3. 精确软件资产

`v0.1.0` Release 固定包含以下 13 个文件：

- `yufeng-v0.1.0-linux-amd64.tar.gz`
- `yufeng-v0.1.0-linux-arm64.tar.gz`
- `yufeng-v0.1.0-linux-mips.tar.gz`
- `yufeng-v0.1.0-windows-amd64.tar.gz`
- `yufeng-v0.1.0-darwin-amd64.tar.gz`
- `yufeng-v0.1.0-darwin-arm64.tar.gz`
- `yufeng-v0.1.0-console.tar.gz`
- `yufeng-v0.1.0-modelside-python.tar.gz`
- `yufeng-v0.1.0-deployment.tar.gz`
- `yufeng-v0.1.0-edge-image-linux-amd64.tar.gz`
- `yufeng-v0.1.0-modelside-image-linux-amd64.tar.gz`
- `yufeng-v0.1.0-release-manifest.json`
- `yufeng-v0.1.0-checksums.txt`

发布清单模式固定为 `yufeng.software-release/v1`，记录版本、源码提交、构建工作流运行、生成时间和前 11 个分发归档的名称、字节数与摘要。校验文件覆盖 11 个分发归档和发布清单，因此不会形成自引用摘要。

## 4. 发布操作

日常修改从最新 `main` 创建短命工作分支，通过拉取请求和必需持续集成合入。版本准备同样走普通拉取请求，并在其中更新版本源和本文件第 2 节。

版本拉取请求合入且精确 `main` 持续集成成功后，在 GitHub Actions 手动触发 `.github/workflows/release.yml`：

1. 不填写恢复运行标识时，工作流构建、验证并上传名为 `yufeng-v0.1.0-release-bundle` 的不可变工作流制品；
2. 工作流创建指向该精确 `main` 的注解标签和 Draft Release，上传同一目录中的 13 个文件；
3. 工作流从 Draft Release 下载全部资产到新目录，重新验证文件集合、归档安全、清单与校验文件；
4. 复核成功后将 Release 公开并标记为 latest；
5. 如果发布在工作流制品保存后中断，使用原运行标识重新触发恢复路径。恢复路径只下载原制品并继续发布，不执行构建。

构建或验证失败时没有发布候选需要“修补后继续”：标签尚未创建，修复通过普通拉取请求进入 `main`，随后启动一个新的工作流运行。标签或 Draft Release 已创建后的恢复只能使用原工作流制品；若原制品不可用，停止并由用户决定递增补丁版本，不得重建同名候选。

## 5. 部署验收证据

部署验收回答“已经公开的精确软件制品在这个环境中是否满足上线条件”，不回答“软件版本是否已经发布”。执行前应从 Release 下载资产并核对发布清单，然后分别记录：

- `scripts/onboarding-live.sh live` 的单站点引导与人工 Edge 生命周期；
- `scripts/security-live.sh live` 的身份、秘密和数据泄漏负向检查；
- `scripts/traffic-review-live.sh live` 的真实流量审查闭环；
- `scripts/resilience-live.sh live` 的断连与回退；
- `scripts/performance-live.sh live` 的五场景容量和第 99 百分位延迟；
- `deploy/envoy/run-integration.sh` 的真实网关路径；
- `scripts/backup-restore-live.sh live` 的独立恢复与源数据库保持不变；
- [`deploy/pilot-change-record.md`](../../deploy/pilot-change-record.md) 中真实上游、代理网段、证书、网络核对与变更责任人。

这些结果绑定发布清单摘要、现场标识、环境和时间。不同环境互不继承；失败只表示该部署尚未验收，不撤销或改写已经公开的软件 Release。
