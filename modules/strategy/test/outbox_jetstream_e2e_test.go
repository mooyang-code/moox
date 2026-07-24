package e2e_test

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	strategybus "github.com/mooyang-code/moox/modules/strategy/internal/bus"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/types/known/structpb"
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
	if _, err := js.AddStream(&nats.StreamConfig{Name: "MOOX_STRATEGY", Subjects: []string{"moox.strategy.>"}, Storage: nats.FileStorage, Duplicates: 2 * time.Minute}); err != nil {
		t.Fatal(err)
	}

	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateInitialState(context.Background(), domain.State{BindingID: "binding-1", StrategyVersion: "1", Revision: 0, StateJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	commitDecision(t, repo, "run-1", 0, "2026-07-17T00:00:00Z")
	runtime, err := strategybus.NewRuntime(strategybus.RuntimeConfig{
		Store: repo, InstanceID: "strategy-e2e", RelayInterval: 20 * time.Millisecond,
		ReconnectInterval: 20 * time.Millisecond, BatchSize: 10,
		Probe: func(ctx context.Context, client strategybus.JetStreamClient) error {
			return strategybus.ValidateJetStreamPublisher(ctx, client, "strategy-e2e")
		},
		Connector: func(ctx context.Context) (strategybus.JetStreamClient, error) {
			client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{natsURL}, Name: "strategy-e2e", ConnectTimeout: 200 * time.Millisecond})
			if err != nil {
				return nil, err
			}
			return strategybus.NewManagedClient(client)
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
	commitDecision(t, repo, "run-2", 1, "2026-07-17T00:01:00Z")
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
	subscription, err := verifyJS.SubscribeSync("moox.strategy.output.accepted.v1.>", nats.DeliverAll())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Unsubscribe()
	seen := map[string]int{}
	for seen["run-1"] == 0 || seen["run-2"] == 0 {
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
		if message.Header.Get(nats.MsgIdHdr) != envelope.GetEventId() || envelope.GetEventName() != events.StrategyOutputAccepted.Name {
			t.Fatalf("invalid envelope/header: id=%q envelope=%+v", message.Header.Get(nats.MsgIdHdr), &envelope)
		}
		if _, ok := payload.(*structpb.Struct); !ok {
			t.Fatalf("payload=%T", payload)
		}
		seen[envelope.GetEventId()]++
	}
	if seen["run-1"] != 1 || seen["run-2"] != 1 {
		t.Fatalf("published ids=%v", seen)
	}
	streamInfo, err := verifyJS.StreamInfo("MOOX_STRATEGY")
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

func commitDecision(t *testing.T, repo *store.Store, runID string, revision int64, trigger string) {
	t.Helper()
	task := domain.Task{
		RunID: runID, BindingID: "binding-1", StrategyID: "demo", Version: "1", Namespace: "default",
		TriggerBarTime: trigger, PreviousState: domain.State{BindingID: "binding-1", StrategyVersion: "1", Revision: revision, StateJSON: "{}"},
	}
	output := domain.Output{Action: domain.ActionHold, Targets: []domain.TargetWeight{}, NextState: map[string]any{"revision": revision + 1}}
	if err := repo.Commit(context.Background(), task, output, "hash-"+runID); err != nil {
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
