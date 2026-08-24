package observability

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "yufeng"

// Setup 安装进程级 OpenTelemetry 追踪与 slog 默认日志。
func Setup(service string) (func(context.Context) error, error) {
	// Default() 与 semconv 次版本的 SchemaURL 可能不同，带 schema 合并会拒启。
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(semconv.ServiceName(service)))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	slogDefault := NewLogger(nil, service)
	// 设置默认日志器，让标准库与业务共用同一套逐行结构化字段。
	setDefaultLogger(slogDefault)
	return tp.Shutdown, nil
}

// Tracer 返回御锋默认追踪器。
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// Start 开启命名跨度。
func Start(ctx context.Context, name string) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name)
}

// InstallTestTracer 把跨度录到内存，供测试断言真实路径。
func InstallTestTracer() (*tracetest.SpanRecorder, func()) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	return rec, func() { _ = tp.Shutdown(context.Background()) }
}

// TraceInterceptor 为每个 Connect 远程过程调用开启跨度，名称取过程路径。
func TraceInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			name := req.Spec().Procedure
			if name == "" {
				name = "connect.unary"
			}
			ctx, span := Start(ctx, name)
			defer span.End()
			resp, err := next(ctx, req)
			if err != nil {
				span.RecordError(err)
			}
			return resp, err
		}
	}
}

// EndedSpanNames 提取已结束跨度名，测试用。
func EndedSpanNames(rec *tracetest.SpanRecorder) []string {
	if rec == nil {
		return nil
	}
	var names []string
	for _, s := range rec.Ended() {
		names = append(names, s.Name())
	}
	return names
}

// HasSpan 判断是否录到指定过程名（后缀匹配 Connect 过程路径）。
func HasSpan(rec *tracetest.SpanRecorder, suffix string) bool {
	for _, n := range EndedSpanNames(rec) {
		if n == suffix || strings.HasSuffix(n, suffix) {
			return true
		}
	}
	return false
}
