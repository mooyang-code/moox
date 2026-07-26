package eventconsumer

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/log"
)

const ConsumerName = "monitor_hostmetrics_ingest_v1"

type Consumer struct {
	pull  *events.Consumer
	store *hostmetrics.Store
}

func Bind(ctx context.Context, client *jetstream.Client, store *hostmetrics.Store) (*Consumer, error) {
	if client == nil || store == nil {
		return nil, errors.New("host metrics client and store are required")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	pull, err := events.NewConsumer(ctx, client, registry, events.ConsumerConfig{
		Name: ConsumerName, Event: events.MetricsHostReported,
		AckWait: time.Minute, MaxDeliver: 3, MaxAckPending: 256,
		FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
		DeliverDecodeErrors: true,
	})
	if err != nil {
		return nil, err
	}
	return &Consumer{pull: pull, store: store}, nil
}

func (c *Consumer) Close() error {
	if c == nil || c.pull == nil {
		return nil
	}
	return c.pull.Close()
}

func (c *Consumer) Run(ctx context.Context) error {
	if c == nil || c.pull == nil || c.store == nil {
		return errors.New("host metrics consumer is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if !c.store.StorageReady() {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}
		runner := jetstream.NewRunner(c.pull, c, jetstream.RunnerConfig{
			BatchSize: 64,
			ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
				if ctx.Err() == nil {
					log.WarnContextf(ctx, "monitor host metrics delivery failed: %v", err)
				}
			}),
		})
		if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		return nil
	}
}

func (c *Consumer) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: errors.New("host metrics delivery is nil")}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	decoded := events.DecodeDelivery(registry, delivery)
	metric, err := hostmetrics.ValidateMessage(decoded.Message)
	if decoded.Err != nil {
		err = decoded.Err
	}
	if err != nil {
		log.WarnContextf(ctx, "component=monitor_hostmetrics consumer=%s event_id=%s subject=%s delivery_count=%d decision=term reason=%v",
			ConsumerName, delivery.RawMessageID, delivery.Subject, delivery.DeliveryCount, err)
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	if err := c.store.Persist(ctx, decoded.Message, metric); err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: retryDelay(delivery.DeliveryCount), Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
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
