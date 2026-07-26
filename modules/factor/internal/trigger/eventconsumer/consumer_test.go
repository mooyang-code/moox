package eventconsumer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/testkit"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

type fakeNATSSession struct {
	run        func(context.Context) error
	closeCalls atomic.Int32
}

type pendingStoreFake struct {
	mu        sync.Mutex
	rows      map[string]pendingStoreRow
	processed map[string]struct{}
}

type pendingStoreRow struct {
	event      *storagepb.DatasetRowsUpserted
	receivedAt time.Time
}

func (s *pendingStoreFake) pendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

func (s *pendingStoreFake) ClaimPendingEvent(_ context.Context, id string, event *storagepb.DatasetRowsUpserted, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[string]pendingStoreRow{}
	}
	if _, ok := s.processed[id]; ok {
		return false, nil
	}
	if _, ok := s.rows[id]; ok {
		return false, nil
	}
	s.rows[id] = pendingStoreRow{event: proto.Clone(event).(*storagepb.DatasetRowsUpserted), receivedAt: at}
	return true, nil
}

func (s *pendingStoreFake) LoadPendingEvents(_ context.Context, visit func(string, *storagepb.DatasetRowsUpserted, time.Time) error) error {
	for id, row := range s.rows {
		if err := visit(id, proto.Clone(row.event).(*storagepb.DatasetRowsUpserted), row.receivedAt); err != nil {
			return err
		}
	}
	return nil
}

func (s *pendingStoreFake) CommitPendingEvents(_ context.Context, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.processed == nil {
		s.processed = map[string]struct{}{}
	}
	for _, id := range ids {
		s.processed[id] = struct{}{}
		delete(s.rows, id)
	}
	return nil
}

func binding(factorID, sourceDataset, subjectMode, subjectsJSON string) domain.FactorBinding {
	return domain.FactorBinding{
		BindingID: "bind-" + factorID, FactorID: factorID, SpaceID: "crypto",
		SourceDataset: sourceDataset, Freq: "1m", SubjectMode: subjectMode,
		SubjectsJSON: subjectsJSON, TargetDataset: "binance_spot_factor",
		Status: domain.BindingStatusEnabled,
	}
}

func event(spaceID, datasetID, subjectID, freq string, dataTime time.Time) *storagepb.DatasetRowsUpserted {
	return &storagepb.DatasetRowsUpserted{
		SpaceId: spaceID, DatasetId: datasetID,
		Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{
			SpaceId: spaceID, DatasetId: datasetID,
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: subjectID, Freq: freq, DataTime: dataTime.Format(time.RFC3339),
			}},
		}}},
	}
}

func (s *fakeNATSSession) Run(ctx context.Context) error {
	return s.run(ctx)
}

func (s *fakeNATSSession) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func TestConsumerReopensFailedSessionAndRestoresReadiness(t *testing.T) {
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
	consumer := New(Config{}, nil)
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
	if !eventuallyConsumer(time.Second, func() bool { return !consumer.Ready() }) {
		t.Fatal("consumer did not become unready after the failed session")
	}
	if !eventuallyConsumer(time.Second, func() bool { return consumer.Ready() }) {
		t.Fatal("consumer did not recover readiness")
	}
	<-recovered
	if opens.Load() != 1 || first.closeCalls.Load() != 1 {
		t.Fatalf("opens=%d first closes=%d", opens.Load(), first.closeCalls.Load())
	}
}

func eventuallyConsumer(timeout time.Duration, condition func() bool) bool {
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
	ref := liveConsumerConfig(Config{FetchMaxWait: 2 * time.Second})
	if ref.Event.Name() != events.DatasetRowsUpserted.Name() ||
		ref.Event.Version() != events.DatasetRowsUpserted.Version() ||
		ref.Name != DatasetRowsConsumerName ||
		ref.FetchMaxWait != 2*time.Second {
		t.Fatalf("live consumer bind ref = %+v", ref)
	}
}

func TestConsumerReceivesRealEventBusDeliveryE2E(t *testing.T) {
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
	batcher := trigger.NewDurableEventBatcher(20*time.Millisecond, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	}, inbox)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	consumer := New(Config{
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
	if !eventuallyConsumer(5*time.Second, func() bool { return inbox.pendingCount() == 1 }) {
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
	d := trigger.NewEventBatcher(time.Second, []domain.FactorBinding{{
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
	tasks, err := d.FlushPending(context.Background(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
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
	d := trigger.NewEventBatcher(time.Second, []domain.FactorBinding{
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
	tasks, err := d.FlushPending(context.Background(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
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
