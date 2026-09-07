package eventconsumer

import (
	"context"
	"errors"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type targetResolverFunc func(context.Context, int64, *tradeeventpb.LogicalAccountTargetWeightRequested, string) (targetapp.WeightConversion, error)

func (f targetResolverFunc) Resolve(ctx context.Context, at int64, request *tradeeventpb.LogicalAccountTargetWeightRequested, space string) (targetapp.WeightConversion, error) {
	return f(ctx, at, request, space)
}

func TestTargetResolutionHasDeadlineAndTimeoutDoesNotAccept(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.Now().UTC()
	opts := targetOptions(tradeStore, now)
	opts.ResolveTimeout = 20 * time.Millisecond
	opts.WeightResolver = targetResolverFunc(func(ctx context.Context, _ int64, _ *tradeeventpb.LogicalAccountTargetWeightRequested, _ string) (targetapp.WeightConversion, error) {
		if _, ok := ctx.Deadline(); !ok {
			return targetapp.WeightConversion{}, errors.New("resolver context has no deadline")
		}
		<-ctx.Done()
		return targetapp.WeightConversion{}, ctx.Err()
	})
	result := HandleTarget(context.Background(), logicalTargetDelivery(t, now, "target-timeout", "runner-1", "logical-1", 1, nil), opts)
	require.Equal(t, jetstream.RETRY, result.Decision)
	require.ErrorIs(t, result.Err, context.DeadlineExceeded)
	_, err := tradeStore.GetTargetReceipt(context.Background(), "space-1", "target-timeout")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	opts.WeightResolver = testWeightResolver{}
	result = HandleTarget(context.Background(), logicalTargetDelivery(t, now, "target-timeout", "runner-1", "logical-1", 1, nil), opts)
	require.Equal(t, jetstream.ACK, result.Decision, result.Err)
}
