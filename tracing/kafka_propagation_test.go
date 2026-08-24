package tracing

import (
	"context"
	"testing"
)

// TestKafkaHeaderRoundTrip 验证 KafkaHeader→FromKafkaHeaders 编解码往返一致：
// 同一个 trace id 经过编码再解码后应得到相同的值。
func TestKafkaHeaderRoundTrip(t *testing.T) {
	ctx, span := Tracer().Start(context.Background(), "test-produce")
	defer span.End()

	want := FromContext(ctx)
	if want == "" {
		t.Fatal("expected non-empty trace id from a started span")
	}

	headers := KafkaHeader(ctx)
	if len(headers) == 0 {
		t.Fatal("expected KafkaHeader to write at least one header")
	}

	gotCtx := FromKafkaHeaders(headers)
	got := FromContext(gotCtx)

	if got != want {
		t.Fatalf("trace id mismatch after round trip: want %s, got %s", want, got)
	}
}

// TestFromKafkaHeadersEmpty 验证空/无关 Header 不会 panic，返回的 ctx 里没有有效追踪标识。
func TestFromKafkaHeadersEmpty(t *testing.T) {
	ctx := FromKafkaHeaders(nil)
	if got := FromContext(ctx); got != "" {
		t.Fatalf("expected empty trace id for empty headers, got %s", got)
	}
}
