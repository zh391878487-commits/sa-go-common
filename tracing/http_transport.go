package tracing

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// RoundTripper 包装一个 http.RoundTripper，返回的新 RoundTripper 会自动把 ctx 中的
// 追踪标识写入出站请求头。所有对内/对外的 http.Client 必须用它包装 Transport，
// 不允许手动读写 X-Trace-Id 之类的 header。
func RoundTripper(base http.RoundTripper) http.RoundTripper {
	ensureInit()
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base)
}
