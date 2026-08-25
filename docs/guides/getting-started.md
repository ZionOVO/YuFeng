# 开始使用御锋

本指南用于验证源码和体验最短数据面链路，不构成企业试点上线证明。生产部署必须使用公开 Release 中经过清单复核的制品，并按[部署与上线](../operations/deployment.md)完成环境验收。

## 1. 基础验证

平台构建要求 Go 1.27.0。在仓库根目录运行：

```sh
make build test vet
```

Linux 与 macOS 的低性能开发机可以限制 Go 测试并发，减少中央处理器和内存峰值：

```sh
make build test vet GO_TEST_FLAGS='-p=1'
```

Windows 不要求额外安装 Make；在 PowerShell 中运行同一组平台检查：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\development-check.ps1
```

脚本默认串行执行测试；性能较好的 Windows 设备可以通过 `-Parallelism` 提高并发。平台检查只扫描仓库自身的 Go 包，不会把控制台 `node_modules` 中偶然携带的 Go 源码纳入门禁。Python 3 未安装时，本地跳过 ModelSide 与发布制品的 Python 契约；持续集成仍在固定 Python 版本下强制执行这些契约。

控制台使用 Node.js 22：

```sh
cd console
npm ci
npm test
npm run lint
npm run typecheck
npm run build
```

这些命令验证源码、测试和静态检查，不证明某个 GitHub Release 或客户环境已经验收。

## 2. 本地纵切片演示

演示验证“普通请求放行、演示攻击被本地规则制品拒绝”的最短路径。先启动数据面：

```sh
make demo-init
make run
```

再从另一终端发送请求：

```sh
curl "http://localhost:18080/api/items?page=2"
curl "http://localhost:18080/api/items?id=1+UNION+SELECT+pw"
```

预期状态码依次为 `200` 和 `403`。该演示使用开发规则，不代表生产检测、治理晋升或客户交付已经完成。

## 3. 启动控制面基座

```sh
make compose-up
```

浏览器打开 `https://127.0.0.1:9050/app/setup`。初次配置只有四步：配置 Brain 模型网关、执行真实连通性探测、等待贾维斯主动注册在线、显式进入主控制台。即使此时没有任何资产，也可以完成引导。

进入主控制台后，在 `/app/assets` 登记真实资产，再打开资产详情执行“接入 Edge”。该操作只签发监听计划、资产世代和模型档案，不安装进程。技术人员随后按 [`deploy/edge/`](../../deploy/edge/README.md) 与 [`deploy/modelside/`](../../deploy/modelside/README.md) 人工安装数据面；Brain 与贾维斯不会创建、启动或探测 Edge。

## 4. 下一步

- 了解产品边界和进程职责：阅读[架构设计](../architecture.md)；
- 查询远程调用行为和状态语义：阅读[应用程序编程接口契约](../api.md)；
- 准备企业试点：阅读[部署与上线](../operations/deployment.md)并填写[现场变更记录](../../deploy/pilot-change-record.md)；
- 参与开发：阅读根目录[开发规范](../../AGENTS.md)和[代码地图](../development/code-map.md)。
