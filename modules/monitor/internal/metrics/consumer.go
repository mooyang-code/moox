package metrics

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	trpc "trpc.group/trpc-go/trpc-go"
)

const (
	MetricTopic                    = "moox.metrics.reported.v1.>"
	unknownProducerGraceDeliveries = 120
)

type ProducerAuthorizer interface {
	IsRegistered(context.Context, string, string) (bool, error)
}

type CheckProducerAuthorizer struct{ Checks *store.CheckRepository }

func (a CheckProducerAuthorizer) IsRegistered(ctx context.Context, serviceName, _ string) (bool, error) {
	if a.Checks == nil {
		return false, errors.New("check producer authorizer is not initialized")
	}
	return a.Checks.IsSysDeployRegistered(ctx, serviceName)
}

type DLQPublisher interface {
	Publish(context.Context, *eventpb.EventMessage) error
}

type jetstreamDLQ struct {
	client   *jetstream.Client
	service  string
	instance string
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
	return jetstreamDLQ{client: client, service: service, instance: instance}
}

func (p jetstreamDLQ) Publish(ctx context.Context, message *eventpb.EventMessage) error {
	if p.client == nil {
		return errors.New("metrics DLQ eventbus client is nil")
	}
	if message == nil {
		return errors.New("metrics DLQ message is nil")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	publisher, err := events.NewPublisher(p.client, registry)
	if err != nil {
		return err
	}
	_, err = publisher.PublishMessage(ctx, message)
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
	pull, err := opts.Client.NewPullConsumer(ctx, jetstream.ConsumerConfig{Stream: cfg.Stream, Durable: cfg.Consumer, FilterSubject: cfg.Topic, AckWait: cfg.AckWait, MaxDeliver: 3, MaxAckPending: cfg.MaxAckPending, FetchMaxWait: cfg.FetchMaxWait})
	if err != nil {
		return nil, err
	}
	opts.Config = cfg
	return &Consumer{pull: pull, opts: opts}, nil
}

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
	return jetstream.NewRunner(c.pull, c, jetstream.RunnerConfig{BatchSize: c.opts.Config.FetchBatchSize}).Run(ctx)
}

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
		if ctx.Err() != nil {
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
		if err == nil {
			err = consumer.Run(ctx)
			_ = consumer.Close()
		}
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

func (c *Consumer) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: errors.New("empty metric delivery")}
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	decoded := events.DecodeDelivery(mustRegistry(), delivery)
	if decoded.Err != nil {
		return c.reject(ctx, delivery, fmt.Errorf("decode metric event: %w", decoded.Err))
	}
	message := decoded.Message
	report, ok := decoded.Payload.(*metricspb.MetricReport)
	if !ok || report.GetSnapshot() == nil {
		return c.reject(ctx, delivery, errors.New("metric report payload is invalid"))
	}
	if message.GetSpaceId() != InternalMetricSpaceID {
		return c.reject(ctx, delivery, fmt.Errorf("unsupported metric space %q", message.GetSpaceId()))
	}
	if c.opts.Authorizer != nil {
		registered, err := c.opts.Authorizer.IsRegistered(ctx, report.GetServiceName(), report.GetInstanceId())
		if err != nil {
			return c.retry(fmt.Errorf("authorize metric producer: %w", err))
		}
		if !registered {
			if delivery.DeliveryCount < unknownProducerGraceDeliveries {
				return c.retry(fmt.Errorf("metric producer %s/%s is not registered yet", report.GetServiceName(), report.GetInstanceId()))
			}
			return c.reject(ctx, delivery, fmt.Errorf("unregistered metric producer %s/%s", report.GetServiceName(), report.GetInstanceId()))
		}
	}
	duplicate, err := c.opts.MessageStore.IsDuplicate(ctx, message.GetEventId())
	if err != nil {
		return c.retry(err)
	}
	if duplicate {
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	}
	observed := message.GetOccurredAt().AsTime()
	samples, err := ParseSnapshot(report.GetSnapshot(), Envelope{ServiceName: report.GetServiceName(), InstanceID: report.GetInstanceId(), MessageID: message.GetEventId(), ProducerNodeID: report.GetNodeId(), ProducerVersion: report.GetServiceVersion(), ObservedAt: observed}, DefaultLimits())
	if err != nil {
		return c.reject(ctx, delivery, err)
	}
	if err := c.opts.Storage.WriteSamples(ctx, samples); err != nil {
		return c.retry(err)
	}
	duplicate, err = c.opts.MessageStore.CommitIngest(ctx, message, report, samples)
	if err != nil {
		return c.retry(err)
	}
	if duplicate {
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	}
	recordIngest("success", observed)
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func (c *Consumer) reject(ctx context.Context, delivery *jetstream.Delivery, reason error) jetstream.HandlerResult {
	recordIngest("rejected", time.Time{})
	if c.opts.DLQ == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: reason}
	}
	event, err := events.RejectedMessage(mustRegistry(), delivery, reason.Error(), c.opts.ServiceName, c.opts.InstanceID)
	if err != nil {
		return c.retry(err)
	}
	if err := c.opts.DLQ.Publish(ctx, event); err != nil {
		return c.retry(fmt.Errorf("publish metrics DLQ: %w", err))
	}
	dlqTotal.Inc()
	return jetstream.HandlerResult{Decision: jetstream.TERM}
}

func (c *Consumer) HandleDelivery(ctx context.Context, delivery *jetstream.Delivery) error {
	result := c.Handle(ctx, delivery)
	return errors.Join(result.Err, jetstream.ApplyHandlerResult(ctx, delivery, result))
}

func (c *Consumer) retry(err error) jetstream.HandlerResult {
	recordIngest("error", time.Time{})
	return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
}

func mustRegistry() *events.Registry {
	registry, _ := events.DefaultRegistry()
	return registry
}
