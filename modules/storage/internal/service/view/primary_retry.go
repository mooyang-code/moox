package view

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	backoff "github.com/cenkalti/backoff/v4"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	trpcerrs "trpc.group/trpc-go/trpc-go/errs"
)

const (
	primaryHistoryReadAttempts = 3
	primaryHistoryRetryDelay   = 100 * time.Millisecond
)

// readPrimaryTimeSeriesRows retries only transport failures. A Primary
// history request is idempotent, while validation and business errors should
// fail immediately and preserve the original error for the rebuild log.
func readPrimaryTimeSeriesRows(ctx context.Context, reader TimeSeriesRangeReader, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if reader == nil {
		return nil, errors.New("Primary history reader is required")
	}
	var (
		response *pb.ReadTimeSeriesRowsRsp
		attempts int
	)
	operation := func() error {
		attempts++
		var err error
		response, err = reader.ReadTimeSeriesRows(ctx, req)
		if err == nil {
			if response == nil {
				return backoff.Permanent(errors.New("Primary history reader returned an empty response"))
			}
			return nil
		}
		if !isRetryablePrimaryHistoryError(err) {
			return backoff.Permanent(err)
		}
		return err
	}
	policy := backoff.WithContext(
		backoff.WithMaxRetries(backoff.NewConstantBackOff(primaryHistoryRetryDelay), primaryHistoryReadAttempts-1),
		ctx,
	)
	if err := backoff.Retry(operation, policy); err != nil {
		return response, fmt.Errorf("Primary history request failed after %d attempts: %w", attempts, err)
	}
	return response, nil
}

func isRetryablePrimaryHistoryError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	code := trpcerrs.Code(err)
	switch code {
	case trpcerrs.RetClientConnectFail,
		trpcerrs.RetClientNetErr,
		trpcerrs.RetClientTimeout,
		trpcerrs.RetClientFullLinkTimeout,
		trpcerrs.RetClientReadFrameErr:
		return true
	}
	if code != trpcerrs.RetUnknown {
		return false
	}
	// Some transport adapters return a plain error instead of preserving the
	// framework code. Keep the fallback narrow so malformed requests and
	// business failures are never retried.
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection refused",
		"connection reset",
		"connection pool",
		"client timeout",
		"deadline exceeded",
		"read frame",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
