package test

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	batchTestSpaceID = "market-fetch-batch-e2e"
	batchTestSubject = "bars"
)

type actionRecord struct {
	id            string
	decision      jetstream.HandlerDecision
	deliveryCount uint64
	err           error
}

type actionRecorder struct{ actions chan actionRecord }

func (r *actionRecorder) ReportAction(_ context.Context, delivery *jetstream.Delivery, result jetstream.HandlerResult, err error) {
	record := actionRecord{decision: result.Decision, err: err}
	if delivery != nil {
		record.id = delivery.RawMessageID
		record.deliveryCount = delivery.DeliveryCount
	}
	r.actions <- record
}

func newBatchE2EQueue(t *testing.T, ctx context.Context, spaceID, subjectID string, maxAckPending int) (*events.Registry, *events.Publisher, *events.Consumer) {
	t.Helper()
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{
		Name: events.MarketFetchBatchCompleted.Stream(), Subjects: []string{"moox.event.market.fetch.batch.completed.v1.>"},
		Storage: nats.MemoryStorage, Retention: nats.LimitsPolicy,
	})
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.URL()}, Name: "market-fetch-batch-e2e"})
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
	cfg := events.SubjectConsumerConfig{
		ConsumerConfig: events.ConsumerConfig{Name: "market-fetch-redelivery-e2e", Event: events.MarketFetchBatchCompleted, AckWait: 500 * time.Millisecond, MaxDeliver: 4, MaxAckPending: maxAckPending, FetchMaxWait: 100 * time.Millisecond, DeliverDecodeErrors: true},
		SpaceID:        spaceID, SubjectID: subjectID,
	}
	if _, err := events.EnsureSubjectConsumer(ctx, client, registry, cfg); err != nil {
		t.Fatal(err)
	}
	consumer, err := events.BindSubjectConsumer(ctx, client, registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = consumer.Close() })
	return registry, publisher, consumer
}

func publishBatchE2ECompletion(t *testing.T, ctx context.Context, publisher *events.Publisher, spaceID, subjectID, batchID string) {
	t.Helper()
	_, err := publisher.Publish(ctx, events.MarketFetchBatchCompleted, &marketfetchpb.MarketFetchBatchCompleted{
		BatchId: batchID, ScheduleId: "e2e", BatchKind: "realtime", DatasetId: subjectID, Frequency: "1m", NodeId: "node-1", PlannedCount: 1, Status: "succeeded", SuccessCount: 1,
		CompletedAt: timestamppb.Now(), Items: []*marketfetchpb.MarketFetchItemResult{{SubjectId: "BTC-USDT", Symbol: "BTCUSDT", Outcome: "success"}},
	}, events.PublishOptions{EventID: batchID, SpaceID: spaceID, SubjectID: subjectID, OccurredAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
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
