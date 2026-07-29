//go:build e2e_external

package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	strategyoutbox "github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

func TestExternalStrategyCommitPublishesLogicalAccountTarget(t *testing.T) {
	natsURL := os.Getenv("MOOX_STRATEGY_TRADE_E2E_NATS_URL")
	if natsURL == "" {
		t.Fatal("MOOX_STRATEGY_TRADE_E2E_NATS_URL is required by the cross-module harness")
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
		Name: "MOOX_TRADE", Subjects: []string{"moox.trade.target.requested.v1.>"},
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
	if err = repo.SaveStrategy(ctx, domain.Strategy{
		ID: "strategy-e2e", Name: "strategy-e2e",
		ManifestYAML: "api_version: moox.strategy/v1",
		SourceCode:   "def run(context, data, params): return {'action':'hold'}",
		SourceHash:   "hash-strategy-e2e", CreatedAt: time.UnixMilli(1000),
	}); err != nil {
		t.Fatal(err)
	}
	logicalAccountID := "logical-e2e"
	if err = repo.CreateRunner(ctx, domain.StrategyRunner{
		ID: "runner-e2e", StrategyID: "strategy-e2e", SpaceID: "space",
		ViewID: "view", Frequency: "1m", ParamsJSON: []byte(`{}`),
		LogicalAccountID: &logicalAccountID, Status: domain.RunnerStatusEnabled,
		CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(1000),
	}); err != nil {
		t.Fatal(err)
	}
	output := domain.Output{
		Action: domain.ActionRebalance,
		Targets: []domain.InstrumentTarget{{
			InstrumentID: "BTC-USDT-SPOT", Quantity: "0.5",
		}},
	}
	trigger := time.Now().UTC()
	if _, err = repo.CommitResult(ctx, store.CommitResultRequest{
		Result: domain.StrategyResult{
			ID: "strategy-e2e-result", RunnerID: "runner-e2e",
			StrategyID: "strategy-e2e", TriggerBarTime: trigger,
			Namespace: "default", InputHash: "strategy-e2e-input",
			Action: output.Action, CreatedAt: trigger.Add(time.Second),
		},
		Output: output,
	}); err != nil {
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
	relay := strategyoutbox.Relay{
		Store: repo,
		Publisher: &strategyoutbox.JetStreamPublisher{
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
