# 开始使用御锋

本指南用于验证源码和体验最短数据面链路，不构成企业试点上线证明。生产部署必须使用公开 Release 中经过清单复核的制品，并按[部署与上线](../operations/deployment.md)完成环境验收。

## 1. 基础验证

平台构建要求 Go 1.27.0。在仓库根目录运行：

```sh
make build test vet
```

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

该命令只启动控制面 Docker Compose 基座。管理员仍须通过控制台六步引导提交部署规格，技术人员再按 [`deploy/edge/`](../../deploy/edge/README.md) 和 [`deploy/modelside/`](../../deploy/modelside/README.md) 的运行手册人工安装数据面。Brain 与贾维斯不会创建、启动或探测 Edge。

## 4. 下一步

- 了解产品边界和进程职责：阅读[架构设计](../architecture.md)；
- 查询远程调用行为和状态语义：阅读[应用程序编程接口契约](../api.md)；
- 准备企业试点：阅读[部署与上线](../operations/deployment.md)并填写[现场变更记录](../../deploy/pilot-change-record.md)；
- 参与开发：阅读根目录[开发规范](../../AGENTS.md)和[代码地图](../development/code-map.md)。
