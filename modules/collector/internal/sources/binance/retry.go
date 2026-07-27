package binance

import (
	"context"
	"errors"
	"time"

	"github.com/avast/retry-go"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const outboundAttempts = 3

func retryStorage(ctx context.Context, operation func() error) error {
	return retry.Do(
		operation,
		retry.Attempts(outboundAttempts),
		retry.Delay(200*time.Millisecond),
		retry.DelayType(retry.BackOffDelay),
		retry.MaxDelay(time.Second),
		retry.LastErrorOnly(true),
		retry.Context(ctx),
		retry.RetryIf(func(err error) bool {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return false
			}
			var responseErr *storageResponseError
			if errors.As(err, &responseErr) {
				return responseErr.code == storagepb.ErrorCode_INNER_ERR
			}
			return true
		}),
	)
}
