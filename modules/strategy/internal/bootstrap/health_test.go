package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	strategybus "github.com/mooyang-code/moox/modules/strategy/internal/bus"
	strategyhealth "github.com/mooyang-code/moox/modules/strategy/internal/health"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

func TestStrategyHealthFailsClosedWhileEventBusUnavailable(t *testing.T) {
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	runtime, err := strategybus.NewRuntime(strategybus.RuntimeConfig{
		Store: repo, RelayInterval: time.Millisecond, ReconnectInterval: time.Millisecond, BatchSize: 1,
		Connector: func(context.Context) (strategybus.JetStreamClient, error) { return nil, errors.New("unavailable") },
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Start(context.Background())
	defer runtime.Close()
	state := strategyhealth.New("strategy", "strategy", "", "")
	state.SetReady(true)
	response := strategyHealthSnapshot(repo, runtime, 2, state)(context.Background())
	if response.Ready {
		t.Fatal("health must fail closed without EventBus")
	}
	for _, key := range []string{"eventbus_connected", "outbox_pending_count", "oldest_outbox_age_seconds"} {
		if _, ok := response.Details[key]; !ok {
			t.Fatalf("health detail %q missing: %+v", key, response.Details)
		}
	}
}
