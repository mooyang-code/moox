package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/events/tradingpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/log"
)

type tradeRawPullConsumer struct {
	pull *jetstream.PullConsumer
}

func (c tradeRawPullConsumer) Fetch(ctx context.Context, batch int) ([]*jetstream.Delivery, error) {
	return c.pull.Fetch(ctx, batch)
}

func (c tradeRawPullConsumer) Close() error {
	if c.pull == nil {
		return nil
	}
	return c.pull.Close()
}

func runTradingSignalConsumer(ctx context.Context, client *jetstream.Client, cfg config.EventBusConfig, s *store.Store, publishers ...events.MessagePublisher) {
	var publisher events.MessagePublisher
	if len(publishers) > 0 {
		publisher = publishers[0]
	}
	for ctx.Err() == nil {
		pull, err := client.BindManagedPullConsumer(ctx, jetstream.ConsumerBindRef{
			Stream:              cfg.Stream,
			Durable:             cfg.TradingSignalDurable,
			FetchMaxWait:        time.Second,
			DeliverDecodeErrors: true,
		})
		if err != nil {
			log.WarnContextf(ctx, "bind trade trading signal consumer: %v", err)
			time.Sleep(time.Second)
			continue
		}

		consumer := tradeRawPullConsumer{pull: pull}
		runner := jetstream.NewRunner(consumer, jetstream.DeliveryHandlerFunc(func(handlerCtx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
			return withTradeDLQ(jetstream.DeliveryHandlerFunc(func(handlerCtx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
				return handleTradingSignalDelivery(handlerCtx, delivery, s, cfg.TradingSignalDurable)
			}), publisher)(handlerCtx, delivery)
		}), jetstream.RunnerConfig{
			BatchSize: 64,
			ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
				log.WarnContextf(ctx, "trade trading signal delivery failed: %v", err)
			}),
		})
		err = runner.Run(ctx)
		_ = consumer.Close()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.WarnContextf(ctx, "trade trading signal consumer stopped: %v", err)
		}
		time.Sleep(time.Second)
	}
}

func handleTradingSignalDelivery(ctx context.Context, delivery *jetstream.Delivery, s *store.Store, consumerName string) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	if delivery.DecodeError != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: delivery.DecodeError}
	}
	if s == nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: fmt.Errorf("trade signal store is nil")}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	message, signal, err := events.DecodeTradingSignalWithContentType(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	record, err := tradingSignalRecord(message, signal, time.Now().UTC())
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	if _, err := s.RecordTradingSignal(ctx, consumerName, message.GetEventId(), delivery.Subject, record); err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func tradingSignalRecord(message *eventpb.EventMessage, signal *tradingpb.TradingSignal, receivedAt time.Time) (store.TradingSignalRecord, error) {
	if message == nil || signal == nil {
		return store.TradingSignalRecord{}, fmt.Errorf("trade signal message and payload are required")
	}
	if message.GetOccurredAt() == nil {
		return store.TradingSignalRecord{}, fmt.Errorf("trade signal occurred_at is required")
	}
	if signal.GetSignalTime() == nil {
		return store.TradingSignalRecord{}, fmt.Errorf("trade signal signal_time is required")
	}
	tags, err := json.Marshal(signal.GetTags())
	if err != nil {
		return store.TradingSignalRecord{}, fmt.Errorf("marshal trade signal tags: %w", err)
	}
	return store.TradingSignalRecord{
		SpaceID:         message.GetSpaceId(),
		EventID:         message.GetEventId(),
		SignalID:        signal.GetSignalId(),
		StrategyID:      signal.GetStrategyId(),
		Symbol:          signal.GetSymbol(),
		Side:            signal.GetSide().String(),
		Action:          signal.GetAction().String(),
		TargetPrice:     optionalFloatString(signal.TargetPrice),
		StopLossPrice:   optionalFloatString(signal.StopLossPrice),
		TakeProfitPrice: optionalFloatString(signal.TakeProfitPrice),
		SignalTime:      signal.GetSignalTime().AsTime().UTC(),
		ReceivedAt:      receivedAt.UTC(),
		Tags:            string(tags),
	}, nil
}

func optionalFloatString(value *float64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(*value, 'g', -1, 64)
}
