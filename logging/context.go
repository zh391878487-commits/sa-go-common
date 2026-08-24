package logging

import (
	"context"

	"github.com/zh391878487-commits/sa-go-common/tracing"
	"go.uber.org/zap"
)

// FromContext 返回一个已经带上 ctx 中追踪标识字段的 *zap.Logger，
// 业务代码原有的 h.logger.Xxx(...) 调用逐步替换为 logging.FromContext(ctx).Xxx(...)。
func FromContext(ctx context.Context) *zap.Logger {
	traceID := tracing.FromContext(ctx)
	if traceID == "" {
		return zap.L()
	}
	return zap.L().With(zap.String("traceId", traceID))
}
