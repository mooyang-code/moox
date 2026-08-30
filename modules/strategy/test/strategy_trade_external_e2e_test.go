//go:build e2e_external

package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestExternalStrategyCommitPublishesLogicalAccountTarget(t *testing.T) {
	if os.Getenv("MOOX_STRATEGY_TRADE_E2E_NATS_URL") == "" {
		t.Fatal("MOOX_STRATEGY_TRADE_E2E_NATS_URL is required")
	}
	natsURL := os.Getenv("MOOX_STRATEGY_TRADE_E2E_NATS_URL")
	nc, err := nats.Connect(natsURL)
	require.NoError(t, err)
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: "MOOX_TRADE", Subjects: []string{"moox.trade.target.weight_requested.v1.>"}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	nc.Close()
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveStrategy(context.Background(), domain.Strategy{ID: "strategy-e2e", Name: "strategy-e2e", Kind: "coin_selection", ManifestYAML: "api_version: moox.strategy/v2", CompiledJSON: []byte(`{"api_version":"moox.strategy/v2","kind":"coin_selection","space_id":"space"}`), SourceHash: "hash", CreatedAt: time.UnixMilli(1000)}); err != nil {
		t.Fatal(err)
	}
	logical := "logical-e2e"
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{ID: "runner-e2e", StrategyID: "strategy-e2e", SpaceID: "space", SourceViewID: "view", Frequency: "1m", LogicalAccountID: &logical, Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(1000)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_, err = repo.CommitEvaluation(context.Background(), store.CommitEvaluationRequest{Result: domain.StrategyResult{ID: "strategy-e2e-result", RunnerID: "runner-e2e", StrategyID: "strategy-e2e", PeriodTime: now, InputHash: "input", Action: domain.ActionRebalance, CreatedAt: now}, Evaluation: domain.Evaluation{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", TargetWeight: "0.5"}}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{natsURL}, Name: "strategy-e2e-publisher"})
	require.NoError(t, err)
	managed, err := outbox.NewManagedClient(client)
	require.NoError(t, err)
	t.Cleanup(func() { _ = managed.Close() })
	relay := &outbox.Relay{Store: repo, Publisher: &outbox.JetStreamPublisher{Publisher: managed.EventPublisher(), InstanceID: "strategy-e2e"}}
	require.NoError(t, relay.PublishPending(ctx, 10))
}
