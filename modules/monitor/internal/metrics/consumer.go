package metrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/jetstream"
	messagepb "github.com/mooyang-code/moox/packages/messagepb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	trpc "trpc.group/trpc-go/trpc-go"
)

const (
	MetricTopic                    = "moox.metrics.snapshot.reported.v1"
	MetricContentType              = "application/vnd.moox.metrics.snapshot+protobuf"
	MetricDLQTopic                 = "moox.dlq.message.rejected.v1"
	unknownProducerGraceDeliveries = 120
)

type ProducerAuthorizer interface {
	IsRegistered(context.Context, string, string) (bool, error)
}

// CheckProducerAuthorizer authorizes metric producers against monitor checks.
// The persistence implementation lives in store; the consumer only depends
// on the narrow capability it needs.
type CheckProducerAuthorizer struct{ Checks *store.CheckRepository }

func (a CheckProducerAuthorizer) IsRegistered(ctx context.Context, serviceName, _ string) (bool, error) {
	if a.Checks == nil {
		return false, errors.New("check producer authorizer is not initialized")
	}
	return a.Checks.IsSysDeployRegistered(ctx, serviceName)
}

type DLQPublisher interface {
	Publish(context.Context, *messagepb.MooxMessage) error
}
type jetstreamDLQ struct {
	client   *jetstream.Client
	producer *messagepb.Producer
}

func JetStreamDLQ(client *jetstream.Client, service, instance string) DLQPublisher {
	if client == nil {
		return nil
	}
	if service == "" {
		service = "moox-monitor"
	}
	if instance == "" {
		instance = "unknown"
	}
	return jetstreamDLQ{client: client, producer: &messagepb.Producer{ServiceName: service, InstanceId: instance}}
}

func (p jetstreamDLQ) Publish(ctx context.Context, msg *messagepb.MooxMessage) error {
	if p.client == nil {
		return errors.New("metrics DLQ eventbus client is nil")
	}
	if msg == nil {
		return errors.New("metrics DLQ message is nil")
	}
	_, err := p.client.Publish(ctx, msg)
	return err
}

type ConsumerOptions struct {
	Client       *jetstream.Client
	Storage      *StorageAdapter
	MessageStore *MetricMessageStore
	Authorizer   ProducerAuthorizer
	DLQ          DLQPublisher
	Config       monconfig.MetricsConfig
	ServiceName  string
	InstanceID   string
}
type Consumer struct {
	pull     *jetstream.PullConsumer
	opts     ConsumerOptions
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewConsumer(ctx context.Context, opts ConsumerOptions) (*Consumer, error) {
	if opts.Client == nil {
		return nil, errors.New("metrics eventbus client is nil")
	}
	if opts.Storage == nil || opts.MessageStore == nil {
		return nil, errors.New("metrics storage and message store are required")
	}
	cfg := opts.Config
	if cfg.Stream == "" {
		cfg.Stream = "MOOX_METRICS"
	}
	if cfg.Topic == "" {
		cfg.Topic = MetricTopic
	}
	if cfg.Consumer == "" {
		cfg.Consumer = "monitor_metrics_ingest_v1"
	}
	if cfg.FetchBatchSize <= 0 {
		cfg.FetchBatchSize = 64
	}
	if cfg.FetchMaxWait <= 0 {
		cfg.FetchMaxWait = time.Second
	}
	if cfg.AckWait <= 0 {
		cfg.AckWait = time.Minute
	}
	if cfg.MaxAckPending <= 0 {
		cfg.MaxAckPending = 256
	}
	pull, err := opts.Client.NewPullConsumer(ctx, jetstream.ConsumerConfig{Stream: cfg.Stream, Durable: cfg.Consumer, FilterSubject: cfg.Topic, AckWait: cfg.AckWait, MaxDeliver: 3, MaxAckPending: cfg.MaxAckPending, FetchMaxWait: cfg.FetchMaxWait, DeliverDecodeErrors: true})
	if err != nil {
		return nil, err
	}
	opts.Config = cfg
	return &Consumer{pull: pull, opts: opts}, nil
}

// Fetch pulls up to batch deliveries for tests and operational drain paths.
func (c *Consumer) Fetch(ctx context.Context, batch int) ([]*jetstream.Delivery, error) {
	if c == nil || c.pull == nil {
		return nil, errors.New("metrics consumer is nil")
	}
	return c.pull.Fetch(ctx, batch)
}

func (c *Consumer) Close() error {
	if c == nil {
		return nil
	}
	c.stopOnce.Do(func() {
		if c.pull != nil {
			_ = c.pull.Close()
		}
		c.wg.Wait()
	})
	return nil
}
func (c *Consumer) Run(ctx context.Context) error {
	if c == nil || c.pull == nil {
		return errors.New("metrics consumer is not initialized")
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	c.wg.Add(1)
	defer c.wg.Done()
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		deliveries, fetchErr := c.pull.Fetch(ctx, c.opts.Config.FetchBatchSize)
		consumerPending.Set(float64(len(deliveries)))
		for _, d := range deliveries {
			_ = c.HandleDelivery(ctx, d)
		}
		consumerPending.Set(0)
		if fetchErr != nil {
			if errors.Is(fetchErr, context.Canceled) || errors.Is(fetchErr, context.DeadlineExceeded) {
				return nil
			}
			if errors.Is(fetchErr, jetstream.ErrClosed) {
				return nil
			}
			// A decode error may accompany valid deliveries (and, when enabled,
			// a poison delivery). Those are handled above; continue fetching.
			continue
		}
	}
}

// RunWhenReady keeps the monitor process alive while Storage metadata or the
// central EventBus is being deployed. It binds the durable consumer only after
// read-only schema validation succeeds, avoiding a NAK loop on fresh installs.
func RunWhenReady(ctx context.Context, opts ConsumerOptions) error {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if opts.Storage == nil {
		return errors.New("metrics storage is not initialized")
	}
	interval := opts.Config.Storage.MetadataValidationInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := opts.Storage.ValidateSchema(ctx); err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(interval):
			}
			continue
		}
		consumer, err := NewConsumer(ctx, opts)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(interval):
			}
			continue
		}
		err = consumer.Run(ctx)
		_ = consumer.Close()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(interval):
			}
		}
	}
}

func (c *Consumer) HandleDelivery(ctx context.Context, d *jetstream.Delivery) error {
	if d == nil {
		return errors.New("empty metric delivery")
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	actionCtx := ctx
	if d.Message == nil {
		if d.DecodeError == nil {
			return errors.New("empty metric delivery")
		}
		reason := fmt.Errorf("decode metric envelope: %w", d.DecodeError)
		recordIngest("rejected", time.Time{})
		if c.opts.DLQ == nil {
			_ = d.Term(actionCtx)
			return reason
		}
		dlq := malformedRejectionMessage(d, reason.Error(), c.opts.ServiceName, c.opts.InstanceID)
		if err := c.opts.DLQ.Publish(ctx, dlq); err != nil {
			_ = d.Nak(actionCtx, time.Second)
			return fmt.Errorf("publish metrics DLQ: %w", err)
		}
		dlqTotal.Inc()
		if err := d.Term(actionCtx); err != nil {
			return fmt.Errorf("term rejected metric: %w", err)
		}
		return reason
	}
	msg := d.Message
	permanent := func(reason error) error {
		recordIngest("rejected", time.Time{})
		if c.opts.DLQ == nil {
			_ = d.Term(actionCtx)
			return reason
		}
		dlq := rejectionMessage(msg, reason.Error(), c.opts.ServiceName, c.opts.InstanceID)
		if err := c.opts.DLQ.Publish(ctx, dlq); err != nil {
			_ = d.Nak(actionCtx, time.Second)
			return fmt.Errorf("publish metrics DLQ: %w", err)
		}
		dlqTotal.Inc()
		if err := d.Term(actionCtx); err != nil {
			return fmt.Errorf("term rejected metric: %w", err)
		}
		return reason
	}
	if msg.GetTopic() != c.opts.Config.Topic && msg.GetTopic() != MetricTopic {
		return permanent(fmt.Errorf("unsupported metric topic %q", msg.GetTopic()))
	}
	if msg.GetKind() != messagepb.MessageKind_MESSAGE_KIND_SNAPSHOT {
		return permanent(fmt.Errorf("unsupported metric message kind %s", msg.GetKind()))
	}
	if msg.GetContentType() != MetricContentType && msg.GetContentType() != "application/x-protobuf" {
		return permanent(fmt.Errorf("unsupported metric content type %q", msg.GetContentType()))
	}
	if msg.GetSpaceId() != "" && msg.GetSpaceId() != InternalMetricSpaceID {
		return permanent(fmt.Errorf("unsupported metric space %q", msg.GetSpaceId()))
	}
	if msg.GetProducer() == nil {
		return permanent(errors.New("metric producer is missing"))
	}
	if c.opts.Authorizer != nil {
		ok, err := c.opts.Authorizer.IsRegistered(ctx, msg.GetProducer().GetServiceName(), msg.GetProducer().GetInstanceId())
		if err != nil {
			return c.retry(actionCtx, d, fmt.Errorf("authorize metric producer: %w", err))
		}
		if !ok {
			// SysDeploy synchronization is asynchronous on Monitor startup. Keep
			// a legitimate first snapshot long enough for the registry to converge;
			// persistently unknown producers still go to the DLQ.
			if d.DeliveryCount < unknownProducerGraceDeliveries {
				return c.retry(actionCtx, d, fmt.Errorf("metric producer %s/%s is not registered yet", msg.GetProducer().GetServiceName(), msg.GetProducer().GetInstanceId()))
			}
			return permanent(fmt.Errorf("unregistered metric producer %s/%s", msg.GetProducer().GetServiceName(), msg.GetProducer().GetInstanceId()))
		}
	}
	duplicate, err := c.opts.MessageStore.IsDuplicate(ctx, msg.GetMessageId())
	if err != nil {
		return c.retry(actionCtx, d, err)
	}
	if duplicate {
		return d.Ack(ctx)
	}
	snapshot := new(metricspb.MetricSnapshot)
	if err := proto.Unmarshal(msg.GetPayload(), snapshot); err != nil {
		return permanent(fmt.Errorf("decode metric snapshot: %w", err))
	}
	observed := time.Now().UTC()
	if msg.GetOccurredAt() != nil {
		observed = msg.GetOccurredAt().AsTime()
	}
	producer := msg.GetProducer()
	samples, err := ParseSnapshot(snapshot, Envelope{ServiceName: producer.GetServiceName(), InstanceID: producer.GetInstanceId(), MessageID: msg.GetMessageId(), ProducerNodeID: producer.GetNodeId(), ProducerVersion: producer.GetVersion(), ObservedAt: observed}, DefaultLimits())
	if err != nil {
		return permanent(err)
	}
	if err := c.opts.Storage.WriteSamples(ctx, samples); err != nil {
		return c.retry(actionCtx, d, err)
	}
	duplicate, err = c.opts.MessageStore.CommitIngest(ctx, msg, samples)
	if err != nil {
		return c.retry(actionCtx, d, err)
	}
	if duplicate {
		return d.Ack(ctx)
	}
	recordIngest("success", observed)
	return d.Ack(ctx)
}
func (c *Consumer) retry(ctx context.Context, d *jetstream.Delivery, err error) error {
	recordIngest("error", time.Time{})
	if d == nil {
		return err
	}
	if nakErr := d.Nak(ctx, time.Second); nakErr != nil {
		return fmt.Errorf("%v; nak: %w", err, nakErr)
	}
	return err
}
func rejectionMessage(original *messagepb.MooxMessage, reason, service, instance string) *messagepb.MooxMessage {
	payload, _ := proto.Marshal(original)
	now := timestamppb.Now()
	return &messagepb.MooxMessage{ProtocolVersion: 1, MessageId: original.GetMessageId() + ".rejected", Topic: MetricDLQTopic, Kind: messagepb.MessageKind_MESSAGE_KIND_EVENT, Producer: &messagepb.Producer{ServiceName: service, InstanceId: instance}, OccurredAt: now, PublishedAt: now, ContentType: "application/x-protobuf", MessageType: "moox.monitor.rejected.v1", Payload: payload, Attributes: map[string]string{"rejection_reason": reason, "original_topic": original.GetTopic()}}
}

func malformedRejectionMessage(delivery *jetstream.Delivery, reason, service, instance string) *messagepb.MooxMessage {
	now := timestamppb.Now()
	id := "invalid-envelope"
	if delivery != nil && delivery.RawMessageID != "" {
		id = delivery.RawMessageID
	}
	var payload []byte
	var topic string
	if delivery != nil {
		payload = append([]byte(nil), delivery.RawData...)
		topic = delivery.Subject
	}
	if id == "invalid-envelope" {
		hashInput := append([]byte(topic+"\x00"), payload...)
		sum := sha256.Sum256(hashInput)
		id += "-" + hex.EncodeToString(sum[:8])
	}
	if service == "" {
		service = "moox-monitor"
	}
	if instance == "" {
		instance = "unknown"
	}
	return &messagepb.MooxMessage{
		ProtocolVersion: 1,
		MessageId:       id + ".rejected",
		Topic:           MetricDLQTopic,
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer:        &messagepb.Producer{ServiceName: service, InstanceId: instance},
		OccurredAt:      now,
		PublishedAt:     now,
		ContentType:     "application/octet-stream",
		MessageType:     "moox.monitor.rejected.v1",
		Payload:         payload,
		Attributes:      map[string]string{"rejection_reason": reason, "original_topic": topic, "original_message_id": id},
	}
}
