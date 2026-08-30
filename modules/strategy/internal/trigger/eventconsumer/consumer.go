package eventconsumer

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

const ViewFactorReadyConsumerName = "strategy_view_factor_ready_v1"

type Config struct {
	Client        *jetstream.Client
	ConsumerName  string
	AckWait       time.Duration
	MaxDeliver    int
	MaxAckPending int
	FetchMaxWait  time.Duration
	BatchSize     int
}

type Consumer struct {
	cfg       Config
	processor *trigger.Processor
	runner    *jetstream.Runner
	consumer  *jetstream.Consumer
	cancel    context.CancelFunc
	ready     bool
	mu        sync.Mutex
}

func New(cfg Config, processor *trigger.Processor) *Consumer {
	if cfg.ConsumerName == "" {
		cfg.ConsumerName = ViewFactorReadyConsumerName
	}
	if cfg.AckWait <= 0 {
		cfg.AckWait = 30 * time.Second
	}
	if cfg.MaxDeliver == 0 {
		// Readiness can lag a completed source event by an arbitrary amount;
		// transient dependency failures must not permanently lose a period.
		// Permanent decode/configuration failures are TERM'ed or marked in the
		// inbox by the processor, so an unlimited retry here is bounded by the
		// actual transient condition rather than poison messages.
		cfg.MaxDeliver = -1
	}
	if cfg.MaxAckPending <= 0 {
		cfg.MaxAckPending = 100
	}
	if cfg.FetchMaxWait <= 0 {
		cfg.FetchMaxWait = time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 10
	}
	return &Consumer{cfg: cfg, processor: processor}
}

func (c *Consumer) Start(ctx context.Context) error {
	if c == nil || c.cfg.Client == nil || c.processor == nil {
		return errors.New("strategy View-ready consumer is not configured")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	filter, err := registry.FamilyPattern(events.ViewFactorPeriodReady)
	if err != nil {
		return err
	}
	consumer, err := c.cfg.Client.NewConsumer(ctx, jetstream.ConsumerConfig{Stream: "MOOX_STORAGE", Durable: c.cfg.ConsumerName, FilterSubject: filter, AckWait: c.cfg.AckWait, MaxDeliver: c.cfg.MaxDeliver, MaxAckPending: c.cfg.MaxAckPending, FetchMaxWait: c.cfg.FetchMaxWait, DeliverPolicy: nats.DeliverNewPolicy, DeliverDecodeErrors: true})
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	runner := jetstream.NewRunner(consumer, jetstream.DeliveryHandlerFunc(func(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
		return HandleViewFactorPeriodReady(ctx, delivery, c.processor)
	}), jetstream.RunnerConfig{BatchSize: c.cfg.BatchSize})
	c.mu.Lock()
	c.consumer = consumer
	c.cancel = cancel
	c.runner = runner
	c.ready = true
	c.mu.Unlock()
	go func() {
		_ = runner.Run(runCtx)
		// A runner can stop because the broker connection failed or because a
		// handler returned an unrecoverable error. Do not leave readiness green
		// after that point: the process must be restarted or repaired before it
		// can safely claim to consume readiness events.
		c.mu.Lock()
		if c.runner == runner {
			c.ready = false
		}
		c.mu.Unlock()
	}()
	return nil
}

func (c *Consumer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
	c.ready = false
	if c.consumer != nil {
		return c.consumer.Close()
	}
	return nil
}

// Ready reports whether the durable consumer exists and its runner is still
// alive. A stopped runner makes Strategy unready so an external supervisor can
// restart it instead of silently dropping readiness events.
func (c *Consumer) Ready() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.consumer != nil && c.cancel != nil && c.ready
}
