# sa-go-common

Seller Aim 跨平台运营系统内部共享 Go module，提供基于 OpenTelemetry SDK 的 traceId 全链路传播能力，供 `sa-go-api`/`sa-go-shop`/`sa-go-platform`/`sa-go-product` 四个服务接入。

- `tracing`：追踪标识提取/生成/传播（HTTP、Kafka、Temporal）
- `logging`：从 `context.Context` 派生带追踪标识字段的 `*zap.Logger`

四服务在各自 `go.mod` 显式声明具体 tag 版本（不使用 `latest`）。API 契约见项目仓库 `specs/017-traceid-propagation/contracts/tracing-api.md`。
