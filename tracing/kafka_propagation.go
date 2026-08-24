package tracing

import (
	"context"

	"github.com/IBM/sarama"
	"go.opentelemetry.io/otel"
)

// saramaHeaderCarrier 适配 propagation.TextMapCarrier 接口到 []sarama.RecordHeader。
//
// 官方 opentelemetry-go-contrib 仓库当前没有 IBM/sarama 的 instrumentation 子模块
// （原 Shopify/sarama 版本已随迁移废弃，未见 IBM/sarama 替代版本，详见 research.md
// 待核实项 1）。此处只适配"从哪里读/写字符串"这一层，W3C traceparent 的编解码协议
// 仍然完全由官方 otel.GetTextMapPropagator() 处理，不是自建协议。
type saramaHeaderCarrier struct {
	headers *[]sarama.RecordHeader
}

func (c saramaHeaderCarrier) Get(key string) string {
	for _, h := range *c.headers {
		if string(h.Key) == key {
			return string(h.Value)
		}
	}
	return ""
}

func (c saramaHeaderCarrier) Set(key, value string) {
	for i, h := range *c.headers {
		if string(h.Key) == key {
			(*c.headers)[i].Value = []byte(value)
			return
		}
	}
	*c.headers = append(*c.headers, sarama.RecordHeader{Key: []byte(key), Value: []byte(value)})
}

func (c saramaHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(*c.headers))
	for _, h := range *c.headers {
		keys = append(keys, string(h.Key))
	}
	return keys
}

// KafkaHeader 从 ctx 中取出追踪标识，编码成可以放进 sarama.ProducerMessage.Headers
// 的 []sarama.RecordHeader。生产消息前调用，不需要手动拼 header key/value。
func KafkaHeader(ctx context.Context) []sarama.RecordHeader {
	ensureInit()
	headers := make([]sarama.RecordHeader, 0, 2)
	otel.GetTextMapPropagator().Inject(ctx, saramaHeaderCarrier{headers: &headers})
	return headers
}

// FromKafkaHeaders 从消费到的消息 Headers 中提取追踪标识，返回携带该标识的新 ctx，
// 用于消费端后续业务日志/下游调用。死信重放场景：重放消息自带原始 Headers，
// 消费端沿用同一套提取逻辑即可保留原始追踪标识，不需要额外处理。
func FromKafkaHeaders(headers []sarama.RecordHeader) context.Context {
	ensureInit()
	hdrs := headers
	return otel.GetTextMapPropagator().Extract(context.Background(), saramaHeaderCarrier{headers: &hdrs})
}
