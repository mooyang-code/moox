package eventbus

import (
	"context"
	"testing"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRowsCommittedBus_EmptyType_ShouldReturnMemoryBus(t *testing.T) {
	bus, err := NewRowsCommittedBus(context.Background(), storageconfig.StorageEventBus{})
	require.NoError(t, err)
	require.NotNil(t, bus)
	_, ok := bus.(*coreeventbus.MemoryBus)
	assert.True(t, ok)
}

func TestNewRowsCommittedBus_MemoryType_ShouldReturnMemoryBus(t *testing.T) {
	bus, err := NewRowsCommittedBus(context.Background(), storageconfig.StorageEventBus{Type: "memory"})
	require.NoError(t, err)
	require.NotNil(t, bus)
	_, ok := bus.(*coreeventbus.MemoryBus)
	assert.True(t, ok)
}

func TestNewRowsCommittedBus_UnsupportedType_ShouldReturnError(t *testing.T) {
	_, err := NewRowsCommittedBus(context.Background(), storageconfig.StorageEventBus{Type: "kafka"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported storage eventbus type")
}

func TestNewRowsCommittedBus_JetStreamRejectsInvalidDeliveryConfigBeforeConnect(t *testing.T) {
	_, err := NewRowsCommittedBus(context.Background(), storageconfig.StorageEventBus{
		Type: "jetstream", NATSURL: "nats://127.0.0.1:1", AckWaitMS: 1000,
		MaxInFlight: 2, MaxAckPending: 1, MaxDeliver: -1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ack_wait_ms")
}
