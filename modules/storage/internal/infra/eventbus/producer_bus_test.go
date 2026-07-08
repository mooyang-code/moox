package eventbus

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestProducerBusPublishesRowsUpdatedSubjectsAndMessageID(t *testing.T) {
	producer := &fakeTransport{}
	bus := NewProducerBus(producer, "moox.storage.test")

	err := bus.PublishTimeSeriesRowsUpdated(context.Background(), &pb.TimeSeriesRowsUpdated{
		MessageId: "msg-1",
		WrittenAt: "2026-07-08T10:00:00Z",
		SpaceId:   "crypto",
		DatasetId: "kline",
		Rows: []*pb.TimeSeriesRow{{
			Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT"},
		}},
	})
	if err != nil {
		t.Fatalf("PublishTimeSeriesRowsUpdated: %v", err)
	}
	if len(producer.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(producer.sent))
	}
	msg := producer.sent[0]
	if msg.Subject != "moox.storage.test.time_series.rows_updated.v1" {
		t.Fatalf("subject = %q, want rows_updated subject", msg.Subject)
	}
	if msg.ID != "msg-1" {
		t.Fatalf("message ID = %q, want message_id", msg.ID)
	}
}

func TestSubscriberBusDeliversRowsUpdatedPayload(t *testing.T) {
	pubsub := &fakeTransport{}
	bus := NewSubscriberBus(pubsub, "moox.storage.test")
	received := make(chan *pb.TimeSeriesRowsUpdated, 1)

	if _, err := bus.SubscribeTimeSeriesRowsUpdated(context.Background(), func(_ context.Context, event *pb.TimeSeriesRowsUpdated) error {
		received <- event
		return nil
	}); err != nil {
		t.Fatalf("SubscribeTimeSeriesRowsUpdated: %v", err)
	}
	if err := bus.PublishTimeSeriesRowsUpdated(context.Background(), &pb.TimeSeriesRowsUpdated{
		MessageId: "msg-2",
		SpaceId:   "crypto",
		DatasetId: "kline",
		Rows: []*pb.TimeSeriesRow{{
			Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "ETH-USDT"},
		}},
	}); err != nil {
		t.Fatalf("PublishTimeSeriesRowsUpdated: %v", err)
	}
	if err := pubsub.handlers["moox.storage.test.time_series.rows_updated.v1"](context.Background(), pubsub.sent[0]); err != nil {
		t.Fatalf("handle message: %v", err)
	}

	got := <-received
	if got.GetMessageId() != "msg-2" || len(got.GetRows()) != 1 || got.GetRows()[0].GetKey().GetSubjectId() != "ETH-USDT" {
		t.Fatalf("received event = %+v, want rows-updated payload", got)
	}
}

type fakeTransport struct {
	sent     []*transport.Message
	handlers map[string]transport.MessageHandler
}

func (f *fakeTransport) Connect(context.Context) error      { return nil }
func (f *fakeTransport) Close() error                       { return nil }
func (f *fakeTransport) IsConnected() bool                  { return true }
func (f *fakeTransport) Options() transport.ProducerOptions { return transport.ProducerOptions{} }

func (f *fakeTransport) Send(_ context.Context, msg *transport.Message) error {
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeTransport) Subscribe(_ context.Context, subject string, handler transport.MessageHandler) (transport.Subscription, error) {
	if f.handlers == nil {
		f.handlers = make(map[string]transport.MessageHandler)
	}
	f.handlers[subject] = handler
	return fakeSubscription{}, nil
}

type fakeSubscription struct{}

func (fakeSubscription) Close() error { return nil }
