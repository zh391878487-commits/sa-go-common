package tracing

import (
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.uber.org/zap"
)

// TraceIDKey 是写入 gin.Context 的追踪标识键名，供各服务响应体/响应头回写逻辑读取。
const TraceIDKey = "traceId"

// GinMiddleware 提取上游请求已有的追踪标识，缺失则生成新的；同时写入 gin.Context
// （供响应体/响应头回写使用）和 c.Request.Context()（供业务代码通过 ctx 继续向下传递）。
//
// 不基于 otelgin.Middleware 实现：otelgin 内部自行调用 c.Next() 并在其返回后才结束
// Span，导致调用方在 base(c) 返回之前无法拿到 traceID 提前写响应头/gin.Context——
// 这与本函数"提取/生成后立刻可用"的契约冲突。此处直接使用 otel 官方核心 API
// （propagation.Extract + Tracer.Start，与 otelgin 内部实现调用的是同一套官方原语），
// 只是自行控制调用时序，不是绕开官方方案自造协议。
func GinMiddleware() gin.HandlerFunc {
	ensureInit()
	return func(c *gin.Context) {
		ctx := otel.GetTextMapPropagator().Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		ctx, span := Tracer().Start(ctx, c.Request.Method+" "+c.FullPath())

		traceID := FromContext(ctx)
		c.Set(TraceIDKey, traceID)
		c.Header("X-Trace-Id", traceID)
		c.Request = c.Request.WithContext(ctx)

		c.Next()

		span.End()
	}
}

// GinCustomRecovery 返回 gin.HandlerFunc，替换 gin.Recovery()，
// 保证 panic 恢复后的错误响应仍然包含追踪标识，响应体遵循项目统一响应体结构。
func GinCustomRecovery(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				traceID := FromContext(c.Request.Context())
				logger.Error("panic_recovered",
					zap.Any("error", r),
					zap.String("traceId", traceID),
					zap.String("method", c.Request.Method),
					zap.String("path", c.Request.URL.Path),
					zap.ByteString("stack", debug.Stack()),
				)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"code":    50001,
					"message": "internal server error",
					"data":    gin.H{},
					"traceId": traceID,
				})
			}
		}()
		c.Next()
	}
}
