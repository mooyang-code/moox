package binance

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/avast/retry-go"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
)

func retryBinance(ctx context.Context, operation func() error) error {
	return retry.Do(
		operation,
		retry.Attempts(3),
		retry.Delay(200*time.Millisecond),
		retry.DelayType(retry.BackOffDelay),
		retry.MaxDelay(time.Second),
		retry.LastErrorOnly(true),
		retry.Context(ctx),
		retry.RetryIf(func(err error) bool {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return false
			}
			var statusErr *httpclient.StatusError
			if errors.As(err, &statusErr) {
				return statusErr.StatusCode == 429 || statusErr.StatusCode >= 500
			}
			var networkErr net.Error
			return errors.As(err, &networkErr)
		}),
	)
}
