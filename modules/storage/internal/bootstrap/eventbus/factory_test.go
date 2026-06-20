package eventbus_test

import (
	"context"
	"testing"

	eventbusbootstrap "github.com/mooyang-code/moox/modules/storage/internal/bootstrap/eventbus"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
)

func TestNewRowsChangedBusSupportsMemoryConfig(t *testing.T) {
	bus, err := eventbusbootstrap.NewRowsChangedBus(context.Background(), storageconfig.StorageEventBus{Type: "memory"})
	if err != nil {
		t.Fatalf("NewRowsChangedBus failed: %v", err)
	}
	if _, ok := bus.(*coreeventbus.MemoryBus); !ok {
		t.Fatalf("bus type = %T", bus)
	}
}
