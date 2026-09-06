package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	strategyhealth "github.com/mooyang-code/moox/modules/strategy/internal/health"
	strategyoutbox "github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

func TestRequireExecutionDependenciesFailsForEnabledInstanceWithoutTargets(t *testing.T) {
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	session := "session-1"
	if err := repo.SaveStrategyDefinition(context.Background(), store.StrategyDefinition{
		StrategyID: "strategy-1", StrategyName: "strategy", DSLYaml: "name: strategy\ntriggers: {event: {name: source.ready}}\ndata: {bar: 1m, calendar: crypto_24x7}\nrules: {r: {pool: [BTC], score: close, weight: 1}}\n",
		CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateInstance(context.Background(), store.StrategyInstance{
		InstanceID: "instance-1", StrategyID: "strategy-1", SpaceID: "space", InputBindingsJSON: json.RawMessage(`{"source_view_id":"source"}`),
		Enabled: true, SessionID: &session, CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1),
	}); err != nil {
		t.Fatal(err)
	}
	if err := requireExecutionDependencies(context.Background(), repo, Config{}); err == nil {
		t.Fatal("enabled instance without Factor/Storage targets should block startup")
	}
}

func TestStrategyHealthFailsClosedWhileEventBusUnavailable(t *testing.T) {
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	runtime, err := strategyoutbox.NewRuntime(strategyoutbox.RuntimeConfig{
		Store: repo, RelayInterval: time.Millisecond, ReconnectInterval: time.Millisecond, BatchSize: 1,
		Probe:     func(context.Context, strategyoutbox.JetStreamClient) error { return nil },
		Connector: func(context.Context) (strategyoutbox.JetStreamClient, error) { return nil, errors.New("unavailable") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	state := strategyhealth.New("strategy", "strategy", "", "")
	state.SetReady(true)
	response := strategyHealthSnapshot(repo, runtime, state)(context.Background())
	if response.Ready {
		t.Fatal("health must fail closed without EventBus")
	}
	for _, key := range []string{"eventbus_connected", "outbox_pending_count", "oldest_outbox_age_seconds"} {
		if _, ok := response.Details[key]; !ok {
			t.Fatalf("health detail %q missing: %+v", key, response.Details)
		}
	}
}
