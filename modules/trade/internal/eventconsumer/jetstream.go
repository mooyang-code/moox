package eventconsumer

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/log"
)

// targetBatchSize 是目标消费者同时处理的条数上限，消费者按并发模式运行：每条消息各自处理，一条处理完就腾出名额。
// 按批顺序处理时，同批里一条返回重试，其余几条会不经处理按同样的延迟退回；即使每次只拉一条，服务端也优先投递到期的重投，
// 一个组合每次都要一秒以上才失败（等组合锁、取价超时等）时，到期后马上又被投回来占住唯一的处理位置，两个这样的组合就能把
// 其他组合的目标拖到过期。并发处理最多容忍 3 个组合同时慢失败，其余组合照常接收。同一组合的处理在组合锁内串行，
// 先后到达的不同 K 线目标由落库前的快速检查和存储层的旧目标拒绝挡住，不会让旧目标覆盖新目标。
const targetBatchSize = 4

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
			Name: opts.ConsumerName, Event: events.LogicalAccountTargetWeightRequested,
			AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 64,
			FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
			DeliverDecodeErrors: true,
		})
		if openErr != nil {
			log.WarnContextf(ctx, "open trade target consumer: error_code=%s", transportErrorCode(openErr))
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
			BatchSize:      targetBatchSize,
			Concurrent:     true,
			ActionReporter: targetActionReporter{},
			ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
				log.WarnContextf(ctx, "trade target delivery failed: error_code=%s", transportErrorCode(err))
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
			log.WarnContextf(ctx, "trade target consumer stopped: error_code=%s", transportErrorCode(runErr))
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
