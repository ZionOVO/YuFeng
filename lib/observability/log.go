package observability

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// 结构化日志固定字段，输出采用逐行 JavaScript 对象表示法。
const (
	LogTime    = "ts"
	LogLevel   = "level"
	LogMsg     = "msg"
	LogService = "service"
	LogTraceID = "trace_id"
	LogSpanID  = "span_id"
)

// RequiredLogFields 是生产可观测性合同固定的日志字段。
var RequiredLogFields = []string{LogTime, LogLevel, LogMsg, LogService, LogTraceID, LogSpanID}

// NewLogger 构造逐行 JavaScript 对象表示法日志；从上下文带出追踪标识，并脱敏令牌。
func NewLogger(w io.Writer, service string) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}
	if service == "" {
		service = "yufeng"
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.TimeKey:
				a.Key = LogTime
			case slog.LevelKey:
				a.Key = LogLevel
			case slog.MessageKey:
				a.Key = LogMsg
			}
			if a.Value.Kind() == slog.KindString {
				a.Value = slog.StringValue(RedactLogValue(a.Key, a.Value.String()))
			}
			return a
		},
	})
	return slog.New(&traceHandler{next: h, service: service})
}

// RedactLogValue 去掉访问令牌与能力令牌明文。
func RedactLogValue(key, val string) string {
	k := strings.ToLower(key)
	if strings.Contains(k, "authorization") || strings.Contains(k, "capability") || strings.Contains(k, "token") {
		return "[redacted]"
	}
	if strings.Contains(strings.ToLower(val), "bearer ") {
		return "[redacted]"
	}
	return val
}

type traceHandler struct {
	next    slog.Handler
	service string
}

// Enabled 把日志级别判定委托给被包装的处理器。
func (h *traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle 注入服务与追踪标识后，把日志记录交给被包装的处理器。
func (h *traceHandler) Handle(ctx context.Context, rec slog.Record) error {
	rec.AddAttrs(slog.String(LogService, h.service))
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		rec.AddAttrs(slog.String(LogTraceID, sc.TraceID().String()), slog.String(LogSpanID, sc.SpanID().String()))
	} else {
		rec.AddAttrs(slog.String(LogTraceID, ""), slog.String(LogSpanID, ""))
	}
	return h.next.Handle(ctx, rec)
}

// WithAttrs 返回附带指定属性且保留追踪注入行为的新处理器。
func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{next: h.next.WithAttrs(attrs), service: h.service}
}

// WithGroup 返回进入指定属性组且保留追踪注入行为的新处理器。
func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{next: h.next.WithGroup(name), service: h.service}
}
