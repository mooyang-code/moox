package jetstream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
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

func TestDeliveryAckDoesNotWaitForServerConfirmation(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Shutdown()
	conn, err := nats.Connect(url)
	require.NoError(t, err)
	defer conn.Close()

	sub, err := conn.SubscribeSync("delivery.test")
	require.NoError(t, err)
	require.NoError(t, conn.PublishRequest("delivery.test", "missing.ack.responder", []byte("payload")))
	msg, err := sub.NextMsg(time.Second)
	require.NoError(t, err)

	delivery := &Delivery{msg: msg, actionTimeout: 50 * time.Millisecond}
	started := time.Now()
	require.NoError(t, delivery.Ack(context.Background()))
	assert.Less(t, time.Since(started), 50*time.Millisecond)
}
