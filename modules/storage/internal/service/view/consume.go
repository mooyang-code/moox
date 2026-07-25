package view

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

// EventConsumerOptions controls the client-side dispatch policy. MaxAckPending
// is intentionally not sent to JetStream: the eventbus topology owns it and
// this value is only useful for validating a local fetch configuration.
type EventConsumerOptions struct {
	Consumer      string
	AckWaitMS     int
	FetchBatch    int
	MaxWorkers    int
	MaxAckPending int
	Ordering      string
	// MaxRetryAttempts bounds client-side retries for transient event apply
	// failures. A value of zero uses the safe default; the broker topology is
	// still authoritative for the durable's immutable settings.
	MaxRetryAttempts int
	ErrorReporter    jetstream.ErrorReporter
	Metrics          *observability.ViewMetrics
	// BeforeProcess is an optional test/diagnostic hook. Production callers
	// leave it nil; it runs inside the subject queue before event apply work.
	BeforeProcess func(context.Context, *jetstream.Delivery) error
}

const (
	defaultMaxRetryAttempts = 10
)

func (o EventConsumerOptions) withDefaults() (EventConsumerOptions, error) {
	if strings.TrimSpace(o.Consumer) == "" {
		o.Consumer = "storage_view"
	}
	if o.AckWaitMS == 0 {
		o.AckWaitMS = 120000
	}
	if o.FetchBatch == 0 {
		o.FetchBatch = 8
	}
	if o.MaxAckPending == 0 {
		o.MaxAckPending = o.FetchBatch
	}
	if o.MaxWorkers == 0 {
		o.MaxWorkers = 4
	}
	if o.MaxRetryAttempts == 0 {
		// Zero means "use the safe default"; negative values remain invalid
		// and are rejected below instead of being silently corrected.
		o.MaxRetryAttempts = defaultMaxRetryAttempts
	}
	o.Consumer = strings.TrimSpace(o.Consumer)
	if strings.TrimSpace(o.Ordering) == "" {
		o.Ordering = "subject"
	}
	o.Ordering = strings.ToLower(strings.TrimSpace(o.Ordering))
	if o.FetchBatch < 1 {
		return o, errors.New("storage view fetch_batch must be positive")
	}
	if o.MaxWorkers < 1 {
		return o, errors.New("storage view max_workers must be positive")
	}
	if o.MaxRetryAttempts < 1 {
		return o, errors.New("storage view max_retry_attempts must be positive")
	}
	if o.Ordering != "subject" {
		return o, fmt.Errorf("storage view ordering %q is unsupported", o.Ordering)
	}
	if o.MaxAckPending < 0 {
		return o, errors.New("storage view max_ack_pending must not be negative")
	}
	if o.AckWaitMS < 1 {
		return o, errors.New("storage view ack_wait_ms must be positive")
	}
	if o.MaxAckPending > 0 && o.FetchBatch > o.MaxAckPending {
		return o, fmt.Errorf("storage view fetch_batch %d exceeds max_ack_pending %d", o.FetchBatch, o.MaxAckPending)
	}
	return o, nil
}

func (s *Service) StartEventConsumer(ctx context.Context, client *jetstream.Client, configured ...EventConsumerOptions) (func(), error) {
	if s == nil {
		return nil, errors.New("storage view service is nil")
	}
	if client == nil {
		return nil, errors.New("eventbus client is required")
	}
	if ctx == nil {
		return nil, errors.New("storage view consumer context is required")
	}
	opts := EventConsumerOptions{}
	if len(configured) > 0 {
		opts = configured[0]
	}
	var err error
	if opts, err = opts.withDefaults(); err != nil {
		return nil, err
	}
	if opts.Metrics == nil {
		opts.Metrics = s.metrics
	}
	if opts.Metrics == nil {
		opts.Metrics = observability.DefaultViewMetrics
	}
	s.metrics = opts.Metrics
	reporter := opts.ErrorReporter
	if reporter == nil {
		reporter = jetstream.ErrorReporterFunc(func(err error) {
			log.Printf("storage view event consumer error: %v", err)
		})
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	consumer, err := events.NewConsumer(ctx, client, registry, events.ConsumerConfig{
		Name: opts.Consumer, Event: events.DatasetRowsUpserted,
		AckWait:    time.Duration(opts.AckWaitMS) * time.Millisecond,
		MaxDeliver: -1, MaxAckPending: opts.MaxAckPending,
		FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
		DeliverDecodeErrors: true,
	})
	if err != nil {
		return nil, err
	}
	opts.Metrics.SetConsumerBound(true)
	loopCtx, cancel := context.WithCancel(ctx)
	dispatcher := newSubjectDispatcher(loopCtx, opts.MaxWorkers, func(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat) error {
		if opts.BeforeProcess != nil {
			if err := opts.BeforeProcess(ctx, delivery); err != nil {
				return err
			}
		}
		return s.processDeliveryWithPolicy(ctx, delivery, heartbeat, opts.MaxRetryAttempts)
	}, reporter, subjectDispatcherMetricsHooks{
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
		defer consumer.Close()
		defer dispatcher.Close()
		for loopCtx.Err() == nil {
			deliveries, fetchErr := consumer.Fetch(loopCtx, opts.FetchBatch)
			opts.Metrics.AddConsumerLagMessages(int64(len(deliveries)))
			for _, delivery := range deliveries {
				opts.Metrics.ObservePendingDelivery(delivery, time.Now().UTC())
				if err := dispatcher.Dispatch(delivery); err != nil {
					opts.Metrics.AddConsumerLagMessages(-1)
					opts.Metrics.CompletePendingDelivery(delivery, time.Now().UTC())
					if loopCtx.Err() == nil {
						reporter.Report(fmt.Errorf("dispatch storage view delivery: %w", err))
					}
				}
			}
			if fetchErr != nil {
				if loopCtx.Err() != nil {
					return
				}
				if !errors.Is(fetchErr, nats.ErrTimeout) {
					opts.Metrics.SetConsumerBound(false)
					reporter.Report(fmt.Errorf("fetch storage view deliveries: %w", fetchErr))
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
