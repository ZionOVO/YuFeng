// Package runtime 实现统一智能代理运行时：指令循环、短命执行实例孵化与本地监督代理。
//
// cmd/yufeng-jarvis、cmd/yufeng-agentd、cmd/yufeng-run 只做装配。贾维斯不是独立协议或独立包；
// 与普通智能代理的差异只在中台签发的角色模板、工具白名单、权限域、对象绑定与调用预算。
// yufeng-run 只继承已连接的监督代理描述符，能力令牌由 yufeng-agentd 代持。
//
// [智能代理座架]: ../../docs/glossary.md#agent-harness
package runtime
