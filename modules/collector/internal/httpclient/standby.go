package httpclient

import (
	"context"
	"time"
)

type standbyKey struct{}

// WithStandbyDelay 让后续的行情请求启用备援：第一次请求超过 delay 还没有结果时，并发再发一个，先成功的为准。
// delay 不大于 0 时原样返回（不备援）。
func WithStandbyDelay(ctx context.Context, delay time.Duration) context.Context {
	if delay <= 0 {
		return ctx
	}
	return context.WithValue(ctx, standbyKey{}, delay)
}

// StandbyDelayFrom 返回上下文里的备援延迟，没有则为 0。
func StandbyDelayFrom(ctx context.Context) time.Duration {
	delay, _ := ctx.Value(standbyKey{}).(time.Duration)
	return delay
}
