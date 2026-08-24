// Package observability 提供御锋管理面探针、OpenTelemetry 追踪、Prometheus 指标与 slog 逐行结构化日志。
//
// 业务基地址不得挂载 /livez、/readyz、/metrics 或 /version；这些路径只挂独立管理端口。
package observability
