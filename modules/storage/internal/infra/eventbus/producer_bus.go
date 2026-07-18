package eventbus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	DefaultSubjectPrefix                  = "moox.storage"
	DefaultTimeSeriesRowsCommittedSubject = "moox.storage.rows_committed.time_series.v1.>"
	DefaultRecordRowsCommittedSubject     = "moox.storage.rows_committed.record.v1.>"
	defaultTimeSeriesRowsCommittedBase    = "rows_committed.time_series.v1"
	defaultRecordRowsCommittedBase        = "rows_committed.record.v1"
	defaultTimeSeriesRowsCommittedType    = "moox.storage.time_series.rows_committed.v1"
	defaultRecordRowsCommittedType        = "moox.storage.record.rows_committed.v1"
	defaultStorageStream                  = "MOOX_STORAGE"
	defaultMaxInFlight                    = 1
	defaultMaxAckPending                  = 128
	defaultAckWait                        = 2 * time.Minute
	defaultNakDelay                       = time.Second
	defaultActionTimeout                  = 5 * time.Second
	defaultHandlerDrainTimeout            = 5 * time.Second
)

// SubscriberOptions is the transport contract for predeclared Storage consumers.
type SubscriberOptions struct {
	StreamName          string
	AckWait             time.Duration
	MaxDeliver          int
	MaxInFlight         int
	MaxAckPending       int
	NakDelay            time.Duration
	ActionTimeout       time.Duration
	HandlerDrainTimeout time.Duration
	Metrics             *observability.ViewMetrics
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
	if opts.HandlerDrainTimeout == 0 {
		opts.HandlerDrainTimeout = defaultHandlerDrainTimeout
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
	if opts.NakDelay < 0 || opts.ActionTimeout <= 0 || opts.HandlerDrainTimeout <= 0 {
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
		ctx = trpc.BackgroundContext()
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
	if opts.HandlerDrainTimeout <= 0 {
		opts.HandlerDrainTimeout = defaultHandlerDrainTimeout
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
			select {
			case err := <-handlerResult:
				handlerErr = errors.Join(ctx.Err(), err)
			case <-time.After(opts.HandlerDrainTimeout):
				handlerErr = errors.Join(ctx.Err(), errors.New("storage delivery handler drain timed out"))
			}
			goto terminal
		case <-ticker.C:
			actionCtx, cancel := context.WithTimeout(trpc.CloneContext(ctx), opts.ActionTimeout)
			if err := delivery.InProgress(actionCtx); err != nil {
				opts.Metrics.ObserveDelivery("in_progress", "error")
				log.ErrorContextf(actionCtx, "[StorageEventBus] delivery heartbeat failed: %v", err)
			} else {
				opts.Metrics.ObserveDelivery("in_progress", "success")
			}
			cancel()
		}
	}

terminal:
	actionCtx, cancel := context.WithTimeout(trpc.CloneContext(ctx), opts.ActionTimeout)
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

func TimeSeriesRowsCommittedSubject(prefix string) string {
	return normalizeSubjectPrefix(prefix) + "." + defaultTimeSeriesRowsCommittedBase + ".>"
}

func RecordRowsCommittedSubject(prefix string) string {
	return normalizeSubjectPrefix(prefix) + "." + defaultRecordRowsCommittedBase + ".>"
}

func TimeSeriesRowsCommittedTopic(prefix, shardID string) (string, error) {
	token, err := jetstream.EncodeShardToken(shardID)
	if err != nil {
		return "", err
	}
	return normalizeSubjectPrefix(prefix) + "." + defaultTimeSeriesRowsCommittedBase + "." + token, nil
}

func RecordRowsCommittedTopic(prefix, shardID string) (string, error) {
	token, err := jetstream.EncodeShardToken(shardID)
	if err != nil {
		return "", err
	}
	return normalizeSubjectPrefix(prefix) + "." + defaultRecordRowsCommittedBase + "." + token, nil
}

func SubjectPrefixWildcard(prefix string) string { return normalizeSubjectPrefix(prefix) + ".>" }

func RowsCommittedSubjectWildcard(prefix string) string {
	return normalizeSubjectPrefix(prefix) + ".rows_committed.>"
}

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
	nextSequence      atomic.Uint64
}

func NewProducerBus(client *jetstream.Client, prefix string) *ProducerBus {
	return &ProducerBus{
		client:            client,
		timeSeriesSubject: TimeSeriesRowsCommittedSubject(prefix),
		recordSubject:     RecordRowsCommittedSubject(prefix),
		producer:          &messagepb.Producer{ServiceName: "moox-storage", InstanceId: "storage"},
	}
}

func (b *ProducerBus) Ready() bool {
	return b != nil && b.client != nil && b.client.Ready()
}

func (b *ProducerBus) PublishTimeSeriesRowsCommitted(ctx context.Context, event *pb.TimeSeriesRowsCommitted) error {
	if event == nil {
		return errors.New("time-series committed batch is nil")
	}
	topic, err := TimeSeriesRowsCommittedTopic(b.subjectPrefix(), event.GetShardId())
	if err != nil {
		return err
	}
	return b.publish(ctx, topic, defaultTimeSeriesRowsCommittedType, event.GetSpaceId(), event.GetDatasetId(), event.GetShardId(), event)
}

func (b *ProducerBus) PublishRecordRowsCommitted(ctx context.Context, event *pb.RecordRowsCommitted) error {
	if event == nil {
		return errors.New("record committed batch is nil")
	}
	topic, err := RecordRowsCommittedTopic(b.subjectPrefix(), event.GetShardId())
	if err != nil {
		return err
	}
	return b.publish(ctx, topic, defaultRecordRowsCommittedType, event.GetSpaceId(), event.GetDatasetId(), event.GetShardId(), event)
}

func (b *ProducerBus) subjectPrefix() string {
	if b == nil {
		return DefaultSubjectPrefix
	}
	return strings.TrimSuffix(b.timeSeriesSubject, "."+defaultTimeSeriesRowsCommittedBase+".>")
}

func (b *ProducerBus) publish(ctx context.Context, topic, messageType, spaceID, datasetID, shardID string, payload proto.Message) error {
	if b == nil || b.client == nil {
		return errors.New("storage eventbus client is nil")
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal storage update: %w", err)
	}
	sequence := b.nextSequence.Add(1)
	id := fmt.Sprintf("storage-%d", sequence)
	now := timestamppb.Now()
	msg := &messagepb.MooxMessage{
		ProtocolVersion: jetstream.ProtocolVersion,
		MessageId:       id,
		Topic:           topic,
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer:        b.producer,
		SpaceId:         spaceID,
		Sequence:        sequence,
		OccurredAt:      now,
		PublishedAt:     now,
		ContentType:     "application/x-protobuf; message=trpc.moox.storage." + map[string]string{defaultTimeSeriesRowsCommittedType: "TimeSeriesRowsCommitted", defaultRecordRowsCommittedType: "RecordRowsCommitted"}[messageType],
		MessageType:     messageType,
		Payload:         data,
		Attributes:      map[string]string{"dataset_id": datasetID},
	}
	if err := validateRowsCommittedMessage(msg, shardID); err != nil {
		return err
	}
	_, err = b.client.Publish(ctx, msg)
	return err
}

// PublishMessage republishes an already persisted deterministic message. It
// is used by the PrimaryStore relay so a retry never changes message_id.
func (b *ProducerBus) PublishMessage(ctx context.Context, data []byte) error {
	if b == nil || b.client == nil {
		return errors.New("storage eventbus client is nil")
	}
	msg := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return err
	}
	if err := validateRowsCommittedMessage(msg, ""); err != nil {
		return err
	}
	_, err := b.client.Publish(ctx, msg)
	return err
}

func validateRowsCommittedMessage(msg *messagepb.MooxMessage, expectedShardID string) error {
	if msg == nil {
		return errors.New("storage message is nil")
	}
	if msg.GetKind() != messagepb.MessageKind_MESSAGE_KIND_EVENT {
		return errors.New("storage rows committed message kind must be EVENT")
	}
	if msg.GetSequence() == 0 {
		return errors.New("storage rows committed message sequence is required")
	}
	tokens := strings.Split(msg.GetTopic(), ".")
	if len(tokens) < 2 {
		return fmt.Errorf("storage rows committed topic %q is invalid", msg.GetTopic())
	}
	shardID, err := jetstream.DecodeShardToken(tokens[len(tokens)-1])
	if err != nil {
		return fmt.Errorf("storage rows committed topic shard: %w", err)
	}
	if expectedShardID != "" && shardID != expectedShardID {
		return fmt.Errorf("storage rows committed topic shard %q does not match expected shard %q", shardID, expectedShardID)
	}
	base := strings.Join(tokens[:len(tokens)-1], ".")
	var payloadShard, payloadSpace, payloadDataset string
	switch msg.GetMessageType() {
	case defaultTimeSeriesRowsCommittedType:
		if !strings.HasSuffix(base, "."+defaultTimeSeriesRowsCommittedBase) {
			return fmt.Errorf("time-series message topic %q is invalid", msg.GetTopic())
		}
		event := new(pb.TimeSeriesRowsCommitted)
		if err := proto.Unmarshal(msg.GetPayload(), event); err != nil {
			return fmt.Errorf("decode time-series rows committed payload: %w", err)
		}
		payloadShard, payloadSpace, payloadDataset = event.GetShardId(), event.GetSpaceId(), event.GetDatasetId()
	case defaultRecordRowsCommittedType:
		if !strings.HasSuffix(base, "."+defaultRecordRowsCommittedBase) {
			return fmt.Errorf("record message topic %q is invalid", msg.GetTopic())
		}
		event := new(pb.RecordRowsCommitted)
		if err := proto.Unmarshal(msg.GetPayload(), event); err != nil {
			return fmt.Errorf("decode record rows committed payload: %w", err)
		}
		payloadShard, payloadSpace, payloadDataset = event.GetShardId(), event.GetSpaceId(), event.GetDatasetId()
	default:
		return fmt.Errorf("unknown storage message_type %q", msg.GetMessageType())
	}
	if payloadShard == "" || payloadShard != shardID {
		return fmt.Errorf("storage payload shard_id %q does not match topic shard %q", payloadShard, shardID)
	}
	if msg.GetSpaceId() != payloadSpace {
		return fmt.Errorf("storage payload space_id %q does not match message space_id %q", payloadSpace, msg.GetSpaceId())
	}
	if dataset := msg.GetAttributes()["dataset_id"]; dataset != "" && dataset != payloadDataset {
		return fmt.Errorf("storage payload dataset_id %q does not match message attribute %q", payloadDataset, dataset)
	}
	return nil
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

func (b *SubscriberBus) consumerRef(_ bool) jetstream.ConsumerRef {
	return jetstream.ConsumerRef{
		Stream: b.opts.StreamName, Durable: "storage_view_builder_rows_committed_v1", FilterSubject: RowsCommittedSubjectWildcard(b.subjectPrefix()),
		AckWait: b.opts.AckWait, MaxDeliver: b.opts.MaxDeliver, MaxAckPending: b.opts.MaxAckPending,
	}
}

func (b *SubscriberBus) SubscribeTimeSeriesRowsCommitted(ctx context.Context, handler coreeventbus.TimeSeriesRowsCommittedHandler) (coreeventbus.Subscription, error) {
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subscribeClosed {
		return nil, context.Canceled
	}
	if b.timeSeriesStopping || b.recordStopping {
		return nil, errors.New("storage time-series subscription is stopping")
	}
	if b.timeSeriesConsumer == nil {
		consumer, err := b.client.BindPullConsumer(ctx, b.consumerRef(true))
		if err != nil {
			return nil, err
		}
		b.timeSeriesConsumer = consumer
		b.startLoop(consumer)
	}
	b.nextID++
	id := b.nextID
	entryCtx, entryCancel := context.WithCancel(trpc.BackgroundContext())
	entry := &subscriberTimeSeriesHandler{handler: handler, lifecycle: newSubscriberHandlerLifecycle(), ctx: entryCtx, cancel: entryCancel}
	b.timeSeriesHandlers[id] = entry
	return &subscriberBusSubscription{close: func() error { return b.closeTimeSeries(id, entry) }}, nil
}

func (b *SubscriberBus) SubscribeRecordRowsCommitted(ctx context.Context, handler coreeventbus.RecordRowsCommittedHandler) (coreeventbus.Subscription, error) {
	if handler == nil {
		return noopSubscription{}, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subscribeClosed {
		return nil, context.Canceled
	}
	if b.recordStopping || b.timeSeriesStopping {
		return nil, errors.New("storage record subscription is stopping")
	}
	if b.timeSeriesConsumer == nil {
		consumer, err := b.client.BindPullConsumer(ctx, b.consumerRef(false))
		if err != nil {
			return nil, err
		}
		b.timeSeriesConsumer = consumer
		b.startLoop(consumer)
	}
	b.nextID++
	id := b.nextID
	entryCtx, entryCancel := context.WithCancel(trpc.BackgroundContext())
	entry := &subscriberRecordHandler{handler: handler, lifecycle: newSubscriberHandlerLifecycle(), ctx: entryCtx, cancel: entryCancel}
	b.recordHandlers[id] = entry
	return &subscriberBusSubscription{close: func() error { return b.closeRecord(id, entry) }}, nil
}

func (b *SubscriberBus) startLoop(consumer *jetstream.PullConsumer) {
	ctx, cancel := context.WithCancel(trpc.BackgroundContext())
	b.timeSeriesCancel = cancel
	fetchWG := &b.timeSeriesFetchWG
	handlerWG := &b.timeSeriesHandleWG
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
					timeSeries := delivery != nil && delivery.Message != nil && delivery.Message.GetMessageType() == defaultTimeSeriesRowsCommittedType
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
					log.ErrorContextf(ctx, "[StorageEventBus] delivery failed message_id=%s delivery_count=%d: %v", messageID, deliveryCount, err)
				}
			}(delivery)
		}
	}()
}

func (b *SubscriberBus) dispatch(ctx context.Context, delivery *jetstream.Delivery, timeSeries bool) error {
	if delivery == nil || delivery.Message == nil {
		return errors.New("nil storage event delivery")
	}
	expectedShard := ""
	if err := validateRowsCommittedMessage(delivery.Message, expectedShard); err != nil {
		return err
	}
	if timeSeries && delivery.Message.GetMessageType() != defaultTimeSeriesRowsCommittedType {
		return errors.New("time-series subscription received a non-time-series message")
	}
	if !timeSeries && delivery.Message.GetMessageType() != defaultRecordRowsCommittedType {
		return errors.New("record subscription received a non-record message")
	}
	var err error
	b.mu.Lock()
	if timeSeries {
		var event pb.TimeSeriesRowsCommitted
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
		err = callTimeSeriesHandlers(ctx, &event, handlers)
	} else {
		var event pb.RecordRowsCommitted
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
		err = callRecordHandlers(ctx, &event, handlers)
	}
	return err
}

func (b *SubscriberBus) handlerDrainTimeout() time.Duration {
	if b != nil && b.opts.HandlerDrainTimeout > 0 {
		return b.opts.HandlerDrainTimeout
	}
	return defaultHandlerDrainTimeout
}

func callTimeSeriesHandlers(ctx context.Context, event *pb.TimeSeriesRowsCommitted, handlers []*subscriberTimeSeriesHandler) (err error) {
	next := 0
	defer func() {
		for ; next < len(handlers); next++ {
			handlers[next].lifecycle.release()
		}
	}()
	for next < len(handlers) {
		entry := handlers[next]
		next++
		err = errors.Join(err, entry.call(ctx, event))
	}
	return err
}

func callRecordHandlers(ctx context.Context, event *pb.RecordRowsCommitted, handlers []*subscriberRecordHandler) (err error) {
	next := 0
	defer func() {
		for ; next < len(handlers); next++ {
			handlers[next].lifecycle.release()
		}
	}()
	for next < len(handlers) {
		entry := handlers[next]
		next++
		err = errors.Join(err, entry.call(ctx, event))
	}
	return err
}

func (b *SubscriberBus) closeTimeSeries(id uint64, entry *subscriberTimeSeriesHandler) error {
	b.mu.Lock()
	delete(b.timeSeriesHandlers, id)
	entry.lifecycle.beginClose()
	entry.cancel()
	if len(b.timeSeriesHandlers) != 0 || len(b.recordHandlers) != 0 || b.timeSeriesCancel == nil {
		b.mu.Unlock()
		return entry.lifecycle.wait(b.handlerDrainTimeout())
	}
	cancel, consumer := b.timeSeriesCancel, b.timeSeriesConsumer
	b.timeSeriesStopping = true
	b.timeSeriesCancel, b.timeSeriesConsumer = nil, nil
	b.mu.Unlock()
	cancel()
	drainErr := entry.lifecycle.wait(b.handlerDrainTimeout())
	b.timeSeriesFetchWG.Wait()
	b.timeSeriesHandleWG.Wait()
	var err error
	if consumer != nil {
		err = consumer.Close()
	}
	b.mu.Lock()
	b.timeSeriesStopping = false
	b.mu.Unlock()
	return errors.Join(drainErr, err)
}
func (b *SubscriberBus) closeRecord(id uint64, entry *subscriberRecordHandler) error {
	b.mu.Lock()
	delete(b.recordHandlers, id)
	entry.lifecycle.beginClose()
	entry.cancel()
	if len(b.recordHandlers) != 0 || len(b.timeSeriesHandlers) != 0 || b.timeSeriesCancel == nil {
		b.mu.Unlock()
		return entry.lifecycle.wait(b.handlerDrainTimeout())
	}
	cancel, consumer := b.timeSeriesCancel, b.timeSeriesConsumer
	b.recordStopping = true
	b.timeSeriesCancel, b.timeSeriesConsumer = nil, nil
	b.mu.Unlock()
	cancel()
	drainErr := entry.lifecycle.wait(b.handlerDrainTimeout())
	b.timeSeriesFetchWG.Wait()
	b.timeSeriesHandleWG.Wait()
	var err error
	if consumer != nil {
		err = consumer.Close()
	}
	b.mu.Lock()
	b.recordStopping = false
	b.mu.Unlock()
	return errors.Join(drainErr, err)
}

type subscriberTimeSeriesHandler struct {
	handler   coreeventbus.TimeSeriesRowsCommittedHandler
	lifecycle *subscriberHandlerLifecycle
	ctx       context.Context
	cancel    context.CancelFunc
}

func (h *subscriberTimeSeriesHandler) call(ctx context.Context, event *pb.TimeSeriesRowsCommitted) error {
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
	handler   coreeventbus.RecordRowsCommittedHandler
	lifecycle *subscriberHandlerLifecycle
	ctx       context.Context
	cancel    context.CancelFunc
}

func (h *subscriberRecordHandler) call(ctx context.Context, event *pb.RecordRowsCommitted) error {
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
	closing  bool
	inFlight int
	idle     chan struct{}
}

func newSubscriberHandlerLifecycle() *subscriberHandlerLifecycle {
	idle := make(chan struct{})
	close(idle)
	return &subscriberHandlerLifecycle{idle: idle}
}

func (l *subscriberHandlerLifecycle) acquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closing {
		return false
	}
	if l.inFlight == 0 {
		l.idle = make(chan struct{})
	}
	l.inFlight++
	return true
}

func (l *subscriberHandlerLifecycle) release() {
	l.mu.Lock()
	l.inFlight--
	if l.inFlight == 0 {
		close(l.idle)
	}
	l.mu.Unlock()
}

func (l *subscriberHandlerLifecycle) beginClose() {
	l.mu.Lock()
	l.closing = true
	l.mu.Unlock()
}

func (l *subscriberHandlerLifecycle) wait(timeout time.Duration) error {
	l.mu.Lock()
	idle := l.idle
	l.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-time.After(timeout):
		return errors.New("storage subscriber handler drain timed out")
	}
}

func (b *SubscriberBus) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.subscribeClosed = true
	timeSeriesCancel := b.timeSeriesCancel
	ts := b.timeSeriesConsumer
	b.timeSeriesCancel, b.recordCancel = nil, nil
	b.timeSeriesConsumer, b.recordConsumer = nil, nil
	b.mu.Unlock()
	if timeSeriesCancel != nil {
		timeSeriesCancel()
	}
	b.timeSeriesFetchWG.Wait()
	b.timeSeriesHandleWG.Wait()
	if ts != nil {
		_ = ts.Close()
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
