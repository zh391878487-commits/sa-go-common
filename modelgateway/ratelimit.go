package modelgateway

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// rateLimitConfig is set via WithRateLimit at NewClient construction time. A nil config (the
// default when no option is passed) means "no limiting" — see research.md D5: existing
// translate call sites migrating onto this shared client must not be silently rate-limited
// unless they opt in, since they previously had no limiting at all (only a 60s HTTP timeout +
// Temporal retry). "调用方显式传入的维度 key" is expressed here as: each Client instance is
// configured once, at construction, with the dimension its call site owns (e.g. "translate" vs
// "embedding"); a single shared Client is never silently rate-limited for a caller that opted out.
type rateLimitConfig struct {
	dimension  string
	capacity   float64
	refillRate float64
}

// tokenBucketScript mirrors the Lua token bucket algorithm in
// sa-go-platform/internal/pkg/ratelimit/limiter.go (HMGET/HMSET atomicity, math.floor integer
// precision). Duplicated rather than imported: sa-go-platform's ratelimit package lives under
// `internal/`, which Go's module system physically forbids importing from another module — see
// research.md D5 for why this is not a "reinventing the wheel" violation.
var tokenBucketScript = redis.NewScript(`
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])
local now_ms = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])

local data = redis.call('HMGET', key, 'tokens', 'last_refill')
local tokens = tonumber(data[1])
local last_refill = tonumber(data[2])

if tokens == nil then
  tokens = capacity
  last_refill = now_ms
end

local elapsed = now_ms - last_refill
if elapsed > 0 then
  local refill = math.floor(elapsed * refill_rate / 1000)
  tokens = math.min(capacity, tokens + refill)
  last_refill = now_ms
end

if tokens >= requested then
  tokens = tokens - requested
  redis.call('HMSET', key, 'tokens', tokens, 'last_refill', last_refill)
  redis.call('EXPIRE', key, math.floor(capacity / refill_rate) * 2)
  return {1, tokens}
else
  redis.call('HMSET', key, 'tokens', tokens, 'last_refill', last_refill)
  redis.call('EXPIRE', key, math.floor(capacity / refill_rate) * 2)
  return {0, tokens}
end
`)

// ErrRateLimited is returned when the configured dimension's token bucket is exhausted.
type ErrRateLimited struct {
	Dimension    string
	RetryAfterMs int64
}

func (e *ErrRateLimited) Error() string {
	return fmt.Sprintf("model gateway rate limit exceeded for dimension %q, retry after %dms", e.Dimension, e.RetryAfterMs)
}

// acquire consumes one token from the configured dimension's bucket. No-op (always succeeds)
// when cfg is nil or rdb is nil — a Client built without WithRateLimit/without a Redis client is
// intentionally unlimited.
func acquire(ctx context.Context, rdb *redis.Client, cfg *rateLimitConfig) error {
	if cfg == nil || cfg.dimension == "" || rdb == nil {
		return nil
	}
	key := fmt.Sprintf("sa-go-common:modelgateway:rate_limit:%s", cfg.dimension)
	nowMs := float64(time.Now().UnixMilli())

	result, err := tokenBucketScript.Run(ctx, rdb, []string{key}, cfg.capacity, cfg.refillRate, nowMs, 1).Int64Slice()
	if err != nil {
		return fmt.Errorf("model gateway rate limit check failed: %w", err)
	}
	if result[0] == 0 {
		waitMs := int64(1000 / cfg.refillRate)
		return &ErrRateLimited{Dimension: cfg.dimension, RetryAfterMs: waitMs}
	}
	return nil
}
