package eventbus_test

import (
	"context"
	"errors"
	"testing"

	infraeventbus "github.com/mooyang-code/moox/modules/storage/internal/infra/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestProducerBusPublishesRowsChangedEvent(t *testing.T) {
	ctx := context.Background()
	producer := &recordingProducer{}
	bus := infraeventbus.NewProducerBus(producer, infraeventbus.RowsChangedSubject("moox.storage"))

	err := bus.PublishRowsChanged(ctx, &pb.DataRowsChangedEvent{
		EventId:   "evt-1",
		Scope:     &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"},
		EventTime: "2026-06-15T00:00:00+08:00",
	})

	require.NoError(t, err)
	require.Equal(t, "moox.storage.fact.rows_changed.v1", producer.message.Subject)
	require.Equal(t, "evt-1", producer.message.ID)
	require.Contains(t, string(producer.message.Data), "APT-USDT")
}

func TestRowsChangedSubjectUsesUnifiedPrefix(t *testing.T) {
	require.Equal(t, "moox.storage.fact.rows_changed.v1", infraeventbus.RowsChangedSubject("moox.storage"))
	require.Equal(t, "moox.storage.>", infraeventbus.SubjectPrefixWildcard("moox.storage"))
	require.Equal(t, "moox.storage.fact.rows_changed.v1", infraeventbus.RowsChangedSubject(""))
}

func TestProducerBusClosesProducer(t *testing.T) {
	producer := &recordingProducer{}
	bus := infraeventbus.NewProducerBus(producer, infraeventbus.DefaultRowsChangedSubject)

	require.NoError(t, bus.Close())
	require.True(t, producer.closed)
}

func TestSubscriberBusConsumesRowsChangedEventFromTransport(t *testing.T) {
	ctx := context.Background()
	pubsub := &recordingPubSub{}
	bus := infraeventbus.NewSubscriberBus(pubsub, infraeventbus.DefaultRowsChangedSubject)

	var got *pb.DataRowsChangedEvent
	sub, err := bus.SubscribeRowsChanged(ctx, func(ctx context.Context, event *pb.DataRowsChangedEvent) error {
		_ = ctx
		got = event
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, sub)

	require.Equal(t, infraeventbus.DefaultRowsChangedSubject, pubsub.subject)
	require.NotNil(t, pubsub.handler)
	err = pubsub.handler(ctx, &transport.Message{
		Subject: infraeventbus.DefaultRowsChangedSubject,
		Data:    []byte(`{"event_id":"evt-1","scope":{"space_id":"crypto","dataset_id":"kline","subject_id":"APT-USDT"}}`),
		ID:      "evt-1",
	})
	require.NoError(t, err)
	require.Equal(t, "evt-1", got.GetEventId())
	require.Equal(t, "APT-USDT", got.GetScope().GetSubjectId())
}

func TestSubscriberBusReturnsTransportSubscribeError(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("durable consumer conflict")
	pubsub := &recordingPubSub{subscribeErr: wantErr}
	bus := infraeventbus.NewSubscriberBus(pubsub, infraeventbus.DefaultRowsChangedSubject)

	sub, err := bus.SubscribeRowsChanged(ctx, func(ctx context.Context, event *pb.DataRowsChangedEvent) error {
		return nil
	})

	require.ErrorIs(t, err, wantErr)
	require.Nil(t, sub)
}

func TestSubscriberBusClosesTransportSubscription(t *testing.T) {
	ctx := context.Background()
	pubsub := &recordingPubSub{subscription: &recordingSubscription{}}
	bus := infraeventbus.NewSubscriberBus(pubsub, infraeventbus.DefaultRowsChangedSubject)

	sub, err := bus.SubscribeRowsChanged(ctx, func(ctx context.Context, event *pb.DataRowsChangedEvent) error {
		return nil
	})
	require.NoError(t, err)

	require.NoError(t, sub.Close())
	require.True(t, pubsub.subscription.closed)
}

type recordingProducer struct {
	message *transport.Message
	closed  bool
}

func (p *recordingProducer) Connect(context.Context) error {
	return nil
}

func (p *recordingProducer) Close() error {
	p.closed = true
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

type recordingPubSub struct {
	recordingProducer
	subject      string
	handler      transport.MessageHandler
	subscribeErr error
	subscription *recordingSubscription
}

func (p *recordingPubSub) Subscribe(ctx context.Context, subject string, handler transport.MessageHandler) (transport.Subscription, error) {
	_ = ctx
	if p.subscribeErr != nil {
		return nil, p.subscribeErr
	}
	p.subject = subject
	p.handler = handler
	if p.subscription == nil {
		p.subscription = &recordingSubscription{}
	}
	return p.subscription, nil
}

type recordingSubscription struct {
	closed bool
}

func (s *recordingSubscription) Close() error {
	s.closed = true
	return nil
}
