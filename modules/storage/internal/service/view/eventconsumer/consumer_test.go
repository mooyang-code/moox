package eventconsumer

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

type datasetRowsHandlerFunc func(context.Context, *eventpb.EventMessage, *storagepb.DatasetRowsUpserted) error

func (f datasetRowsHandlerFunc) HandleDatasetRows(ctx context.Context, message *eventpb.EventMessage, payload *storagepb.DatasetRowsUpserted) error {
	return f(ctx, message, payload)
}

func TestConfigDefaults(t *testing.T) {
	config, err := (Config{}).withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if config.Consumer != "storage_view_period_v1" || config.AckWaitMS != 120000 || config.FetchBatch != 1 || config.MaxWorkers != 1 || config.MaxAckPending != 1 || config.MaxRetryAttempts != -1 || config.Ordering != "subject" || config.DeliverPolicy != "all" {
		t.Fatalf("config = %+v", config)
	}
}

func TestConfigNormalizesDeliverPolicy(t *testing.T) {
	config, err := (Config{DeliverPolicy: " NEW "}).withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if config.DeliverPolicy != "new" {
		t.Fatalf("deliver policy = %q, want new", config.DeliverPolicy)
	}
	if _, err := (Config{DeliverPolicy: "last"}).withDefaults(); err == nil {
		t.Fatal("unsupported deliver policy was accepted")
	}
}

func TestConfigAcceptsUnlimitedRetryAttempts(t *testing.T) {
	if config, err := (Config{MaxRetryAttempts: -1}).withDefaults(); err != nil || config.MaxRetryAttempts != -1 {
		t.Fatalf("unlimited MaxRetryAttempts = %+v, err=%v", config, err)
	}
	if _, err := (Config{MaxRetryAttempts: -2}).withDefaults(); err == nil {
		t.Fatal("invalid negative MaxRetryAttempts was accepted")
	}
}

func TestShouldRebindFetchTransportErrors(t *testing.T) {
	for _, err := range []error{nats.ErrFetchDisconnected, nats.ErrDisconnected, nats.ErrConnectionClosed, nats.ErrBadSubscription} {
		if !shouldRebind(err) {
			t.Fatalf("shouldRebind(%v) = false, want true", err)
		}
	}
	if shouldRebind(nats.ErrTimeout) {
		t.Fatal("fetch timeout should not recreate a subscription")
	}
}

func TestRebindRetriesUntilSubscriptionIsAvailable(t *testing.T) {
	consumer := &Consumer{}
	var attempts int
	want := &fakeDeliveryConsumer{}
	consumer.bind = func(context.Context) (deliveryConsumer, error) {
		attempts++
		if attempts < 2 {
			return nil, errors.New("nats reconnecting")
		}
		return want, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got := consumer.rebind(ctx, Config{})
	if got != want || attempts != 2 {
		t.Fatalf("rebind() = %v after %d attempts, want fake consumer after 2 attempts", got, attempts)
	}
}

func TestStartReconnectsClientAfterPersistentBadSubscription(t *testing.T) {
	consumer := testConsumer(t, datasetRowsHandlerFunc(func(context.Context, *eventpb.EventMessage, *storagepb.DatasetRowsUpserted) error {
		return nil
	}))
	consumer.config, _ = consumer.config.withDefaults()
	consumer.config.ErrorReporter = jetstream.ErrorReporterFunc(func(error) {})
	consumer.bind = func(context.Context) (deliveryConsumer, error) {
		return badSubscriptionDeliveryConsumer{}, nil
	}
	var reconnects atomic.Int32
	consumer.reconnect = func(context.Context) error {
		reconnects.Add(1)
		return nil
	}
	stop, err := consumer.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for reconnects.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if reconnects.Load() == 0 {
		t.Fatal("persistent invalid subscription did not trigger a client reconnect")
	}
}

type fakeDeliveryConsumer struct{}

func (*fakeDeliveryConsumer) Fetch(context.Context, int) ([]*jetstream.Delivery, error) {
	return nil, nats.ErrTimeout
}
func (*fakeDeliveryConsumer) Close() error { return nil }

type badSubscriptionDeliveryConsumer struct{}

func (badSubscriptionDeliveryConsumer) Fetch(context.Context, int) ([]*jetstream.Delivery, error) {
	return nil, nats.ErrBadSubscription
}
func (badSubscriptionDeliveryConsumer) Close() error { return nil }

func TestApplyDeliveryRejectsMalformedAndMismatchedEvents(t *testing.T) {
	consumer := testConsumer(t, datasetRowsHandlerFunc(func(context.Context, *eventpb.EventMessage, *storagepb.DatasetRowsUpserted) error {
		return nil
	}))
	for _, delivery := range []*jetstream.Delivery{
		{Subject: "malformed", RawData: []byte("not-protobuf"), ContentType: events.ContentType, RawMessageID: "malformed"},
		{DecodeError: errors.New("decode failed")},
		{RawData: []byte("legacy"), ContentType: "application/x-protobuf"},
	} {
		if err := consumer.applyDelivery(context.Background(), delivery); !IsPermanent(err) {
			t.Fatalf("error = %v, want permanent", err)
		}
	}
	encoded, raw := validDatasetDelivery(t)
	if err := consumer.applyDelivery(context.Background(), &jetstream.Delivery{Subject: encoded.Subject, RawData: raw, RawMessageID: "wrong-id", ContentType: events.ContentType}); !IsPermanent(err) {
		t.Fatalf("event id mismatch = %v, want permanent", err)
	}
}

func TestApplyDeliveryPassesGovernedEventToHandler(t *testing.T) {
	var handled bool
	consumer := testConsumer(t, datasetRowsHandlerFunc(func(_ context.Context, message *eventpb.EventMessage, payload *storagepb.DatasetRowsUpserted) error {
		handled = message.GetSpaceId() == "foo" && payload.GetDatasetId() == "bar"
		return nil
	}))
	encoded, raw := validDatasetDelivery(t)
	if err := consumer.applyDelivery(context.Background(), &jetstream.Delivery{Subject: encoded.Subject, RawData: raw, RawMessageID: encoded.Message.GetEventId(), ContentType: events.ContentType}); err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("typed handler was not called")
	}
}

func testConsumer(t *testing.T, handler DatasetRowsHandler) *Consumer {
	t.Helper()
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return &Consumer{handler: handler, registry: registry, config: Config{}}
}

func validDatasetDelivery(t *testing.T) (events.EncodedEvent, []byte) {
	t.Helper()
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := registry.Encode(events.DatasetRowsUpserted, &storagepb.DatasetRowsUpserted{
		SpaceId: "foo", DatasetId: "bar",
		Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{SpaceId: "foo", DatasetId: "bar", Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: "record-1", Version: "v1"}}}}},
	}, events.PublishOptions{EventID: "storage-test-1", OccurredAt: time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC), SpaceID: "foo", SubjectID: "bar"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(encoded.Message)
	if err != nil {
		t.Fatal(err)
	}
	return encoded, raw
}
