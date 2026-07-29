package e2e_test

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	strategyoutbox "github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func TestStrategyOutboxJetStreamReconnectAndCatchUp(t *testing.T) {
	storeDir := t.TempDir()
	natsServer := startStrategyNATS(t, &server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: storeDir})
	port := natsServer.Addr().(*net.TCPAddr).Port
	natsURL := fmt.Sprintf("nats://127.0.0.1:%d", port)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{Name: "MOOX_TRADE", Subjects: []string{"moox.trade.>"}, Storage: nats.FileStorage, Duplicates: 2 * time.Minute}); err != nil {
		t.Fatal(err)
	}

	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveStrategy(context.Background(), domain.Strategy{
		ID: "demo", Name: "demo", ManifestYAML: "api_version: moox.strategy/v1",
		SourceCode: "def run(context, data, params): return {'action':'hold'}",
		SourceHash: "hash-demo", CreatedAt: time.UnixMilli(1000),
	}); err != nil {
		t.Fatal(err)
	}
	logicalAccountID := "logical-1"
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{
		ID: "runner-1", StrategyID: "demo", SpaceID: "space", ViewID: "view-1",
		Frequency: "1m", ParamsJSON: []byte(`{}`), LogicalAccountID: &logicalAccountID,
		Status: domain.RunnerStatusDisabled, CreatedAt: time.UnixMilli(1000),
		UpdatedAt: time.UnixMilli(1000),
	}); err != nil {
		t.Fatal(err)
	}
	commitDecision(t, repo, "result-1", time.Now().UTC())
	runtime, err := strategyoutbox.NewRuntime(strategyoutbox.RuntimeConfig{
		Store: repo, InstanceID: "strategy-e2e", RelayInterval: 20 * time.Millisecond,
		ReconnectInterval: 20 * time.Millisecond, BatchSize: 10,
		Probe: func(ctx context.Context, client strategyoutbox.JetStreamClient) error {
			return strategyoutbox.ValidateJetStreamPublisher(ctx, client, "strategy-e2e")
		},
		Connector: func(ctx context.Context) (strategyoutbox.JetStreamClient, error) {
			client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{natsURL}, Name: "strategy-e2e", ConnectTimeout: 200 * time.Millisecond})
			if err != nil {
				return nil, err
			}
			return strategyoutbox.NewManagedClient(client)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = runtime.Close()
		_ = repo.Close()
	})
	eventuallyStrategy(t, 3*time.Second, func() bool {
		stats, _ := repo.PendingOutboxStats(context.Background())
		return runtime.Connected() && stats.PendingCount == 0
	})

	natsServer.Shutdown()
	natsServer.WaitForShutdown()
	nc.Close()
	commitDecision(t, repo, "result-2", time.Now().UTC().Add(time.Minute))
	eventuallyStrategy(t, 3*time.Second, func() bool {
		stats, _ := repo.PendingOutboxStats(context.Background())
		return !runtime.Connected() && stats.PendingCount == 1
	})

	natsServer = startStrategyNATS(t, &server.Options{Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: storeDir})
	t.Cleanup(natsServer.Shutdown)
	eventuallyStrategy(t, 5*time.Second, func() bool {
		stats, _ := repo.PendingOutboxStats(context.Background())
		return runtime.Connected() && stats.PendingCount == 0
	})

	verifyNC, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer verifyNC.Close()
	verifyJS, _ := verifyNC.JetStream()
	subscription, err := verifyJS.SubscribeSync("moox.trade.target.requested.v1.>", nats.DeliverAll())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Unsubscribe()
	seen := map[string]int{}
	firstID, secondID := "result-1", "result-2"
	for seen[firstID] == 0 || seen[secondID] == 0 {
		message, err := subscription.NextMsg(3 * time.Second)
		if err != nil {
			t.Fatal(err)
		}
		registry, err := events.DefaultRegistry()
		if err != nil {
			t.Fatal(err)
		}
		envelope, payload, err := events.DecodeRaw(registry, message.Data, message.Subject, message.Header.Get(nats.MsgIdHdr), message.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		if message.Header.Get(nats.MsgIdHdr) != envelope.GetEventId() ||
			envelope.GetEventName() != events.LogicalAccountTargetRequested.Name() {
			t.Fatalf("invalid envelope/header: id=%q envelope=%+v", message.Header.Get(nats.MsgIdHdr), &envelope)
		}
		target, ok := payload.(*tradeeventpb.LogicalAccountTargetRequested)
		if !ok || target.GetLogicalAccountId() != "logical-1" ||
			target.GetRunnerId() != "runner-1" {
			t.Fatalf("payload=%T", payload)
		}
		seen[envelope.GetEventId()]++
	}
	if seen[firstID] != 1 || seen[secondID] != 1 {
		t.Fatalf("published ids=%v", seen)
	}
	streamInfo, err := verifyJS.StreamInfo("MOOX_TRADE")
	if err != nil {
		t.Fatal(err)
	}
	if streamInfo.State.Msgs != 2 {
		t.Fatalf("stream message count=%d, want two decisions", streamInfo.State.Msgs)
	}
}

func startStrategyNATS(t *testing.T, options *server.Options) *server.Server {
	t.Helper()
	natsServer, err := server.NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	go natsServer.Start()
	if !natsServer.ReadyForConnections(5 * time.Second) {
		t.Fatal("embedded NATS did not become ready")
	}
	return natsServer
}

func commitDecision(
	t *testing.T,
	repo *store.Store,
	resultID string,
	trigger time.Time,
) {
	t.Helper()
	output := domain.Output{
		Action: domain.ActionRebalance,
		Targets: []domain.InstrumentTarget{{
			InstrumentID: "BTC-USDT-SPOT", Quantity: "0.5",
		}},
	}
	_, err := repo.CommitResult(context.Background(), store.CommitResultRequest{
		Result: domain.StrategyResult{
			ID: resultID, RunnerID: "runner-1", StrategyID: "demo",
			TriggerBarTime: trigger, Namespace: "default", InputHash: "hash-" + resultID,
			Action: output.Action, CreatedAt: trigger.Add(time.Second),
		},
		Output: output,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func eventuallyStrategy(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met")
}
