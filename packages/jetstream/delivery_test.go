package jetstream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeliveryAckBoundsTransportAction(t *testing.T) {
	delivery := &Delivery{
		actionTimeout: 20 * time.Millisecond,
		ackFn: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}

	started := time.Now()
	err := delivery.Ack(context.Background())

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.Less(t, time.Since(started), 200*time.Millisecond)
}

func TestDeliveryActionPreservesShorterCallerDeadline(t *testing.T) {
	delivery := &Delivery{
		actionTimeout: time.Second,
		progressFn: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := delivery.InProgress(ctx)

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.Less(t, time.Since(started), 200*time.Millisecond)
}
