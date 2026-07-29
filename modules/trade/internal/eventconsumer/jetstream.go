package eventconsumer

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/log"
)

func RunTarget(ctx context.Context, opts TargetOptions) error {
	if opts.SetReady != nil {
		opts.SetReady(false)
		defer opts.SetReady(false)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	for ctx.Err() == nil {
		consumer, openErr := events.NewConsumer(ctx, opts.Client, registry, events.ConsumerConfig{
			Name: opts.ConsumerName, Event: events.TradeTargetRequested,
			AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 64,
			FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
			DeliverDecodeErrors: true,
		})
		if openErr != nil {
			log.WarnContextf(ctx, "open trade target consumer: %v", openErr)
			if !sleepContext(ctx, time.Second) {
				return ctx.Err()
			}
			continue
		}
		if opts.SetReady != nil {
			opts.SetReady(true)
		}
		handler := jetstream.DeliveryHandlerFunc(func(handlerCtx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
			return HandleTarget(handlerCtx, delivery, opts)
		})
		runner := jetstream.NewRunner(consumer, handler, jetstream.RunnerConfig{
			BatchSize: 16,
			ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
				log.WarnContextf(ctx, "trade target delivery failed: %v", err)
			}),
		})
		runErr := runner.Run(ctx)
		if opts.SetReady != nil {
			opts.SetReady(false)
		}
		_ = consumer.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if runErr != nil {
			log.WarnContextf(ctx, "trade target consumer stopped: %v", runErr)
		}
		if !sleepContext(ctx, time.Second) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
