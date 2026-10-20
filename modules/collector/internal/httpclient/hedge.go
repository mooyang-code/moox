package httpclient

import (
	"context"
	"time"
)

type hedgeKey struct{}

// WithHedgeDelay 让后续的行情请求启用对冲：第一次请求超过 delay 还没有结果时，并发再发一个，先成功的为准。
// delay 不大于 0 时原样返回（不对冲）。
func WithHedgeDelay(ctx context.Context, delay time.Duration) context.Context {
	if delay <= 0 {
		return ctx
	}
	return context.WithValue(ctx, hedgeKey{}, delay)
}

// HedgeDelayFrom 返回上下文里的对冲延迟，没有则为 0。
func HedgeDelayFrom(ctx context.Context) time.Duration {
	delay, _ := ctx.Value(hedgeKey{}).(time.Duration)
	return delay
}
