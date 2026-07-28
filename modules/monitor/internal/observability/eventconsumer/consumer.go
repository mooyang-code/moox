package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	DefaultStream        = "MOOX_OBSERVABILITY"
	DefaultConsumer      = "monitor_observability_ingest_v1"
	DefaultFilterSubject = "moox.observability.>"
)

var rejectedEvents = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "moox_monitor_observability_rejected_total",
	Help: "Observability events terminated because their envelope, payload, or route result was invalid.",
})

func init() {
	prometheus.MustRegister(rejectedEvents)
}

type Config struct {
	Stream         string
	Consumer       string
	FilterSubject  string
	FetchBatchSize int
	FetchMaxWait   time.Duration
	AckWait        time.Duration
	MaxDeliver     int
	MaxAckPending  int
	DeliverPolicy  nats.DeliverPolicy
}

func DefaultConfig() Config {
	return Config{
		Stream: DefaultStream, Consumer: DefaultConsumer, FilterSubject: DefaultFilterSubject,
		FetchBatchSize: 64, FetchMaxWait: time.Second, AckWait: time.Minute,
		MaxDeliver: 3, MaxAckPending: 256, DeliverPolicy: nats.DeliverAllPolicy,
	}
}

type Routes struct {
	Metrics func(context.Context, *eventpb.EventMessage, *metricspb.MetricReport) error
	Host    func(context.Context, *eventpb.EventMessage, *hostmetricpb.HostMetric) error
	Health  func(context.Context, *eventpb.EventMessage, *observabilitypb.HealthCheckReport) error
}

func (r Routes) validate() error {
	if r.Metrics == nil || r.Host == nil || r.Health == nil {
		return errors.New("observability metrics, host, and health routes are required")
	}
	return nil
}

type Consumer struct {
	consumer *jetstream.Consumer
	registry *events.Registry
	routes   Routes
	cfg      Config

	stopOnce  sync.Once
	readyOnce sync.Once
	readyMu   sync.Mutex
	ready     func()
}

func NewConsumer(ctx context.Context, client *jetstream.Client, registry *events.Registry, cfg Config, routes Routes) (*Consumer, error) {
	if err := routes.validate(); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("observability eventbus client is required")
	}
	if registry == nil {
		return nil, errors.New("observability event registry is required")
	}
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	cfg = withDefaults(cfg)
	if cfg.Stream != DefaultStream || cfg.Consumer != DefaultConsumer || cfg.FilterSubject != DefaultFilterSubject {
		return nil, fmt.Errorf("observability consumer topology must be stream=%s consumer=%s filter=%s", DefaultStream, DefaultConsumer, DefaultFilterSubject)
	}
	pull, err := client.NewConsumer(ctx, jetstream.ConsumerConfig{
		Stream: cfg.Stream, Durable: cfg.Consumer, FilterSubject: cfg.FilterSubject,
		AckWait: cfg.AckWait, MaxDeliver: cfg.MaxDeliver, MaxAckPending: cfg.MaxAckPending,
		FetchMaxWait: cfg.FetchMaxWait, DeliverPolicy: cfg.DeliverPolicy,
		DeliverDecodeErrors: true,
	})
	if err != nil {
		return nil, err
	}
	return &Consumer{consumer: pull, registry: registry, routes: routes, cfg: cfg}, nil
}

func withDefaults(cfg Config) Config {
	defaults := DefaultConfig()
	if cfg.Stream == "" {
		cfg.Stream = defaults.Stream
	}
	if cfg.Consumer == "" {
		cfg.Consumer = defaults.Consumer
	}
	if cfg.FilterSubject == "" {
		cfg.FilterSubject = defaults.FilterSubject
	}
	if cfg.FetchBatchSize <= 0 {
		cfg.FetchBatchSize = defaults.FetchBatchSize
	}
	if cfg.FetchMaxWait <= 0 {
		cfg.FetchMaxWait = defaults.FetchMaxWait
	}
	if cfg.AckWait <= 0 {
		cfg.AckWait = defaults.AckWait
	}
	if cfg.MaxDeliver == 0 {
		cfg.MaxDeliver = defaults.MaxDeliver
	}
	if cfg.MaxAckPending <= 0 {
		cfg.MaxAckPending = defaults.MaxAckPending
	}
	if cfg.DeliverPolicy != nats.DeliverAllPolicy && cfg.DeliverPolicy != nats.DeliverNewPolicy {
		cfg.DeliverPolicy = defaults.DeliverPolicy
	}
	return cfg
}

func (c *Consumer) Fetch(ctx context.Context, batch int) ([]*jetstream.Delivery, error) {
	if c == nil || c.consumer == nil {
		return nil, errors.New("observability consumer is not initialized")
	}
	c.readyMu.Lock()
	ready := c.ready
	c.readyMu.Unlock()
	if ready != nil {
		c.readyOnce.Do(ready)
	}
	return c.consumer.Fetch(ctx, batch)
}

func (c *Consumer) Run(ctx context.Context, onReceiveReady ...func()) error {
	if c == nil || c.consumer == nil {
		return errors.New("observability consumer is not initialized")
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if len(onReceiveReady) > 0 {
		c.readyMu.Lock()
		c.ready = onReceiveReady[0]
		c.readyMu.Unlock()
	}
	return jetstream.NewRunner(c, c, jetstream.RunnerConfig{
		BatchSize: c.cfg.FetchBatchSize,
		ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
			if ctx.Err() == nil {
				log.WarnContextf(ctx, "monitor observability delivery failed: %v", err)
			}
		}),
	}).Run(ctx)
}

func (c *Consumer) Close() error {
	if c == nil {
		return nil
	}
	var err error
	c.stopOnce.Do(func() {
		if c.consumer != nil {
			err = c.consumer.Close()
		}
	})
	return err
}

func (c *Consumer) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil {
		return c.reject(errors.New("observability delivery is nil"))
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	decoded := events.DecodeDelivery(c.registry, delivery)
	if decoded.Err != nil {
		return c.reject(fmt.Errorf("decode observability event: %w", decoded.Err))
	}
	message := decoded.Message
	switch {
	case matches(message, events.ObservabilityMetricsSnapshotReported):
		payload, ok := decoded.Payload.(*metricspb.MetricReport)
		if !ok {
			return c.reject(fmt.Errorf("metrics route payload has type %T", decoded.Payload))
		}
		if err := c.routes.Metrics(ctx, message, payload); err != nil {
			return c.routeError("metrics", err, delivery.DeliveryCount)
		}
	case matches(message, events.ObservabilityHostSnapshotReported):
		payload, ok := decoded.Payload.(*hostmetricpb.HostMetric)
		if !ok {
			return c.reject(fmt.Errorf("host route payload has type %T", decoded.Payload))
		}
		if err := c.routes.Host(ctx, message, payload); err != nil {
			return c.routeError("host", err, delivery.DeliveryCount)
		}
	case matches(message, events.ObservabilityHealthCheckReported):
		payload, ok := decoded.Payload.(*observabilitypb.HealthCheckReport)
		if !ok {
			return c.reject(fmt.Errorf("health route payload has type %T", decoded.Payload))
		}
		if err := c.routes.Health(ctx, message, payload); err != nil {
			return c.routeError("health", err, delivery.DeliveryCount)
		}
	default:
		return c.reject(fmt.Errorf("unsupported observability event %s@%d", message.GetEventName(), message.GetEventVersion()))
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func matches(message *eventpb.EventMessage, event events.Event) bool {
	return message != nil && message.GetEventName() == event.Name() && message.GetEventVersion() == event.Version()
}

type permanentError struct {
	err error
}

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

func (c *Consumer) routeError(route string, err error, deliveryCount uint64) jetstream.HandlerResult {
	var permanent permanentError
	if errors.As(err, &permanent) {
		return c.reject(fmt.Errorf("%s route: %w", route, err))
	}
	return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: retryDelay(deliveryCount), Err: err}
}

func (c *Consumer) reject(err error) jetstream.HandlerResult {
	rejectedEvents.Inc()
	return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
}

func retryDelay(deliveryCount uint64) time.Duration {
	switch {
	case deliveryCount <= 1:
		return time.Second
	case deliveryCount == 2:
		return 5 * time.Second
	default:
		return 15 * time.Second
	}
}
