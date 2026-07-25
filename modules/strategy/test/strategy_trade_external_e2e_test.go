package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	strategybus "github.com/mooyang-code/moox/modules/strategy/internal/bus"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

func TestExternalStrategyCommitPublishesRebalance(t *testing.T) {
	natsURL := os.Getenv("MOOX_STRATEGY_TRADE_E2E_NATS_URL")
	if natsURL == "" {
		t.Skip("set MOOX_STRATEGY_TRADE_E2E_NATS_URL to run the cross-module E2E")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = js.AddStream(&nats.StreamConfig{
		Name: "MOOX_TRADE", Subjects: []string{"moox.trade.rebalance.requested.v1.>"},
		Retention: nats.WorkQueuePolicy, Storage: nats.FileStorage,
	}); err != nil {
		t.Fatal(err)
	}
	nc.Close()

	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err = repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	if err = repo.CreateBinding(ctx, domain.Binding{
		BindingID: "binding-e2e", StrategyID: "strategy-e2e", StrategyVersion: "1",
		SpaceID: "space", ViewID: "view", Freq: "1m", GroupID: "group-e2e", Status: "enabled",
	}); err != nil {
		t.Fatal(err)
	}
	if err = repo.CreateExecutionBinding(ctx, domain.ExecutionBinding{
		ExecutionBindingID: "execution-e2e", GroupID: "group-e2e", AccountID: "acct",
		ChannelID: "chan", Mode: "paper", CapitalAmount: "100", QuoteAsset: "USDT", Status: "enabled",
	}); err != nil {
		t.Fatal(err)
	}
	if err = repo.CreateInitialState(ctx, domain.State{
		BindingID: "binding-e2e", StrategyVersion: "1", Revision: 0, StateJSON: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{
		RunID: "strategy-e2e-run", BindingID: "binding-e2e", StrategyID: "strategy-e2e",
		Version: "1", Namespace: "default", SpaceID: "space",
		TriggerBarTime: "2026-07-25T00:00:00Z", DataRevision: "revision-e2e",
		PreviousState: domain.State{BindingID: "binding-e2e", StrategyVersion: "1", Revision: 0},
	}
	output := domain.Output{
		Action: domain.ActionRebalance,
		Targets: []domain.TargetWeight{{
			InstrumentID: "BTC-USDT", Symbol: "BTC-USDT", MarketType: "spot", TargetWeight: "0.5",
		}},
		NextState: map[string]any{"runs": 1},
	}
	if err = repo.Commit(ctx, task, output, "strategy-e2e-input"); err != nil {
		t.Fatal(err)
	}

	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{natsURL}, Name: "strategy-trade-e2e-producer"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		t.Fatal(err)
	}
	relay := strategybus.Relay{
		Store: repo,
		Publisher: &strategybus.JetStreamPublisher{
			Publisher: publisher, InstanceID: "strategy-trade-e2e",
		},
	}
	if err = relay.PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	}
	stats, err := repo.PendingOutboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.PendingCount != 0 {
		t.Fatalf("pending outbox=%d", stats.PendingCount)
	}
}
