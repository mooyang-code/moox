package binance

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/avast/retry-go"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const (
	outboundAttempts         = 3
	maxStorageAttempts       = 3
	storageAttemptTimeoutCap = 1400 * time.Millisecond
	storageRetryDelayReserve = 350 * time.Millisecond
)

func retryStorage(ctx context.Context, operation func() error) error {
	return retryStorageAttempts(ctx, storageRetryAttempts(), operation)
}

// retryStorageWithAttemptTimeout retries a single aggregate write with a
// bounded context per attempt. A transient Gateway response timeout must not
// consume the entire SCF Storage reserve and turn every successful item in the
// batch into a retry item.
func retryStorageWithAttemptTimeout(ctx context.Context, operation func(context.Context) error) error {
	attempts := storageRetryAttempts()
	used := 0
	return retryStorageAttempts(ctx, attempts, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		used++
		attemptCtx, cancel := context.WithTimeout(ctx, storageAttemptTimeout(ctx, attempts-used+1))
		defer cancel()
		err := operation(attemptCtx)
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			// The child deadline belongs to this attempt, not the aggregate
			// Storage reserve. Keep it distinguishable so RetryIf can start the
			// next bounded attempt while the parent budget remains usable.
			return &storageAttemptTimeoutError{err: err}
		}
		return err
	})
}

type storageAttemptTimeoutError struct{ err error }

func (e *storageAttemptTimeoutError) Error() string { return e.err.Error() }
func (e *storageAttemptTimeoutError) Unwrap() error { return e.err }

// retryMetadataStorage keeps metadata registrations retryable even when the
// enclosing SCF uses one-shot writes. Registration is idempotent, and a
// dropped response must not discard an otherwise complete Symbol snapshot.
func retryMetadataStorage(ctx context.Context, operation func() error) error {
	return retryStorageAttempts(ctx, outboundAttempts, operation)
}

func storageRetryAttempts() int {
	raw := os.Getenv("MOOX_FETCH_STORAGE_MAX_ATTEMPTS")
	attempts, err := strconv.Atoi(raw)
	if err != nil || attempts < 1 {
		return outboundAttempts
	}
	if attempts > maxStorageAttempts {
		return maxStorageAttempts
	}
	return attempts
}

func storageAttemptTimeout(ctx context.Context, attemptsLeft int) time.Duration {
	if attemptsLeft < 1 {
		attemptsLeft = 1
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return storageAttemptTimeoutCap
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0
	}
	reservedDelay := time.Duration(attemptsLeft-1) * storageRetryDelayReserve
	available := remaining - reservedDelay
	if available <= 0 {
		return remaining
	}
	budget := available / time.Duration(attemptsLeft)
	if budget > storageAttemptTimeoutCap {
		return storageAttemptTimeoutCap
	}
	return budget
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
			var attemptTimeout *storageAttemptTimeoutError
			if errors.As(err, &attemptTimeout) {
				return ctx.Err() == nil
			}
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
