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

const ViewDataReadyConsumerName = "strategy_view_data_ready_v1"

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
	runners   []*jetstream.Runner
	consumers []*jetstream.Consumer
	cancel    context.CancelFunc
	ready     bool
	mu        sync.Mutex
}

func New(cfg Config, processor *trigger.Processor) *Consumer {
	if cfg.ConsumerName == "" {
		cfg.ConsumerName = ViewDataReadyConsumerName
	}
	if cfg.AckWait <= 0 {
		cfg.AckWait = 30 * time.Second
	}
	if cfg.MaxDeliver == 0 {
		// Bound poison periods so a permanently incomplete View cannot consume
		// the entire durable consumer's pending window. A later ready event or
		// an explicit recalc can retry the same bar after the terminal ACK.
		cfg.MaxDeliver = 10
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
	filter, err := registry.FamilyPattern(events.ViewDataReady)
	if err != nil {
		return err
	}
	readyConsumer, err := c.cfg.Client.NewConsumer(ctx, jetstream.ConsumerConfig{Stream: "MOOX_STORAGE", Durable: c.cfg.ConsumerName, FilterSubject: filter, AckWait: c.cfg.AckWait, MaxDeliver: c.cfg.MaxDeliver, MaxAckPending: c.cfg.MaxAckPending, FetchMaxWait: c.cfg.FetchMaxWait, DeliverPolicy: nats.DeliverNewPolicy, DeliverDecodeErrors: true})
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	runner := jetstream.NewRunner(readyConsumer, jetstream.DeliveryHandlerFunc(func(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
		return HandleViewPeriodReady(ctx, delivery, c.processor)
	}), jetstream.RunnerConfig{BatchSize: c.cfg.BatchSize})
	c.mu.Lock()
	c.consumers = []*jetstream.Consumer{readyConsumer}
	c.runners = []*jetstream.Runner{runner}
	c.cancel = cancel
	c.ready = true
	c.mu.Unlock()
	go func() {
		_ = runner.Run(runCtx)
		c.mu.Lock()
		c.ready = false
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
	var closeErr error
	for _, consumer := range c.consumers {
		if consumer != nil {
			closeErr = errors.Join(closeErr, consumer.Close())
		}
	}
	return closeErr
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
	return len(c.consumers) == 1 && c.cancel != nil && c.ready
}
