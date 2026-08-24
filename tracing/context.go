package tracing

import (
	"context"
	"crypto/rand"

	"go.opentelemetry.io/otel/trace"
)

// FromContext 返回 ctx 中当前处理链路的追踪标识（32位十六进制字符串）。
// 如果 ctx 中不存在有效的追踪上下文（异常调用路径），返回空字符串，调用方不应因此 panic。
func FromContext(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// WithTraceID 返回一个携带指定追踪标识的新 context，用于没有原始 HTTP 请求可继承时
// （定时任务、外部异步回调）生成一条新链路的起点。traceID 必须是 32 位十六进制字符串
// （如 workflow.SideEffect 确定性生成的值），非法值原样返回原 ctx。
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	tid, err := trace.TraceIDFromHex(traceID)
	if err != nil {
		return ctx
	}
	var sid trace.SpanID
	if _, err := rand.Read(sid[:]); err != nil {
		return ctx
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	return trace.ContextWithRemoteSpanContext(ctx, sc)
}
