package observability

import "log/slog"

// setDefaultLogger 仅在调用方提供有效日志器时替换进程默认日志器。
func setDefaultLogger(l *slog.Logger) {
	if l != nil {
		slog.SetDefault(l)
	}
}
