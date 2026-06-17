package eventbus

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
)

const DefaultRowsChangedSubject = "storage.rows.changed"

type ProducerBus struct {
	producer transport.Producer
	subject  string
}

func NewProducerBus(producer transport.Producer, subject string) *ProducerBus {
	if subject == "" {
		subject = DefaultRowsChangedSubject
	}
	return &ProducerBus{producer: producer, subject: subject}
}

func (b *ProducerBus) PublishRowsChanged(ctx context.Context, event *pb.DataRowsChangedEvent) error {
	data, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(event)
	if err != nil {
		return err
	}
	return b.producer.Send(ctx, &transport.Message{
		Subject: b.subject,
		Data:    data,
		ID:      event.GetEventId(),
		Time:    time.Now(),
	})
}
