// Package modelside 提供 Edge 邻近异步流量推理服务的交付制品。
//
// 约束：
//   - 不进 yufeng-edge / yufeng-host / yufeng-brain / yufeng-jarvis 二进制；
//   - 不引入 Redis，不订阅 NATS，不读写 PostgreSQL；
//   - 只从 Edge 取得有界规范流量，只向 Brain 上报无原文结果；
//   - 只按已验签世代转交的模型档案执行，永不参与 Gate 或当前请求裁决。
//
// [Edge 邻近模型旁路]: ../../docs/glossary.md#edge-near-model-bypass
package modelside
