# 御锋文档中心

本文档中心按使用目的组织内容。根目录 [`README.md`](../README.md) 是产品入口；组件目录内的 README 只负责对应组件的安装、配置或运行步骤。

## 产品用户

1. [开始使用](guides/getting-started.md)：验证源码、构建控制台并运行本地纵切片演示。
2. [部署与上线](operations/deployment.md)：选择入口形态、部署拓扑并核对上线条件。
3. [术语表](glossary.md)：查阅产品术语及代码注释使用的稳定锚点。

## 交付运维

1. [部署与上线](operations/deployment.md)：进程生命周期、网络边界、故障回退、安全检查和现场验收。
2. [软件发布与交付证据](operations/release-and-delivery.md)：软件 Release、发布制品和客户部署证据的责任边界。
3. [`deploy/` 运行手册](../deploy/README.md)：控制面、Edge、ModelSide、入口配置和现场变更记录的可执行步骤。

## 开发者

1. [架构设计](architecture.md)：系统边界、技术选型和架构决策的唯一权威。
2. [应用程序编程接口契约](api.md)：网络行为、状态语义和跨接口约束的唯一权威。
3. [代码地图](development/code-map.md)：概念到实现的定位及当前完成状态。
4. [模型打分与智能代理规则提炼数据集](development/testing/model-scoring-and-agent-rule-datasets.md)：公开测试材料的选择、展开和验收约束。
5. [开发规范](../AGENTS.md)：开发流程、代码风格、测试、分支和语义化定位规则。

## 权威归属

| 问题 | 唯一来源 |
|---|---|
| 系统边界、进程职责、技术选型、负面清单 | [`architecture.md`](architecture.md) |
| 网络行为、状态转换、失败语义 | [`api.md`](api.md) |
| 线上字段、枚举和编码 | [`proto/`](../proto/) |
| 固定术语与稳定锚点 | [`glossary.md`](glossary.md) |
| 已实现、受限和未实现能力 | [`development/code-map.md`](development/code-map.md) |
| 部署拓扑和上线前提 | [`operations/deployment.md`](operations/deployment.md) |
| 软件发布与现场证据边界 | [`operations/release-and-delivery.md`](operations/release-and-delivery.md) |
| 动态任务、排期与讨论 | [GitHub Issues](https://github.com/ZionOVO/YuFeng/issues) |

架构决定写入架构正文或架构决策记录；网络行为先写接口契约；字段变化先写 Protocol Buffers 消息。已经被吸收的讨论稿、审查纪要和阶段计划不在主分支保留副本，历史通过 Git 追溯。
