package tracing

import (
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
)

// TemporalInterceptor 返回供 Temporal client/worker 注册使用的 interceptor，
// 供 client.Options.Interceptors / worker 对应选项使用，取代手写 ContextPropagator。
func TemporalInterceptor() interceptor.Interceptor {
	ensureInit()
	i, err := temporalotel.NewTracingInterceptor(temporalotel.TracerOptions{
		Tracer: Tracer(),
	})
	if err != nil {
		// 仅在 TracerOptions 配置本身非法时发生，属编程期错误。
		panic("sa-go-common/tracing: failed to build temporal tracing interceptor: " + err.Error())
	}
	return i
}
