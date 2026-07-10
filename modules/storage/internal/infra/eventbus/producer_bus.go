package eventbus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	DefaultSubjectPrefix                = "moox.storage"
	DefaultTimeSeriesRowsUpdatedSubject = "moox.storage.time_series.rows_updated.v1"
	DefaultRecordRowsUpdatedSubject     = "moox.storage.record.rows_updated.v1"
	defaultTimeSeriesRowsUpdatedSuffix  = "time_series.rows_updated.v1"
	defaultRecordRowsUpdatedSuffix      = "record.rows_updated.v1"
)

func TimeSeriesRowsUpdatedSubject(prefix string) string {
	return normalizeSubjectPrefix(prefix) + "." + defaultTimeSeriesRowsUpdatedSuffix
}

func RecordRowsUpdatedSubject(prefix string) string {
	return normalizeSubjectPrefix(prefix) + "." + defaultRecordRowsUpdatedSuffix
}

func SubjectPrefixWildcard(prefix string) string { return normalizeSubjectPrefix(prefix) + ".>" }

func normalizeSubjectPrefix(prefix string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), ".")
	if prefix == "" {
		return DefaultSubjectPrefix
	}
	return prefix
}

// ProducerBus adapts the shared JetStream package to Storage's domain event API.
type ProducerBus struct {
	client            *jetstream.Client
	timeSeriesSubject string
	recordSubject     string
	producer          *messagepb.Producer
}

func NewProducerBus(client *jetstream.Client, prefix string) *ProducerBus {
	return &ProducerBus{
		client:            client,
		timeSeriesSubject: TimeSeriesRowsUpdatedSubject(prefix),
		recordSubject:     RecordRowsUpdatedSubject(prefix),
		producer:          &messagepb.Producer{ServiceName: "moox-storage", InstanceId: "storage"},
	}
}

func (b *ProducerBus) PublishTimeSeriesRowsUpdated(ctx context.Context, event *pb.TimeSeriesRowsUpdated) error {
	if event == nil {
		return errors.New("time-series update is nil")
	}
	return b.publish(ctx, b.timeSeriesSubject, event.GetMessageId(), event.GetSpaceId(), event.GetDatasetId(), event)
}

func (b *ProducerBus) PublishRecordRowsUpdated(ctx context.Context, event *pb.RecordRowsUpdated) error {
	if event == nil {
		return errors.New("record update is nil")
	}
	return b.publish(ctx, b.recordSubject, event.GetMessageId(), event.GetSpaceId(), event.GetDatasetId(), event)
}

func (b *ProducerBus) publish(ctx context.Context, topic, id, spaceID, datasetID string, payload proto.Message) error {
	if b == nil || b.client == nil {
		return errors.New("storage eventbus client is nil")
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal storage update: %w", err)
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("storage update message_id is required")
	}
	now := timestamppb.Now()
	contentType := "application/x-protobuf; message=trpc.moox.storage.RecordRowsUpdated"
	if strings.HasSuffix(topic, defaultTimeSeriesRowsUpdatedSuffix) {
		contentType = "application/x-protobuf; message=trpc.moox.storage.TimeSeriesRowsUpdated"
	}
	msg := &messagepb.MooxMessage{
		ProtocolVersion: jetstream.ProtocolVersion,
		MessageId:       id,
		Topic:           topic,
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer:        b.producer,
		SpaceId:         spaceID,
		OccurredAt:      now,
		PublishedAt:     now,
		ContentType:     contentType,
		Payload:         data,
		Attributes:      map[string]string{"dataset_id": datasetID},
	}
	_, err = b.client.Publish(ctx, msg)
	return err
}

// PublishEnvelope republishes an already persisted deterministic message. It
// is used by the PrimaryStore relay so a retry never changes message_id.
func (b *ProducerBus) PublishEnvelope(ctx context.Context, data []byte) error {
	if b == nil || b.client == nil {
		return errors.New("storage eventbus client is nil")
	}
	msg := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return err
	}
	_, err := b.client.Publish(ctx, msg)
	return err
}

func (b *ProducerBus) PublishEnvelopes(ctx context.Context, data [][]byte) []error {
	results := make([]error, len(data))
	msgs := make([]*messagepb.MooxMessage, len(data))
	for i, raw := range data {
		msg := &messagepb.MooxMessage{}
		if err := proto.Unmarshal(raw, msg); err != nil {
			results[i] = err
		} else {
			msgs[i] = msg
		}
	}
	acks := b.client.PublishBatch(ctx, msgs)
	for i := range results {
		if results[i] == nil {
			results[i] = acks[i].Err
		}
	}
	return results
}

func (b *ProducerBus) Close() error {
	if b == nil || b.client == nil {
		return nil
	}
	return b.client.Close()
}

// SubscriberBus fan-outs decoded shared messages to Storage consumers.
type SubscriberBus struct {
	*ProducerBus
	client             *jetstream.Client
	mu                 sync.Mutex
	nextID             uint64
	timeSeriesHandlers map[uint64]coreeventbus.TimeSeriesRowsUpdatedHandler
	recordHandlers     map[uint64]coreeventbus.RecordRowsUpdatedHandler
	timeSeriesConsumer *jetstream.PullConsumer
	recordConsumer     *jetstream.PullConsumer
	timeSeriesCancel   context.CancelFunc
	recordCancel       context.CancelFunc
	subscribeClosed    bool
	wg                 sync.WaitGroup
}

func NewSubscriberBus(client *jetstream.Client, prefix string) *SubscriberBus {
	return &SubscriberBus{
		ProducerBus:        NewProducerBus(client, prefix),
		client:             client,
		timeSeriesHandlers: make(map[uint64]coreeventbus.TimeSeriesRowsUpdatedHandler),
		recordHandlers:     make(map[uint64]coreeventbus.RecordRowsUpdatedHandler),
	}
}

func (b *SubscriberBus) SubscribeTimeSeriesRowsUpdated(ctx context.Context, handler coreeventbus.TimeSeriesRowsUpdatedHandler) (coreeventbus.Subscription, error) {
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subscribeClosed {
		return nil, context.Canceled
	}
	if b.timeSeriesConsumer == nil {
		consumer, err := b.client.BindPullConsumer(ctx, jetstream.ConsumerRef{Stream: "MOOX_STORAGE", Durable: "storage_view_builder_time_series_rows_updated_v1", FilterSubject: b.timeSeriesSubject, AckWait: 2 * time.Minute, MaxDeliver: -1, MaxAckPending: 128})
		if err != nil {
			return nil, err
		}
		b.timeSeriesConsumer = consumer
		b.startLoop(consumer, true)
	}
	b.nextID++
	id := b.nextID
	b.timeSeriesHandlers[id] = handler
	return &subscriberBusSubscription{close: func() error { return b.closeTimeSeries(id) }}, nil
}

func (b *SubscriberBus) SubscribeRecordRowsUpdated(ctx context.Context, handler coreeventbus.RecordRowsUpdatedHandler) (coreeventbus.Subscription, error) {
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subscribeClosed {
		return nil, context.Canceled
	}
	if b.recordConsumer == nil {
		consumer, err := b.client.BindPullConsumer(ctx, jetstream.ConsumerRef{Stream: "MOOX_STORAGE", Durable: "storage_view_builder_record_rows_updated_v1", FilterSubject: b.recordSubject, AckWait: 2 * time.Minute, MaxDeliver: -1, MaxAckPending: 128})
		if err != nil {
			return nil, err
		}
		b.recordConsumer = consumer
		b.startLoop(consumer, false)
	}
	b.nextID++
	id := b.nextID
	b.recordHandlers[id] = handler
	return &subscriberBusSubscription{close: func() error { return b.closeRecord(id) }}, nil
}

func (b *SubscriberBus) startLoop(consumer *jetstream.PullConsumer, timeSeries bool) {
	ctx, cancel := context.WithCancel(context.Background())
	if timeSeries {
		b.timeSeriesCancel = cancel
	} else {
		b.recordCancel = cancel
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			deliveries, err := consumer.Fetch(ctx, 32)
			if err != nil && len(deliveries) == 0 {
				if errors.Is(err, context.Canceled) || errors.Is(err, jetstream.ErrClosed) {
					return
				}
				time.Sleep(100 * time.Millisecond)
				continue
			}
			for _, delivery := range deliveries {
				handlerErr := b.dispatch(ctx, delivery, timeSeries)
				if handlerErr != nil {
					_ = delivery.Nak(context.Background(), time.Second)
					continue
				}
				_ = delivery.Ack(context.Background())
			}
		}
	}()
}

func (b *SubscriberBus) dispatch(ctx context.Context, delivery *jetstream.Delivery, timeSeries bool) error {
	if delivery == nil || delivery.Message == nil {
		return errors.New("nil storage event delivery")
	}
	var err error
	b.mu.Lock()
	if timeSeries {
		var event pb.TimeSeriesRowsUpdated
		err = proto.Unmarshal(delivery.Message.GetPayload(), &event)
		handlers := make([]coreeventbus.TimeSeriesRowsUpdatedHandler, 0, len(b.timeSeriesHandlers))
		for _, h := range b.timeSeriesHandlers {
			handlers = append(handlers, h)
		}
		b.mu.Unlock()
		if err == nil {
			for _, h := range handlers {
				err = errors.Join(err, h(ctx, &event))
			}
		}
	} else {
		var event pb.RecordRowsUpdated
		err = proto.Unmarshal(delivery.Message.GetPayload(), &event)
		handlers := make([]coreeventbus.RecordRowsUpdatedHandler, 0, len(b.recordHandlers))
		for _, h := range b.recordHandlers {
			handlers = append(handlers, h)
		}
		b.mu.Unlock()
		if err == nil {
			for _, h := range handlers {
				err = errors.Join(err, h(ctx, &event))
			}
		}
	}
	return err
}

func (b *SubscriberBus) closeTimeSeries(id uint64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.timeSeriesHandlers, id)
	return nil
}
func (b *SubscriberBus) closeRecord(id uint64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.recordHandlers, id)
	return nil
}
func (b *SubscriberBus) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.subscribeClosed = true
	timeSeriesCancel, recordCancel := b.timeSeriesCancel, b.recordCancel
	ts, rec := b.timeSeriesConsumer, b.recordConsumer
	b.timeSeriesCancel, b.recordCancel = nil, nil
	b.mu.Unlock()
	if timeSeriesCancel != nil {
		timeSeriesCancel()
	}
	if recordCancel != nil {
		recordCancel()
	}
	if ts != nil {
		_ = ts.Close()
	}
	if rec != nil {
		_ = rec.Close()
	}
	b.wg.Wait()
	return b.ProducerBus.Close()
}

type subscriberBusSubscription struct{ close func() error }

func (s *subscriberBusSubscription) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	return s.close()
}

type noopSubscription struct{}

func (noopSubscription) Close() error { return nil }
