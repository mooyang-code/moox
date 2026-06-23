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

func TestProducerBusPublishesRecordRowsChangedEvent(t *testing.T) {
	ctx := context.Background()
	producer := &recordingProducer{}
	bus := infraeventbus.NewProducerBus(producer, "moox.storage")

	err := bus.PublishRecordRowsChanged(ctx, &pb.RecordRowsChangedEvent{
		EventId:   "evt-1",
		EventTime: "2026-06-15T00:00:00+08:00",
		Keys:      []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "symbols", RecordId: "APT-USDT"}},
	})

	require.NoError(t, err)
	require.Equal(t, "moox.storage.record.rows_changed.v1", producer.message.Subject)
	require.Equal(t, "evt-1", producer.message.ID)
	require.Contains(t, string(producer.message.Data), "APT-USDT")
}

func TestRowsChangedSubjectsUseUnifiedPrefix(t *testing.T) {
	require.Equal(t, "moox.storage.time_series.rows_changed.v1", infraeventbus.TimeSeriesRowsChangedSubject("moox.storage"))
	require.Equal(t, "moox.storage.record.rows_changed.v1", infraeventbus.RecordRowsChangedSubject("moox.storage"))
	require.Equal(t, "moox.storage.>", infraeventbus.SubjectPrefixWildcard("moox.storage"))
	require.Equal(t, infraeventbus.DefaultRecordRowsChangedSubject, infraeventbus.RecordRowsChangedSubject(""))
}

func TestProducerBusClosesProducer(t *testing.T) {
	producer := &recordingProducer{}
	bus := infraeventbus.NewProducerBus(producer, "")

	require.NoError(t, bus.Close())
	require.True(t, producer.closed)
}

func TestSubscriberBusConsumesTimeSeriesRowsChangedEventFromTransport(t *testing.T) {
	ctx := context.Background()
	pubsub := &recordingPubSub{}
	bus := infraeventbus.NewSubscriberBus(pubsub, "")

	var got *pb.TimeSeriesRowsChangedEvent
	sub, err := bus.SubscribeTimeSeriesRowsChanged(ctx, func(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error {
		_ = ctx
		got = event
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, sub)

	handler := pubsub.handler(infraeventbus.DefaultTimeSeriesRowsChangedSubject)
	require.NotNil(t, handler)
	err = handler(ctx, &transport.Message{
		Subject: infraeventbus.DefaultTimeSeriesRowsChangedSubject,
		Data:    []byte(`{"event_id":"evt-1","keys":[{"space_id":"crypto","dataset_id":"kline","subject_id":"APT-USDT","freq":"1m"}]}`),
		ID:      "evt-1",
	})
	require.NoError(t, err)
	require.Equal(t, "evt-1", got.GetEventId())
	require.Equal(t, "APT-USDT", got.GetKeys()[0].GetSubjectId())
}

func TestSubscriberBusSubscribesToDistinctRowsChangedSubjects(t *testing.T) {
	ctx := context.Background()
	pubsub := &recordingPubSub{}
	bus := infraeventbus.NewSubscriberBus(pubsub, "")

	timeSeriesSub, err := bus.SubscribeTimeSeriesRowsChanged(ctx, func(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error {
		return nil
	})
	require.NoError(t, err)
	recordSub, err := bus.SubscribeRecordRowsChanged(ctx, func(ctx context.Context, event *pb.RecordRowsChangedEvent) error {
		return nil
	})
	require.NoError(t, err)
	defer timeSeriesSub.Close()
	defer recordSub.Close()

	require.ElementsMatch(t, []string{
		infraeventbus.DefaultTimeSeriesRowsChangedSubject,
		infraeventbus.DefaultRecordRowsChangedSubject,
	}, pubsub.subjects)
	require.NotEqual(t,
		infraeventbus.DefaultTimeSeriesRowsChangedSubject,
		infraeventbus.DefaultRecordRowsChangedSubject,
	)
}

func TestSubscriberBusPropagatesTimeSeriesHandlerError(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("derive failed")
	pubsub := &recordingPubSub{}
	bus := infraeventbus.NewSubscriberBus(pubsub, "")

	_, err := bus.SubscribeTimeSeriesRowsChanged(ctx, func(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error {
		return wantErr
	})
	require.NoError(t, err)

	handler := pubsub.handler(infraeventbus.DefaultTimeSeriesRowsChangedSubject)
	require.NotNil(t, handler)
	err = handler(ctx, &transport.Message{
		Subject: infraeventbus.DefaultTimeSeriesRowsChangedSubject,
		Data:    []byte(`{"event_id":"evt-1"}`),
		ID:      "evt-1",
	})
	require.ErrorIs(t, err, wantErr)
}

func TestSubscriberBusPropagatesRecordHandlerError(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("index failed")
	pubsub := &recordingPubSub{}
	bus := infraeventbus.NewSubscriberBus(pubsub, "")

	_, err := bus.SubscribeRecordRowsChanged(ctx, func(ctx context.Context, event *pb.RecordRowsChangedEvent) error {
		return wantErr
	})
	require.NoError(t, err)

	handler := pubsub.handler(infraeventbus.DefaultRecordRowsChangedSubject)
	require.NotNil(t, handler)
	err = handler(ctx, &transport.Message{
		Subject: infraeventbus.DefaultRecordRowsChangedSubject,
		Data:    []byte(`{"event_id":"evt-1"}`),
		ID:      "evt-1",
	})
	require.ErrorIs(t, err, wantErr)
}

func TestSubscriberBusReturnsTransportSubscribeError(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("durable consumer conflict")
	pubsub := &recordingPubSub{subscribeErr: wantErr}
	bus := infraeventbus.NewSubscriberBus(pubsub, "")

	sub, err := bus.SubscribeRecordRowsChanged(ctx, func(ctx context.Context, event *pb.RecordRowsChangedEvent) error {
		return nil
	})

	require.ErrorIs(t, err, wantErr)
	require.Nil(t, sub)
}

func TestSubscriberBusClosesTransportSubscription(t *testing.T) {
	ctx := context.Background()
	pubsub := &recordingPubSub{subscription: &recordingSubscription{}}
	bus := infraeventbus.NewSubscriberBus(pubsub, "")

	sub, err := bus.SubscribeRecordRowsChanged(ctx, func(ctx context.Context, event *pb.RecordRowsChangedEvent) error {
		return nil
	})
	require.NoError(t, err)

	require.NoError(t, sub.Close())
	require.True(t, pubsub.subscription.closed)
}

// recordingProducer 是事件发布测试使用的记录型生产者。
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

// recordingPubSub 是事件总线适配测试使用的内存传输。
type recordingPubSub struct {
	recordingProducer
	subjects      []string
	handlers      map[string]transport.MessageHandler
	subscribeErr  error
	subscription  *recordingSubscription
	subscriptions map[string]*recordingSubscription
}

func (p *recordingPubSub) Subscribe(ctx context.Context, subject string, handler transport.MessageHandler) (transport.Subscription, error) {
	_ = ctx
	if p.subscribeErr != nil {
		return nil, p.subscribeErr
	}
	p.subjects = append(p.subjects, subject)
	if p.handlers == nil {
		p.handlers = make(map[string]transport.MessageHandler)
	}
	p.handlers[subject] = handler
	if p.subscriptions == nil {
		p.subscriptions = make(map[string]*recordingSubscription)
	}
	subscription := p.subscription
	if subscription == nil {
		subscription = &recordingSubscription{}
	}
	p.subscriptions[subject] = subscription
	p.subscription = subscription
	return subscription, nil
}

func (p *recordingPubSub) handler(subject string) transport.MessageHandler {
	if p.handlers == nil {
		return nil
	}
	return p.handlers[subject]
}

// recordingSubscription 是事件订阅测试使用的记录型订阅句柄。
type recordingSubscription struct {
	closed bool
}

func (s *recordingSubscription) Close() error {
	s.closed = true
	return nil
}
