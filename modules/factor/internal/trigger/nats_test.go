package trigger

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/testkit"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

type fakeNATSSession struct {
	run        func(context.Context) error
	closeCalls atomic.Int32
}

func (s *fakeNATSSession) Run(ctx context.Context) error {
	return s.run(ctx)
}

func (s *fakeNATSSession) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func TestNATSConsumerReopensFailedSessionAndRestoresReadiness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	recovered := make(chan struct{})
	first := &fakeNATSSession{run: func(context.Context) error {
		close(started)
		<-release
		return errors.New("fetch failed")
	}}
	second := &fakeNATSSession{run: func(ctx context.Context) error {
		close(recovered)
		<-ctx.Done()
		return nil
	}}
	var opens atomic.Int32
	consumer := NewNATSConsumer(NATSConfig{}, nil)
	consumer.retryDelay = 50 * time.Millisecond
	consumer.openSession = func(context.Context) (natsConsumerSession, error) {
		opens.Add(1)
		return second, nil
	}
	consumer.startSessionLoop(ctx, first)
	t.Cleanup(func() { _ = consumer.Close() })

	<-started
	if !consumer.Ready() {
		t.Fatal("consumer must be ready while the initial session is running")
	}
	close(release)
	if !eventuallyNATSConsumer(time.Second, func() bool { return !consumer.Ready() }) {
		t.Fatal("consumer did not become unready after the failed session")
	}
	if !eventuallyNATSConsumer(time.Second, func() bool { return consumer.Ready() }) {
		t.Fatal("consumer did not recover readiness")
	}
	<-recovered
	if opens.Load() != 1 || first.closeCalls.Load() != 1 {
		t.Fatalf("opens=%d first closes=%d", opens.Load(), first.closeCalls.Load())
	}
}

func eventuallyNATSConsumer(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return condition()
}

func TestLiveConsumerConfig(t *testing.T) {
	ref := liveConsumerConfig(NATSConfig{FetchMaxWait: 2 * time.Second})
	if ref.Event.Name() != events.DatasetRowsUpserted.Name() ||
		ref.Event.Version() != events.DatasetRowsUpserted.Version() ||
		ref.Name != LiveConsumer ||
		ref.FetchMaxWait != 2*time.Second {
		t.Fatalf("live consumer bind ref = %+v", ref)
	}
}

func TestNATSConsumerReceivesRealEventBusDeliveryE2E(t *testing.T) {
	ns, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		ns.Shutdown()
		t.Fatal("factor EventBus fixture did not start")
	}
	t.Cleanup(func() {
		ns.Shutdown()
		ns.WaitForShutdown()
	})

	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		t.Fatal(err)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		nc.Close()
		t.Fatal(err)
	}
	family, err := registry.FamilyPattern(events.DatasetRowsUpserted)
	if err != nil {
		nc.Close()
		t.Fatal(err)
	}
	if _, err = js.AddStream(&nats.StreamConfig{
		Name: events.DatasetRowsUpserted.Stream(), Subjects: []string{family}, Storage: nats.MemoryStorage,
	}); err != nil {
		nc.Close()
		t.Fatal(err)
	}
	nc.Close()

	now := time.Now().UTC().Truncate(time.Second)
	inbox := &pendingStoreFake{}
	batcher := NewDurableEventBatcher(20*time.Millisecond, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	}, inbox)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	consumer := NewNATSConsumer(NATSConfig{
		URLs: []string{ns.ClientURL()}, FetchMaxWait: 50 * time.Millisecond,
	}, batcher)
	if err = consumer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = consumer.Close() })
	if !consumer.Ready() {
		t.Fatal("factor consumer is not ready after binding the real session")
	}

	client, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv([]string{ns.ClientURL()}, "factor-real-e2e-publisher"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		t.Fatal(err)
	}
	payload := event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now)
	if _, err = publisher.Publish(ctx, events.DatasetRowsUpserted, payload, events.PublishOptions{
		EventID: "factor-real-e2e-1", OccurredAt: now, SpaceID: "crypto", SubjectID: "binance_spot_kline",
	}); err != nil {
		t.Fatal(err)
	}
	if !eventuallyNATSConsumer(5*time.Second, func() bool { return inbox.pendingCount() == 1 }) {
		t.Fatal("real EventBus delivery did not reach Factor's durable inbox")
	}
	tasks, err := batcher.FlushPending(ctx, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].SubjectID != "BTC-USDT" ||
		len(tasks[0].PendingEventIDs) != 1 || tasks[0].PendingEventIDs[0] != "factor-real-e2e-1" {
		t.Fatalf("factor tasks = %+v", tasks)
	}
}

func TestEventStormEmitsOneTaskPerSubject(t *testing.T) {
	symbols := testkit.Symbols(500)
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	d := NewEventBatcher(time.Second, []domain.FactorBinding{{
		BindingID:     "b1",
		FactorID:      "bias",
		SpaceID:       "crypto",
		SourceDataset: "binance_spot_kline",
		Freq:          "1m",
		SubjectMode:   domain.SubjectModeAll,
		SubjectsJSON:  "[]",
		TargetDataset: "binance_spot_factor",
		Status:        domain.BindingStatusEnabled,
	}})

	d.Ingest(testkit.RowsChangedEvent("crypto", "binance_spot_kline", "1m", now, symbols), now)
	tasks := d.Flush(now.Add(time.Second))
	if len(tasks) != len(symbols) {
		t.Fatalf("tasks = %d, want %d", len(tasks), len(symbols))
	}
	seen := map[string]struct{}{}
	for _, task := range tasks {
		if len(task.FactorIDs) != 1 || task.FactorIDs[0] != "bias" {
			t.Fatalf("factor ids = %#v", task.FactorIDs)
		}
		seen[task.SubjectID] = struct{}{}
	}
	if len(seen) != len(symbols) {
		t.Fatalf("unique subjects = %d, want %d", len(seen), len(symbols))
	}
}

func TestEventBatcherSplitsTasksByTargetDataset(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	d := NewEventBatcher(time.Second, []domain.FactorBinding{
		{
			BindingID:     "b1",
			FactorID:      "bias",
			SpaceID:       "crypto",
			SourceDataset: "binance_spot_kline",
			Freq:          "1m",
			SubjectMode:   domain.SubjectModeAll,
			SubjectsJSON:  "[]",
			TargetDataset: "binance_spot_factor",
			Status:        domain.BindingStatusEnabled,
		},
		{
			BindingID:     "b2",
			FactorID:      "volume",
			SpaceID:       "crypto",
			SourceDataset: "binance_spot_kline",
			Freq:          "1m",
			SubjectMode:   domain.SubjectModeAll,
			SubjectsJSON:  "[]",
			TargetDataset: "binance_spot_volume_factor",
			Status:        domain.BindingStatusEnabled,
		},
	})

	d.Ingest(testkit.RowsChangedEvent("crypto", "binance_spot_kline", "1m", now, []string{"BTC-USDT"}), now)
	tasks := d.Flush(now.Add(time.Second))
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2: %+v", len(tasks), tasks)
	}
	byTarget := map[string][]string{}
	for _, task := range tasks {
		byTarget[task.TargetDataset] = task.FactorIDs
	}
	if got := byTarget["binance_spot_factor"]; len(got) != 1 || got[0] != "bias" {
		t.Fatalf("binance_spot_factor ids = %#v", got)
	}
	if got := byTarget["binance_spot_volume_factor"]; len(got) != 1 || got[0] != "volume" {
		t.Fatalf("binance_spot_volume_factor ids = %#v", got)
	}
}
