package tracing

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

var (
	initOnce sync.Once
	tracer   oteltrace.Tracer
)

// ensureInit 惰性初始化全局 TracerProvider。
//
// 必须使用 sdktrace.NewTracerProvider（真实 SDK Provider），禁止使用
// go.opentelemetry.io/otel/trace/noop 包：noop TracerProvider 生成的
// SpanContext 全零无效，会导致下游 instrumentation（otelhttp 等）判定
// context 无效后重新生成新的 trace id，破坏全链路唯一标识这一核心目标
// （见 research.md 待核实项 3、opentelemetry-go#4027）。不挂 exporter/processor
// 不影响 trace id 的生成——ID 生成由 IDGenerator 负责，与是否导出到后端无关。
func ensureInit() {
	initOnce.Do(func() {
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
		)
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(propagation.TraceContext{})
		tracer = tp.Tracer("github.com/zh391878487-commits/sa-go-common")
	})
}

// Tracer 返回共享的全局 Tracer，供包内其余文件生成 Span。
func Tracer() oteltrace.Tracer {
	ensureInit()
	return tracer
}
