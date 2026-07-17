package eventbus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	DefaultSubjectPrefix                = "moox.storage"
	DefaultTimeSeriesRowsUpdatedSubject = "moox.storage.time_series.rows_updated.v1"
	DefaultRecordRowsUpdatedSubject     = "moox.storage.record.rows_updated.v1"
	defaultTimeSeriesRowsUpdatedSuffix  = "time_series.rows_updated.v1"
	defaultRecordRowsUpdatedSuffix      = "record.rows_updated.v1"
	defaultStorageStream                = "MOOX_STORAGE"
	defaultMaxInFlight                  = 128
	defaultMaxAckPending                = 128
	defaultAckWait                      = 2 * time.Minute
	defaultNakDelay                     = time.Second
	defaultActionTimeout                = 5 * time.Second
)

// SubscriberOptions is the transport contract for predeclared Storage consumers.
type SubscriberOptions struct {
	StreamName    string
	AckWait       time.Duration
	MaxDeliver    int
	MaxInFlight   int
	MaxAckPending int
	NakDelay      time.Duration
	ActionTimeout time.Duration
	Metrics       *observability.ViewMetrics
}

func normalizeSubscriberOptions(opts SubscriberOptions) (SubscriberOptions, error) {
	if strings.TrimSpace(opts.StreamName) == "" {
		opts.StreamName = defaultStorageStream
	}
	if opts.AckWait == 0 {
		opts.AckWait = defaultAckWait
	}
	if opts.MaxDeliver == 0 {
		opts.MaxDeliver = -1
	}
	if opts.MaxInFlight == 0 {
		opts.MaxInFlight = defaultMaxInFlight
	}
	if opts.MaxAckPending == 0 {
		opts.MaxAckPending = defaultMaxAckPending
	}
	if opts.NakDelay == 0 {
		opts.NakDelay = defaultNakDelay
	}
	if opts.ActionTimeout == 0 {
		opts.ActionTimeout = defaultActionTimeout
	}
	if opts.Metrics == nil {
		opts.Metrics = observability.DefaultViewMetrics
	}
	if opts.AckWait < 3*time.Second {
		return SubscriberOptions{}, errors.New("storage subscriber ack wait must be at least 3s")
	}
	if opts.MaxInFlight < 1 || opts.MaxAckPending < 1 || opts.MaxInFlight > opts.MaxAckPending {
		return SubscriberOptions{}, errors.New("storage subscriber requires 1 <= max in flight <= max ack pending")
	}
	if opts.MaxDeliver != -1 && opts.MaxDeliver < 1 {
		return SubscriberOptions{}, errors.New("storage subscriber max deliver must be -1 or at least 1")
	}
	if opts.NakDelay < 0 || opts.ActionTimeout <= 0 {
		return SubscriberOptions{}, errors.New("storage subscriber action durations are invalid")
	}
	return opts, nil
}

type deliveryControl interface {
	Ack(context.Context) error
	Nak(context.Context, time.Duration) error
	InProgress(context.Context) error
}

func processDelivery(ctx context.Context, delivery deliveryControl, opts SubscriberOptions, handler func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if delivery == nil || handler == nil {
		return errors.New("storage delivery and handler are required")
	}
	if opts.AckWait <= 0 {
		opts.AckWait = defaultAckWait
	}
	if opts.NakDelay <= 0 {
		opts.NakDelay = defaultNakDelay
	}
	if opts.ActionTimeout <= 0 {
		opts.ActionTimeout = defaultActionTimeout
	}
	if opts.Metrics == nil {
		opts.Metrics = observability.DefaultViewMetrics
	}
	heartbeatEvery := opts.AckWait / 3
	if heartbeatEvery <= 0 {
		heartbeatEvery = time.Second
	}

	handlerResult := make(chan error, 1)
	go func() { handlerResult <- handler(ctx) }()
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()

	var handlerErr error
	for {
		select {
		case handlerErr = <-handlerResult:
			goto terminal
		case <-ctx.Done():
			handlerErr = errors.Join(ctx.Err(), <-handlerResult)
			goto terminal
		case <-ticker.C:
			actionCtx, cancel := context.WithTimeout(context.Background(), opts.ActionTimeout)
			if err := delivery.InProgress(actionCtx); err != nil {
				opts.Metrics.ObserveDelivery("in_progress", "error")
				log.ErrorContextf(context.Background(), "[StorageEventBus] delivery heartbeat failed: %v", err)
			} else {
				opts.Metrics.ObserveDelivery("in_progress", "success")
			}
			cancel()
		}
	}

terminal:
	actionCtx, cancel := context.WithTimeout(context.Background(), opts.ActionTimeout)
	defer cancel()
	if handlerErr != nil {
		nakErr := delivery.Nak(actionCtx, opts.NakDelay)
		opts.Metrics.ObserveDelivery("nak", deriveDeliveryResult(nakErr))
		return errors.Join(handlerErr, nakErr)
	}
	ackErr := delivery.Ack(actionCtx)
	opts.Metrics.ObserveDelivery("ack", deriveDeliveryResult(ackErr))
	return ackErr
}

func deriveDeliveryResult(err error) string {
	if err == nil {
		return "success"
	}
	return "error"
}

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
	timeSeriesHandlers map[uint64]*subscriberTimeSeriesHandler
	recordHandlers     map[uint64]*subscriberRecordHandler
	timeSeriesConsumer *jetstream.PullConsumer
	recordConsumer     *jetstream.PullConsumer
	timeSeriesCancel   context.CancelFunc
	recordCancel       context.CancelFunc
	timeSeriesStopping bool
	recordStopping     bool
	subscribeClosed    bool
	opts               SubscriberOptions
	timeSeriesFetchWG  sync.WaitGroup
	recordFetchWG      sync.WaitGroup
	timeSeriesHandleWG sync.WaitGroup
	recordHandleWG     sync.WaitGroup
}

func NewSubscriberBus(client *jetstream.Client, prefix string, opts SubscriberOptions) (*SubscriberBus, error) {
	normalized, err := normalizeSubscriberOptions(opts)
	if err != nil {
		return nil, err
	}
	return &SubscriberBus{
		ProducerBus:        NewProducerBus(client, prefix),
		client:             client,
		timeSeriesHandlers: make(map[uint64]*subscriberTimeSeriesHandler),
		recordHandlers:     make(map[uint64]*subscriberRecordHandler),
		opts:               normalized,
	}, nil
}

func (b *SubscriberBus) consumerRef(timeSeries bool) jetstream.ConsumerRef {
	durable := "storage_view_builder_record_rows_updated_v1"
	subject := b.recordSubject
	if timeSeries {
		durable = "storage_view_builder_time_series_rows_updated_v1"
		subject = b.timeSeriesSubject
	}
	return jetstream.ConsumerRef{
		Stream: b.opts.StreamName, Durable: durable, FilterSubject: subject,
		AckWait: b.opts.AckWait, MaxDeliver: b.opts.MaxDeliver, MaxAckPending: b.opts.MaxAckPending,
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
	if b.timeSeriesStopping {
		return nil, errors.New("storage time-series subscription is stopping")
	}
	if b.timeSeriesConsumer == nil {
		consumer, err := b.client.BindPullConsumer(ctx, b.consumerRef(true))
		if err != nil {
			return nil, err
		}
		b.timeSeriesConsumer = consumer
		b.startLoop(consumer, true)
	}
	b.nextID++
	id := b.nextID
	entryCtx, entryCancel := context.WithCancel(context.Background())
	entry := &subscriberTimeSeriesHandler{handler: handler, lifecycle: newSubscriberHandlerLifecycle(), ctx: entryCtx, cancel: entryCancel}
	b.timeSeriesHandlers[id] = entry
	return &subscriberBusSubscription{close: func() error { return b.closeTimeSeries(id, entry) }}, nil
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
	if b.recordStopping {
		return nil, errors.New("storage record subscription is stopping")
	}
	if b.recordConsumer == nil {
		consumer, err := b.client.BindPullConsumer(ctx, b.consumerRef(false))
		if err != nil {
			return nil, err
		}
		b.recordConsumer = consumer
		b.startLoop(consumer, false)
	}
	b.nextID++
	id := b.nextID
	entryCtx, entryCancel := context.WithCancel(context.Background())
	entry := &subscriberRecordHandler{handler: handler, lifecycle: newSubscriberHandlerLifecycle(), ctx: entryCtx, cancel: entryCancel}
	b.recordHandlers[id] = entry
	return &subscriberBusSubscription{close: func() error { return b.closeRecord(id, entry) }}, nil
}

func (b *SubscriberBus) startLoop(consumer *jetstream.PullConsumer, timeSeries bool) {
	ctx, cancel := context.WithCancel(context.Background())
	fetchWG := &b.recordFetchWG
	handlerWG := &b.recordHandleWG
	if timeSeries {
		b.timeSeriesCancel = cancel
		fetchWG = &b.timeSeriesFetchWG
		handlerWG = &b.timeSeriesHandleWG
	} else {
		b.recordCancel = cancel
	}
	fetchWG.Add(1)
	go func() {
		defer fetchWG.Done()
		semaphore := make(chan struct{}, b.opts.MaxInFlight)
		for {
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			deliveries, err := consumer.Fetch(ctx, 1)
			if err != nil && len(deliveries) == 0 {
				<-semaphore
				if errors.Is(err, context.Canceled) || errors.Is(err, jetstream.ErrClosed) {
					return
				}
				time.Sleep(100 * time.Millisecond)
				continue
			}
			if len(deliveries) == 0 {
				<-semaphore
				continue
			}
			delivery := deliveries[0]
			if delivery != nil && delivery.DeliveryCount > 1 {
				b.opts.Metrics.IncRedelivery()
			}
			handlerWG.Add(1)
			go func(delivery *jetstream.Delivery) {
				defer handlerWG.Done()
				defer func() { <-semaphore }()
				err := processDelivery(ctx, delivery, b.opts, func(handlerCtx context.Context) error {
					return b.dispatch(handlerCtx, delivery, timeSeries)
				})
				if err != nil {
					messageID := ""
					if delivery != nil && delivery.Message != nil {
						messageID = delivery.Message.GetMessageId()
					}
					deliveryCount := uint64(0)
					if delivery != nil {
						deliveryCount = delivery.DeliveryCount
					}
					log.ErrorContextf(context.Background(), "[StorageEventBus] delivery failed message_id=%s delivery_count=%d: %v", messageID, deliveryCount, err)
				}
			}(delivery)
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
		handlers := make([]*subscriberTimeSeriesHandler, 0, len(b.timeSeriesHandlers))
		for _, entry := range b.timeSeriesHandlers {
			if entry.lifecycle.acquire() {
				handlers = append(handlers, entry)
			}
		}
		b.mu.Unlock()
		if len(handlers) == 0 {
			return errors.New("storage event delivery has no time-series handlers")
		}
		if err != nil {
			for _, entry := range handlers {
				entry.lifecycle.release()
			}
			return err
		}
		for _, entry := range handlers {
			err = errors.Join(err, entry.call(ctx, &event))
		}
	} else {
		var event pb.RecordRowsUpdated
		err = proto.Unmarshal(delivery.Message.GetPayload(), &event)
		handlers := make([]*subscriberRecordHandler, 0, len(b.recordHandlers))
		for _, entry := range b.recordHandlers {
			if entry.lifecycle.acquire() {
				handlers = append(handlers, entry)
			}
		}
		b.mu.Unlock()
		if len(handlers) == 0 {
			return errors.New("storage event delivery has no record handlers")
		}
		if err != nil {
			for _, entry := range handlers {
				entry.lifecycle.release()
			}
			return err
		}
		for _, entry := range handlers {
			err = errors.Join(err, entry.call(ctx, &event))
		}
	}
	return err
}

func (b *SubscriberBus) closeTimeSeries(id uint64, entry *subscriberTimeSeriesHandler) error {
	b.mu.Lock()
	delete(b.timeSeriesHandlers, id)
	entry.lifecycle.beginClose()
	entry.cancel()
	if len(b.timeSeriesHandlers) != 0 || b.timeSeriesCancel == nil {
		b.mu.Unlock()
		entry.lifecycle.wait()
		return nil
	}
	cancel, consumer := b.timeSeriesCancel, b.timeSeriesConsumer
	b.timeSeriesStopping = true
	b.timeSeriesCancel, b.timeSeriesConsumer = nil, nil
	b.mu.Unlock()
	entry.lifecycle.wait()
	cancel()
	b.timeSeriesFetchWG.Wait()
	b.timeSeriesHandleWG.Wait()
	var err error
	if consumer != nil {
		err = consumer.Close()
	}
	b.mu.Lock()
	b.timeSeriesStopping = false
	b.mu.Unlock()
	return err
}
func (b *SubscriberBus) closeRecord(id uint64, entry *subscriberRecordHandler) error {
	b.mu.Lock()
	delete(b.recordHandlers, id)
	entry.lifecycle.beginClose()
	entry.cancel()
	if len(b.recordHandlers) != 0 || b.recordCancel == nil {
		b.mu.Unlock()
		entry.lifecycle.wait()
		return nil
	}
	cancel, consumer := b.recordCancel, b.recordConsumer
	b.recordStopping = true
	b.recordCancel, b.recordConsumer = nil, nil
	b.mu.Unlock()
	entry.lifecycle.wait()
	cancel()
	b.recordFetchWG.Wait()
	b.recordHandleWG.Wait()
	var err error
	if consumer != nil {
		err = consumer.Close()
	}
	b.mu.Lock()
	b.recordStopping = false
	b.mu.Unlock()
	return err
}

type subscriberTimeSeriesHandler struct {
	handler   coreeventbus.TimeSeriesRowsUpdatedHandler
	lifecycle *subscriberHandlerLifecycle
	ctx       context.Context
	cancel    context.CancelFunc
}

func (h *subscriberTimeSeriesHandler) call(ctx context.Context, event *pb.TimeSeriesRowsUpdated) error {
	defer h.lifecycle.release()
	handlerCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(h.ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	return h.handler(handlerCtx, event)
}

type subscriberRecordHandler struct {
	handler   coreeventbus.RecordRowsUpdatedHandler
	lifecycle *subscriberHandlerLifecycle
	ctx       context.Context
	cancel    context.CancelFunc
}

func (h *subscriberRecordHandler) call(ctx context.Context, event *pb.RecordRowsUpdated) error {
	defer h.lifecycle.release()
	handlerCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(h.ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	return h.handler(handlerCtx, event)
}

type subscriberHandlerLifecycle struct {
	mu       sync.Mutex
	cond     *sync.Cond
	closing  bool
	inFlight int
}

func newSubscriberHandlerLifecycle() *subscriberHandlerLifecycle {
	lifecycle := &subscriberHandlerLifecycle{}
	lifecycle.cond = sync.NewCond(&lifecycle.mu)
	return lifecycle
}

func (l *subscriberHandlerLifecycle) acquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closing {
		return false
	}
	l.inFlight++
	return true
}

func (l *subscriberHandlerLifecycle) release() {
	l.mu.Lock()
	l.inFlight--
	if l.inFlight == 0 {
		l.cond.Broadcast()
	}
	l.mu.Unlock()
}

func (l *subscriberHandlerLifecycle) beginClose() {
	l.mu.Lock()
	l.closing = true
	l.mu.Unlock()
}

func (l *subscriberHandlerLifecycle) wait() {
	l.mu.Lock()
	for l.inFlight > 0 {
		l.cond.Wait()
	}
	l.mu.Unlock()
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
	b.timeSeriesFetchWG.Wait()
	b.recordFetchWG.Wait()
	b.timeSeriesHandleWG.Wait()
	b.recordHandleWG.Wait()
	if ts != nil {
		_ = ts.Close()
	}
	if rec != nil {
		_ = rec.Close()
	}
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
