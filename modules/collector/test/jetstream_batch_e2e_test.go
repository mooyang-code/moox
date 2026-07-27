package test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/cloudjobqueue"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	batchTestSpaceID = "collector-batch-e2e"
	batchTestJobType = "collect.binance.kline"
)

type recordingConsumer struct {
	consumer jetstream.ConsumerAPI

	mu        sync.Mutex
	fetchArgs []int
}

func (c *recordingConsumer) Fetch(ctx context.Context, batch int) ([]*jetstream.Delivery, error) {
	c.mu.Lock()
	c.fetchArgs = append(c.fetchArgs, batch)
	c.mu.Unlock()
	return c.consumer.Fetch(ctx, batch)
}

func (c *recordingConsumer) Close() error {
	return c.consumer.Close()
}

func (c *recordingConsumer) args() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.fetchArgs...)
}

type actionRecord struct {
	id            string
	decision      jetstream.HandlerDecision
	deliveryCount uint64
	err           error
	at            time.Time
}

type actionRecorder struct {
	actions chan actionRecord
}

func (r *actionRecorder) ReportAction(
	_ context.Context,
	delivery *jetstream.Delivery,
	result jetstream.HandlerResult,
	err error,
) {
	record := actionRecord{decision: result.Decision, err: err, at: time.Now()}
	if delivery != nil {
		record.id = delivery.RawMessageID
		record.deliveryCount = delivery.DeliveryCount
	}
	r.actions <- record
}

func TestJetStreamBatchE2EProcessesOneHundredJobsInIndependentFetches(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, registry, publisher, consumer := newBatchE2EQueue(t, ctx, batchTestSpaceID, batchTestJobType, 32)
	recordedConsumer := &recordingConsumer{consumer: consumer}

	var active atomic.Int32
	var maxActive atomic.Int32
	releaseFirstBatch := make(chan struct{})
	var releaseOnce sync.Once
	started := make(map[string]int, 100)
	var startedMu sync.Mutex
	handler := jetstream.DeliveryHandlerFunc(func(_ context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
		decoded := events.DecodeDelivery(registry, delivery)
		if decoded.Err != nil {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: decoded.Err}
		}
		payload, ok := decoded.Payload.(*cloudjobpb.JobExecutionRequested)
		if !ok {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("payload type %T", decoded.Payload)}
		}
		startedMu.Lock()
		started[payload.GetJobItemId()]++
		startedMu.Unlock()

		current := active.Add(1)
		updateMaxActive(&maxActive, current)
		if current <= 10 {
			<-releaseFirstBatch
		}
		time.Sleep(2 * time.Millisecond)
		active.Add(-1)
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	})

	submittedBatches := make([]int, 0, 4)
	for batchStart := 0; batchStart < 100; batchStart += 25 {
		batchEnd := batchStart + 25
		for item := batchStart; item < batchEnd; item++ {
			publishBatchE2EJob(t, ctx, publisher, batchTestSpaceID, batchTestJobType, fmt.Sprintf("job-%03d", item), nil)
		}
		submittedBatches = append(submittedBatches, batchEnd-batchStart)
	}

	recorder := &actionRecorder{actions: make(chan actionRecord, 128)}
	runnerDone := make(chan error, 1)
	go func() {
		runnerDone <- jetstream.NewRunner(recordedConsumer, handler, jetstream.RunnerConfig{
			BatchSize:        10,
			IndependentBatch: true,
			ActionReporter:   recorder,
		}).Run(ctx)
	}()

	waitForAtomicValue(t, &active, 10)
	releaseOnce.Do(func() { close(releaseFirstBatch) })

	counts := map[jetstream.HandlerDecision]int{}
	for total := 0; total < 100; total++ {
		action := waitBatchAction(t, recorder.actions)
		if action.err != nil {
			t.Fatalf("action %s failed: %v", action.id, action.err)
		}
		counts[action.decision]++
	}
	cancel()
	if err := waitRunner(t, runnerDone); err != nil {
		t.Fatalf("runner error = %v", err)
	}

	if fmt.Sprint(submittedBatches) != "[25 25 25 25]" {
		t.Fatalf("submitted batches = %v", submittedBatches)
	}
	for index, batchArg := range recordedConsumer.args() {
		if batchArg != 10 {
			t.Fatalf("fetch argument %d = %d, want 10", index, batchArg)
		}
	}
	if got := maxActive.Load(); got != 10 {
		t.Fatalf("max active handlers = %d, want 10", got)
	}
	if counts[jetstream.ACK] != 100 || counts[jetstream.RETRY] != 0 || counts[jetstream.TERM] != 0 {
		t.Fatalf("action counts = ACK:%d NAK:%d TERM:%d", counts[jetstream.ACK], counts[jetstream.RETRY], counts[jetstream.TERM])
	}
	startedMu.Lock()
	defer startedMu.Unlock()
	if len(started) != 100 {
		t.Fatalf("unique handler starts = %d, want 100", len(started))
	}
	for id, count := range started {
		if count != 1 {
			t.Fatalf("handler %s started %d times, want once", id, count)
		}
	}
}

func TestJetStreamMixedBatchE2EAppliesIndependentActions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, registry, publisher, consumer := newBatchE2EQueue(t, ctx, batchTestSpaceID+"-mixed", batchTestJobType, 10)
	recordedConsumer := &recordingConsumer{consumer: consumer}
	recorder := &actionRecorder{actions: make(chan actionRecord, 16)}

	var startsMu sync.Mutex
	starts := make(map[string]int)
	firstRound := make(chan struct{})
	var firstRoundActive atomic.Int32
	handler := jetstream.DeliveryHandlerFunc(func(_ context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
		decoded := events.DecodeDelivery(registry, delivery)
		if decoded.Err != nil {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: decoded.Err}
		}
		payload := decoded.Payload.(*cloudjobpb.JobExecutionRequested)
		id := payload.GetJobItemId()
		startsMu.Lock()
		starts[id]++
		startNumber := starts[id]
		startsMu.Unlock()

		if delivery.DeliveryCount == 1 {
			if firstRoundActive.Add(1) == 4 {
				close(firstRound)
			}
			<-firstRound
		}
		switch id {
		case "invalid":
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("invalid collector parameters")}
		case "retryable":
			if startNumber == 1 {
				return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: 10 * time.Millisecond, Err: fmt.Errorf("temporary provider failure")}
			}
		case "future":
			if executeAt := payload.GetExecuteAt(); executeAt != nil && time.Now().Before(executeAt.AsTime()) {
				return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Until(executeAt.AsTime())}
			}
		}
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	})

	spaceID := batchTestSpaceID + "-mixed"
	futureAt := time.Now().Add(500 * time.Millisecond)
	publishBatchE2EJob(t, ctx, publisher, spaceID, batchTestJobType, "due", nil)
	publishBatchE2EJob(t, ctx, publisher, spaceID, batchTestJobType, "future", timestamppb.New(futureAt))
	publishBatchE2EJob(t, ctx, publisher, spaceID, batchTestJobType, "retryable", nil)
	publishBatchE2EJob(t, ctx, publisher, spaceID, batchTestJobType, "invalid", nil)

	runnerDone := make(chan error, 1)
	go func() {
		runnerDone <- jetstream.NewRunner(recordedConsumer, handler, jetstream.RunnerConfig{
			BatchSize:        10,
			IndependentBatch: true,
			ActionReporter:   recorder,
		}).Run(ctx)
	}()

	actions := make([]actionRecord, 0, 6)
	for len(actions) < 6 {
		actions = append(actions, waitBatchAction(t, recorder.actions))
	}
	cancel()
	if err := waitRunner(t, runnerDone); err != nil {
		t.Fatalf("runner error = %v", err)
	}

	assertAction(t, actions, "due", jetstream.ACK, 1)
	assertAction(t, actions, "future", jetstream.RETRY, 1)
	futureACK := assertAction(t, actions, "future", jetstream.ACK, 2)
	if futureACK.at.Before(futureAt) {
		t.Fatalf("future job ACK at %s before execute_at %s", futureACK.at, futureAt)
	}
	assertAction(t, actions, "retryable", jetstream.RETRY, 1)
	assertAction(t, actions, "retryable", jetstream.ACK, 2)
	assertAction(t, actions, "invalid", jetstream.TERM, 1)

	for index, batchArg := range recordedConsumer.args() {
		if batchArg != 10 {
			t.Fatalf("fetch argument %d = %d, want 10", index, batchArg)
		}
	}
}

func newBatchE2EQueue(
	t *testing.T,
	ctx context.Context,
	spaceID string,
	jobType string,
	maxAckPending int,
) (*jetstream.Client, *events.Registry, *events.Publisher, *events.Consumer) {
	t.Helper()
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{
		Name:      events.CloudJobExecutionRequested.Stream(),
		Subjects:  []string{"moox.cloudnode.job.execution.requested.v1.>"},
		Storage:   nats.MemoryStorage,
		Retention: nats.WorkQueuePolicy,
	})
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.URL()}, Name: "collector-batch-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		t.Fatal(err)
	}
	identity := cloudjobqueue.Identity{SpaceID: spaceID, JobType: jobType}
	consumerName, err := identity.ConsumerName()
	if err != nil {
		t.Fatal(err)
	}
	subjectID, err := identity.SubjectID()
	if err != nil {
		t.Fatal(err)
	}
	cfg := events.SubjectConsumerConfig{
		ConsumerConfig: events.ConsumerConfig{
			Name:                consumerName,
			Event:               events.CloudJobExecutionRequested,
			AckWait:             500 * time.Millisecond,
			MaxDeliver:          4,
			MaxAckPending:       maxAckPending,
			FetchMaxWait:        100 * time.Millisecond,
			DeliverDecodeErrors: true,
		},
		SpaceID:   spaceID,
		SubjectID: subjectID,
	}
	if _, err := events.EnsureSubjectConsumer(ctx, client, registry, cfg); err != nil {
		t.Fatal(err)
	}
	consumer, err := events.BindSubjectConsumer(ctx, client, registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = consumer.Close() })
	return client, registry, publisher, consumer
}

func publishBatchE2EJob(
	t *testing.T,
	ctx context.Context,
	publisher *events.Publisher,
	spaceID string,
	jobType string,
	itemID string,
	executeAt *timestamppb.Timestamp,
) {
	t.Helper()
	_, err := publisher.Publish(ctx, events.CloudJobExecutionRequested, &cloudjobpb.JobExecutionRequested{
		JobId: "batch-e2e", JobItemId: itemID, JobType: jobType, ExecuteAt: executeAt,
	}, events.PublishOptions{
		EventID: itemID, SpaceID: spaceID, SubjectID: jobType, OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func waitForAtomicValue(t *testing.T, value *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if value.Load() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("atomic value = %d, want %d", value.Load(), want)
}

func updateMaxActive(maximum *atomic.Int32, current int32) {
	for {
		previous := maximum.Load()
		if current <= previous || maximum.CompareAndSwap(previous, current) {
			return
		}
	}
}

func waitBatchAction(t *testing.T, actions <-chan actionRecord) actionRecord {
	t.Helper()
	select {
	case action := <-actions:
		return action
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for JetStream action")
		return actionRecord{}
	}
}

func waitRunner(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not stop")
		return nil
	}
}

func assertAction(
	t *testing.T,
	actions []actionRecord,
	id string,
	decision jetstream.HandlerDecision,
	deliveryCount uint64,
) actionRecord {
	t.Helper()
	for _, action := range actions {
		if action.id == id && action.decision == decision && action.deliveryCount == deliveryCount {
			if action.err != nil {
				t.Fatalf("%s action failed: %v", id, action.err)
			}
			return action
		}
	}
	t.Fatalf("missing action id=%s decision=%d delivery_count=%d in %+v", id, decision, deliveryCount, actions)
	return actionRecord{}
}
