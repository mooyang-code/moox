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

func TestStartEmbeddedServerSkipsWhenDisabled(t *testing.T) {
	closer, err := eventbusbootstrap.StartEmbeddedServer(storageconfig.StorageEventBus{Type: "nats"})
	if err != nil {
		t.Fatalf("StartEmbeddedServer returned error: %v", err)
	}
	if closer != nil {
		t.Fatalf("StartEmbeddedServer should return nil closer when embedded nats is disabled")
	}
}

func TestStartEmbeddedServerStartsJetStream(t *testing.T) {
	closer, err := eventbusbootstrap.StartEmbeddedServer(storageconfig.StorageEventBus{
		Type: "nats",
		Embedded: storageconfig.StorageEmbeddedEventBus{
			Enabled:          true,
			Host:             "127.0.0.1",
			Port:             -1,
			StoreDir:         t.TempDir(),
			StartupTimeoutMS: 3000,
		},
	})
	if err != nil {
		t.Fatalf("StartEmbeddedServer returned error: %v", err)
	}
	if closer == nil {
		t.Fatalf("StartEmbeddedServer returned nil closer")
	}
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Fatalf("close embedded nats failed: %v", err)
		}
	})
}
