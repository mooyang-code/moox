package eventconsumer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
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
	if config.Consumer != "storage_view" || config.AckWaitMS != 120000 || config.FetchBatch != 8 || config.MaxWorkers != 4 || config.MaxRetryAttempts != 10 || config.Ordering != "subject" {
		t.Fatalf("config = %+v", config)
	}
}

func TestConfigRejectsNegativeRetryAttempts(t *testing.T) {
	if _, err := (Config{MaxRetryAttempts: -1}).withDefaults(); err == nil {
		t.Fatal("negative MaxRetryAttempts was accepted")
	}
}

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
