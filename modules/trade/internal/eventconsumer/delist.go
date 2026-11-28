package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	delistapp "github.com/mooyang-code/moox/modules/trade/internal/application/delist"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/log"
)

// delistRetryDelay 是下架事件写库失败后的重投延迟。
const delistRetryDelay = 5 * time.Second

// DelistIntake 处理一条下架事件（*delistapp.Service 实现）。
type DelistIntake interface {
	Intake(context.Context, delistapp.Event) (delistapp.IntakeResult, error)
}

// DelistOptions 是下架事件消费者的参数。
type DelistOptions struct {
	Client       *jetstream.Client
	ConsumerName string
	Delist       DelistIntake
	// State 记下消费者从何时起没在运行，供健康详情与关注项读；可以为空。
	State *RunState
}

// RunState 记录一个消费者是否在运行、从何时起处于当前状态。
type RunState struct {
	mu      sync.Mutex
	running bool
	since   time.Time
}

// NewRunState 返回一个从 now 起“没在运行”的状态。
func NewRunState(now time.Time) *RunState { return &RunState{since: now} }

// Set 改变运行状态；状态没变时不刷新起始时间。
func (s *RunState) Set(running bool, now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running != running {
		s.running, s.since = running, now
	}
}

// Status 返回是否在运行以及从何时起处于这个状态。
func (s *RunState) Status() (running bool, since time.Time) {
	if s == nil {
		return false, time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.since
}

// RunDelist 消费 Collector 发布的下架事件，串行处理。不受暂停限制：写名单是保护动作，暂停期间照常写。
// 它不计入 Trade 的 EventBus 就绪：配错不能拖住目标执行与恢复追赶，只上报关注项。
func RunDelist(ctx context.Context, opts DelistOptions) error {
	defer opts.State.Set(false, time.Now())
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	for ctx.Err() == nil {
		consumer, openErr := events.NewConsumer(ctx, opts.Client, registry, events.ConsumerConfig{
			Consumer: events.TradeDelistConsumer.WithName(opts.ConsumerName),
			// Collector 可能先于 Trade 发布（全新部署），从头读起。
			AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 1,
			FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
			DeliverDecodeErrors: true,
		})
		if openErr != nil {
			log.WarnContextf(ctx, "open trade delist consumer: error_code=%s", transportErrorCode(openErr))
			if !sleepContext(ctx, time.Second) {
				return ctx.Err()
			}
			continue
		}
		opts.State.Set(true, time.Now())
		handler := jetstream.DeliveryHandlerFunc(func(handlerCtx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
			return HandleDelist(handlerCtx, delivery, opts.Delist)
		})
		runner := jetstream.NewRunner(consumer, handler, jetstream.RunnerConfig{
			BatchSize: 1,
			ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
				log.WarnContextf(ctx, "trade delist delivery failed: error_code=%s", transportErrorCode(err))
			}),
		})
		runErr := runner.Run(ctx)
		opts.State.Set(false, time.Now())
		_ = consumer.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if runErr != nil {
			log.WarnContextf(ctx, "trade delist consumer stopped: error_code=%s", transportErrorCode(runErr))
		}
		if !sleepContext(ctx, time.Second) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

// HandleDelist 处理一条下架事件：解码或校验失败 TERM；写库失败延迟 5 秒重投；其余 ACK。
func HandleDelist(ctx context.Context, delivery *jetstream.Delivery, intake DelistIntake) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	if intake == nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: delistRetryDelay, Err: delistapp.ErrServiceConfig}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: delistRetryDelay, Err: err}
	}
	message, payload, err := events.DecodeRaw(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	scheduled, ok := payload.(*storagepb.InstrumentDelistScheduled)
	if !ok || message.GetEventName() != events.InstrumentDelistScheduled.Name() {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("trade delist: unexpected event payload %T", payload)}
	}
	_, err = intake.Intake(ctx, delistapp.Event{
		EventID: message.GetEventId(), OccurredAt: time.UnixMilli(message.GetOccurredAt()).UTC(),
		Key: delistapp.Key{
			Exchange: scheduled.GetExchange(), MarketType: scheduled.GetMarket(), ExchangeSymbol: scheduled.GetExchangeSymbol(),
		},
		Scope: scheduled.GetScope(), DelistAt: time.UnixMilli(scheduled.GetDelistAt()).UTC(), Source: scheduled.GetSource(),
	})
	switch {
	case errors.Is(err, delistapp.ErrInvalidRequest):
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	case err != nil:
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: delistRetryDelay, Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}
