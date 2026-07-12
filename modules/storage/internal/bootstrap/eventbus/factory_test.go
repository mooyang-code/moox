package eventbus

import (
	"context"
	"testing"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRowsUpdatedBus_EmptyType_ShouldReturnMemoryBus(t *testing.T) {
	bus, err := NewRowsUpdatedBus(context.Background(), storageconfig.StorageEventBus{})
	require.NoError(t, err)
	require.NotNil(t, bus)
	_, ok := bus.(*coreeventbus.MemoryBus)
	assert.True(t, ok)
}

func TestNewRowsUpdatedBus_MemoryType_ShouldReturnMemoryBus(t *testing.T) {
	bus, err := NewRowsUpdatedBus(context.Background(), storageconfig.StorageEventBus{Type: "memory"})
	require.NoError(t, err)
	require.NotNil(t, bus)
	_, ok := bus.(*coreeventbus.MemoryBus)
	assert.True(t, ok)
}

func TestNewRowsUpdatedBus_UnsupportedType_ShouldReturnError(t *testing.T) {
	_, err := NewRowsUpdatedBus(context.Background(), storageconfig.StorageEventBus{Type: "kafka"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported storage eventbus type")
}
