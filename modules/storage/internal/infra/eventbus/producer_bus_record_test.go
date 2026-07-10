package eventbus

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestProducerBusPublishesCommittedRecordSubjectAndStableID(t *testing.T) {
	producer := &recordProducer{}
	bus := NewProducerBus(producer, "moox.storage")
	event := &pb.RecordRowsCommittedEvent{EventId: "source:7", CommitSeq: 7}
	if err := bus.PublishRecordRowsCommitted(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if producer.message == nil || producer.message.Subject != "moox.storage.record.rows_committed.v1" || producer.message.ID != event.GetEventId() {
		t.Fatalf("message = %+v", producer.message)
	}
}

type recordProducer struct{ message *transport.Message }

func (p *recordProducer) Connect(context.Context) error { return nil }
func (p *recordProducer) Close() error                  { return nil }
func (p *recordProducer) Send(_ context.Context, message *transport.Message) error {
	p.message = message
	return nil
}
func (p *recordProducer) IsConnected() bool                  { return true }
func (p *recordProducer) Options() transport.ProducerOptions { return transport.ProducerOptions{} }
