package common

import (
	"context"
	"time"

	"github.com/avast/retry-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// RetryOperation 使用 retry-go 库执行带重试的操作
func RetryOperation(ctx context.Context, operation func() error, operationName string) error {
	const (
		MaxRetryCount = 3
		RetryDelay    = 500 * time.Millisecond
	)

	return retry.Do(
		operation,
		retry.Attempts(MaxRetryCount),
		retry.Delay(RetryDelay),
		retry.DelayType(retry.BackOffDelay), // 指数退避
		retry.LastErrorOnly(true),
		retry.OnRetry(func(n uint, err error) {
			log.WarnContextf(ctx, "[Common] [Retry] %s failed (attempt %d/%d): %v", operationName, n+1, MaxRetryCount, err)
		}),
		retry.Context(ctx),
	)
}