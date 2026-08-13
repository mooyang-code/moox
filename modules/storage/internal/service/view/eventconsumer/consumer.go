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

// A View event must stay pending until its row/marker application succeeds.
// Operators may still set a positive value for a bounded emergency policy.
const defaultMaxRetryAttempts = -1

const reconnectAfterSubscriptionErrors = 3

type Config struct {
	Consumer         string
	AckWaitMS        int
	FetchBatch       int
	MaxWorkers       int
	MaxAckPending    int
	Ordering         string
	DeliverPolicy    string
	MaxRetryAttempts int
	ErrorReporter    jetstream.ErrorReporter
	Metrics          *observability.ViewMetrics
	BeforeProcess    func(context.Context, *jetstream.Delivery) error
	Lease            DeliveryLease
}

func (c Config) withDefaults() (Config, error) {
	if strings.TrimSpace(c.Consumer) == "" {
		c.Consumer = "storage_view_period_v1"
	}
	if c.AckWaitMS == 0 {
		c.AckWaitMS = 120000
	}
	if c.FetchBatch == 0 {
		c.FetchBatch = 1
	}
	if c.MaxAckPending == 0 {
		c.MaxAckPending = c.FetchBatch
	}
	if c.MaxWorkers == 0 {
		c.MaxWorkers = 1
	}
	if c.MaxRetryAttempts == 0 {
		// A View apply must not be TERM'ed after a short transient outage:
		// MaxAckPending=1 is the ordering fence and the message remains pending
		// until the row/marker application succeeds. Positive values are kept as
		// an explicit operator override for tests and emergency recovery.
		c.MaxRetryAttempts = -1
	}
	c.Consumer = strings.TrimSpace(c.Consumer)
	if strings.TrimSpace(c.Ordering) == "" {
		c.Ordering = "dataset"
	}
	c.Ordering = strings.ToLower(strings.TrimSpace(c.Ordering))
	if c.FetchBatch < 1 {
		return c, errors.New("storage view fetch_batch must be positive")
	}
	if c.MaxWorkers < 1 {
		return c, errors.New("storage view max_workers must be positive")
	}
	if c.MaxRetryAttempts == 0 || c.MaxRetryAttempts < -1 {
		return c, errors.New("storage view max_retry_attempts must be -1 or positive")
	}
	if c.Ordering != "dataset" {
		return c, fmt.Errorf("storage view ordering %q is unsupported; want dataset", c.Ordering)
	}
	c.DeliverPolicy = strings.ToLower(strings.TrimSpace(c.DeliverPolicy))
	if c.DeliverPolicy == "" {
		c.DeliverPolicy = "all"
	}
	if c.DeliverPolicy != "all" && c.DeliverPolicy != "new" {
		return c, fmt.Errorf("storage view deliver_policy %q is unsupported", c.DeliverPolicy)
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
	bind func(context.Context) (deliveryConsumer, error)
	// reconnect is used as a last resort when the NATS connection reports ready
	// but repeatedly returns an invalid pull subscription after EventBus restart.
	reconnect func(context.Context) error
}

type deliveryConsumer interface {
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
	consumer.bind = consumer.bindDelivery
	consumer.reconnect = client.Reconnect
	return consumer, nil
}

func (c *Consumer) bindDelivery(ctx context.Context) (deliveryConsumer, error) {
	if c == nil || c.client == nil || c.registry == nil {
		return nil, errors.New("storage view event consumer is not initialized")
	}
	opts := c.config
	deliverPolicy := nats.DeliverAllPolicy
	if opts.DeliverPolicy == "new" {
		deliverPolicy = nats.DeliverNewPolicy
	}
	return events.NewConsumer(ctx, c.client, c.registry, events.ConsumerConfig{
		Name: opts.Consumer,
		Events: []events.Event{
			events.DatasetRowsUpserted,
			events.DatasetPeriodCollected,
			events.FactorPeriodComputed,
			events.DatasetSyncPoint,
		},
		AckWait:             time.Duration(opts.AckWaitMS) * time.Millisecond,
		MaxDeliver:          -1,
		MaxAckPending:       opts.MaxAckPending,
		FetchMaxWait:        time.Second,
		DeliverPolicy:       deliverPolicy,
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
		c.bind = c.bindDelivery
	}
	bound, err := c.bind(ctx)
	if err != nil {
		return nil, err
	}
	opts.Metrics.SetConsumerBound(true)
	loopCtx, cancel := context.WithCancel(ctx)
	dispatcher := newSubjectDispatcherWithKey(loopCtx, opts.MaxWorkers, opts.MaxAckPending, func(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat) error {
		if opts.BeforeProcess != nil {
			if err := opts.BeforeProcess(ctx, delivery); err != nil {
				return err
			}
		}
		return c.processDeliveryWithPolicy(ctx, delivery, heartbeat, opts.MaxRetryAttempts)
	}, opts.ErrorReporter, func(delivery *jetstream.Delivery) (string, error) {
		return datasetQueueKey(c.registry, delivery)
	}, subjectDispatcherMetricsHooks{
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
		consecutiveSubscriptionErrors := 0
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
				if errors.Is(fetchErr, nats.ErrTimeout) {
					opts.Metrics.SetConsumerBound(true)
				} else {
					opts.Metrics.SetConsumerBound(false)
					if shouldRebind(fetchErr) {
						// Tear down the old pull subscription before replacing the
						// shared NATS connection. Keeping it alive while Reconnect
						// swaps connections can leave the server with the old pull
						// binding and make every newly-created subscription invalid.
						if bound != nil {
							_ = bound.Close()
							bound = nil
						}
						reportRebind := true
						if errors.Is(fetchErr, nats.ErrBadSubscription) {
							consecutiveSubscriptionErrors++
							reportRebind = consecutiveSubscriptionErrors == 1
							if consecutiveSubscriptionErrors >= reconnectAfterSubscriptionErrors {
								reportRebind = true
								if err := c.reconnectClient(loopCtx); err != nil {
									opts.ErrorReporter.Report(fmt.Errorf("reconnect storage view eventbus client: %w", err))
								}
								consecutiveSubscriptionErrors = 0
							}
						} else {
							consecutiveSubscriptionErrors = 0
						}
						if reportRebind {
							opts.ErrorReporter.Report(fmt.Errorf("rebind storage view deliveries: %w", fetchErr))
						}
						bound = c.rebind(loopCtx, opts)
						if bound == nil {
							return
						}
					} else {
						consecutiveSubscriptionErrors = 0
						opts.ErrorReporter.Report(fmt.Errorf("fetch storage view deliveries: %w", fetchErr))
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
			consecutiveSubscriptionErrors = 0
			opts.Metrics.SetConsumerBound(true)
		}
	}()
	return func() { cancel(); <-done }, nil
}

func (c *Consumer) reconnectClient(ctx context.Context) error {
	if c == nil || c.reconnect == nil {
		return errors.New("storage view eventbus client reconnect is unavailable")
	}
	attemptCtx, cancel := context.WithTimeout(ctx, rebindAttemptLimit)
	defer cancel()
	return c.reconnect(attemptCtx)
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

func (c *Consumer) rebind(ctx context.Context, opts Config) deliveryConsumer {
	for ctx.Err() == nil {
		// nats.go can return a subscription object while the underlying
		// connection is still reconnecting. Do not bind that unusable
		// subscription; wait until the shared client reports a live connection.
		if c.client != nil && !c.client.Ready() {
			if !sleepRebind(ctx) {
				return nil
			}
			continue
		}
		attemptCtx, cancel := context.WithTimeout(ctx, rebindAttemptLimit)
		bound, err := c.bind(attemptCtx)
		cancel()
		if err == nil {
			return bound
		}
		if opts.ErrorReporter != nil {
			opts.ErrorReporter.Report(fmt.Errorf("rebind storage view consumer: %w", err))
		}
		if !sleepRebind(ctx) {
			return nil
		}
	}
	return nil
}

func sleepRebind(ctx context.Context) bool {
	timer := time.NewTimer(rebindRetryDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
