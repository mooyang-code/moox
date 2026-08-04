package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

const defaultMaxRetryAttempts = 10

type Config struct {
	Consumer         string
	AckWaitMS        int
	FetchBatch       int
	MaxWorkers       int
	MaxAckPending    int
	Ordering         string
	MaxRetryAttempts int
	ErrorReporter    jetstream.ErrorReporter
	Metrics          *observability.ViewMetrics
	BeforeProcess    func(context.Context, *jetstream.Delivery) error
	Lease            DeliveryLease
}

func (c Config) withDefaults() (Config, error) {
	if strings.TrimSpace(c.Consumer) == "" {
		c.Consumer = "storage_view"
	}
	if c.AckWaitMS == 0 {
		c.AckWaitMS = 120000
	}
	if c.FetchBatch == 0 {
		c.FetchBatch = 8
	}
	if c.MaxAckPending == 0 {
		c.MaxAckPending = c.FetchBatch
	}
	if c.MaxWorkers == 0 {
		c.MaxWorkers = 4
	}
	if c.MaxRetryAttempts == 0 {
		c.MaxRetryAttempts = defaultMaxRetryAttempts
	}
	c.Consumer = strings.TrimSpace(c.Consumer)
	if strings.TrimSpace(c.Ordering) == "" {
		c.Ordering = "subject"
	}
	c.Ordering = strings.ToLower(strings.TrimSpace(c.Ordering))
	if c.FetchBatch < 1 {
		return c, errors.New("storage view fetch_batch must be positive")
	}
	if c.MaxWorkers < 1 {
		return c, errors.New("storage view max_workers must be positive")
	}
	if c.MaxRetryAttempts < 1 {
		return c, errors.New("storage view max_retry_attempts must be positive")
	}
	if c.Ordering != "subject" {
		return c, fmt.Errorf("storage view ordering %q is unsupported", c.Ordering)
	}
	if c.MaxAckPending < 0 {
		return c, errors.New("storage view max_ack_pending must not be negative")
	}
	if c.AckWaitMS < 1 {
		return c, errors.New("storage view ack_wait_ms must be positive")
	}
	if c.MaxAckPending > 0 && c.FetchBatch > c.MaxAckPending {
		return c, fmt.Errorf("storage view fetch_batch %d exceeds max_ack_pending %d", c.FetchBatch, c.MaxAckPending)
	}
	if c.Metrics == nil {
		c.Metrics = observability.DefaultViewMetrics
	}
	if c.ErrorReporter == nil {
		c.ErrorReporter = jetstream.ErrorReporterFunc(func(err error) {
			log.Printf("storage view event consumer error: %v", err)
		})
	}
	return c, nil
}

type Consumer struct {
	client   *jetstream.Client
	handler  DatasetRowsHandler
	config   Config
	registry *events.Registry
	// bind is injectable for tests and lets the delivery loop recreate a pull
	// subscription after a transient NATS disconnect. A durable JetStream
	// consumer remains intact; only the local subscription is rebound.
	bind func(context.Context) (pullConsumer, error)
}

type pullConsumer interface {
	Fetch(context.Context, int) ([]*jetstream.Delivery, error)
	Close() error
}

func New(client *jetstream.Client, handler DatasetRowsHandler, config Config) (*Consumer, error) {
	if client == nil {
		return nil, errors.New("eventbus client is required")
	}
	if handler == nil {
		return nil, errors.New("storage view dataset rows handler is required")
	}
	config, err := config.withDefaults()
	if err != nil {
		return nil, err
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	consumer := &Consumer{client: client, handler: handler, config: config, registry: registry}
	consumer.bind = consumer.bindPullConsumer
	return consumer, nil
}

func (c *Consumer) bindPullConsumer(ctx context.Context) (pullConsumer, error) {
	if c == nil || c.client == nil || c.registry == nil {
		return nil, errors.New("storage view event consumer is not initialized")
	}
	opts := c.config
	return events.NewConsumer(ctx, c.client, c.registry, events.ConsumerConfig{
		Name:                opts.Consumer,
		Event:               events.DatasetRowsUpserted,
		AckWait:             time.Duration(opts.AckWaitMS) * time.Millisecond,
		MaxDeliver:          -1,
		MaxAckPending:       opts.MaxAckPending,
		FetchMaxWait:        time.Second,
		DeliverPolicy:       nats.DeliverAllPolicy,
		DeliverDecodeErrors: true,
	})
}

func (c *Consumer) Start(ctx context.Context) (func(), error) {
	if c == nil {
		return nil, errors.New("storage view event consumer is nil")
	}
	if ctx == nil {
		return nil, errors.New("storage view consumer context is required")
	}
	opts := c.config
	if c.bind == nil {
		c.bind = c.bindPullConsumer
	}
	bound, err := c.bind(ctx)
	if err != nil {
		return nil, err
	}
	opts.Metrics.SetConsumerBound(true)
	loopCtx, cancel := context.WithCancel(ctx)
	dispatcher := newSubjectDispatcher(loopCtx, opts.MaxWorkers, opts.MaxAckPending, func(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat) error {
		if opts.BeforeProcess != nil {
			if err := opts.BeforeProcess(ctx, delivery); err != nil {
				return err
			}
		}
		return c.processDeliveryWithPolicy(ctx, delivery, heartbeat, opts.MaxRetryAttempts)
	}, opts.ErrorReporter, subjectDispatcherMetricsHooks{
		newHeartbeat: func(ctx context.Context, delivery *jetstream.Delivery) *deliveryHeartbeat {
			return newDeliveryHeartbeat(ctx, delivery, deliveryHeartbeatInterval(time.Duration(opts.AckWaitMS)*time.Millisecond), opts.Metrics)
		},
		onSubmit: func(delivery *jetstream.Delivery) {
			opts.Metrics.ObserveLaneSubmit()
			opts.Metrics.ObservePendingDelivery(delivery, time.Now().UTC())
		},
		onStart: func(*jetstream.Delivery) { opts.Metrics.IncLaneActive() },
		onFinish: func(delivery *jetstream.Delivery) {
			opts.Metrics.DecLaneActive()
			opts.Metrics.AddConsumerLagMessages(-1)
			opts.Metrics.CompletePendingDelivery(delivery, time.Now().UTC())
		},
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer opts.Metrics.SetConsumerBound(false)
		defer func() {
			if bound != nil {
				_ = bound.Close()
			}
		}()
		defer dispatcher.Close()
		for loopCtx.Err() == nil {
			deliveries, fetchErr := bound.Fetch(loopCtx, opts.FetchBatch)
			opts.Metrics.AddConsumerLagMessages(int64(len(deliveries)))
			for _, delivery := range deliveries {
				opts.Metrics.ObservePendingDelivery(delivery, time.Now().UTC())
				if err := dispatcher.Dispatch(delivery); err != nil {
					opts.Metrics.AddConsumerLagMessages(-1)
					opts.Metrics.CompletePendingDelivery(delivery, time.Now().UTC())
					if loopCtx.Err() == nil {
						opts.ErrorReporter.Report(fmt.Errorf("dispatch storage view delivery: %w", err))
					}
				}
			}
			if fetchErr != nil {
				if loopCtx.Err() != nil {
					return
				}
				if !errors.Is(fetchErr, nats.ErrTimeout) {
					opts.Metrics.SetConsumerBound(false)
					opts.ErrorReporter.Report(fmt.Errorf("fetch storage view deliveries: %w", fetchErr))
					if shouldRebind(fetchErr) {
						if bound != nil {
							_ = bound.Close()
							bound = nil
						}
						bound = c.rebind(loopCtx, opts)
						if bound == nil {
							return
						}
						opts.Metrics.SetConsumerBound(true)
					}
				}
				timer := time.NewTimer(250 * time.Millisecond)
				select {
				case <-timer.C:
				case <-loopCtx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return
				}
				continue
			}
			opts.Metrics.SetConsumerBound(true)
		}
	}()
	return func() { cancel(); <-done }, nil
}

const (
	rebindRetryDelay   = time.Second
	rebindAttemptLimit = 5 * time.Second
)

func shouldRebind(err error) bool {
	return errors.Is(err, nats.ErrFetchDisconnected) ||
		errors.Is(err, nats.ErrDisconnected) ||
		errors.Is(err, nats.ErrConnectionClosed) ||
		errors.Is(err, nats.ErrBadSubscription)
}

func (c *Consumer) rebind(ctx context.Context, opts Config) pullConsumer {
	for ctx.Err() == nil {
		attemptCtx, cancel := context.WithTimeout(ctx, rebindAttemptLimit)
		bound, err := c.bind(attemptCtx)
		cancel()
		if err == nil {
			return bound
		}
		if opts.ErrorReporter != nil {
			opts.ErrorReporter.Report(fmt.Errorf("rebind storage view consumer: %w", err))
		}
		timer := time.NewTimer(rebindRetryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		}
	}
	return nil
}
