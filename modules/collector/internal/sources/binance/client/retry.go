package binance

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/avast/retry-go"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
)

type singleAttemptContextKey struct{}

// SingleAttempt marks an outbound provider request as one-shot. Short-lived
// SCF batches retry at the Collector boundary instead of spending the 10s
// function budget on in-function backoff.
func SingleAttempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, singleAttemptContextKey{}, true)
}

func retryBinance(ctx context.Context, operation func() error) error {
	attempts := 3
	if single, _ := ctx.Value(singleAttemptContextKey{}).(bool); single {
		attempts = 1
	}
	return retry.Do(
		operation,
		retry.Attempts(uint(attempts)),
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
