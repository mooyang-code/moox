package binance

import (
	"context"
	"errors"
	"time"

	"github.com/avast/retry-go"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const outboundAttempts = 3

type singleAttemptContextKey struct{}

// SingleAttempt marks Storage calls as one-shot for short-lived SCF batches.
func SingleAttempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, singleAttemptContextKey{}, true)
}

func retryStorage(ctx context.Context, operation func() error) error {
	attempts := outboundAttempts
	if single, _ := ctx.Value(singleAttemptContextKey{}).(bool); single {
		attempts = 1
	}
	return retryStorageAttempts(ctx, attempts, operation)
}

// retryMetadataStorage keeps metadata registrations retryable even when the
// enclosing SCF uses one-shot writes. Registration is idempotent, and a
// dropped response must not discard an otherwise complete Symbol snapshot.
func retryMetadataStorage(ctx context.Context, operation func() error) error {
	return retryStorageAttempts(ctx, outboundAttempts, operation)
}

func retryStorageAttempts(ctx context.Context, attempts int, operation func() error) error {
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
			var responseErr *storageResponseError
			if errors.As(err, &responseErr) {
				return responseErr.code == storagepb.ErrorCode_INNER_ERR
			}
			return true
		}),
	)
}
