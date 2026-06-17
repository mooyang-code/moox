package eventbus_test

import (
	"context"
	"testing"

	infraeventbus "github.com/mooyang-code/moox/modules/storage/internal/infra/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestProducerBusPublishesRowsChangedEvent(t *testing.T) {
	ctx := context.Background()
	producer := &recordingProducer{}
	bus := infraeventbus.NewProducerBus(producer, "storage.rows.changed")

	err := bus.PublishRowsChanged(ctx, &pb.DataRowsChangedEvent{
		EventId:   "evt-1",
		Scope:     &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"},
		EventTime: "2026-06-15T00:00:00+08:00",
	})

	require.NoError(t, err)
	require.Equal(t, "storage.rows.changed", producer.message.Subject)
	require.Equal(t, "evt-1", producer.message.ID)
	require.Contains(t, string(producer.message.Data), "APT-USDT")
}

type recordingProducer struct {
	message *transport.Message
}

func (p *recordingProducer) Connect(context.Context) error {
	return nil
}

func (p *recordingProducer) Close() error {
	return nil
}

func (p *recordingProducer) Send(ctx context.Context, msg *transport.Message) error {
	_ = ctx
	p.message = msg
	return nil
}

func (p *recordingProducer) IsConnected() bool {
	return true
}

func (p *recordingProducer) Options() transport.ProducerOptions {
	return transport.ProducerOptions{}
}
