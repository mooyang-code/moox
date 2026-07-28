package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"gorm.io/gorm"
)

type TargetOptions struct {
	Client       *jetstream.Client
	ConsumerName string
	Store        *store.Store
	Wake         func()
	Now          func() time.Time
}

func HandleTarget(
	ctx context.Context,
	delivery *jetstream.Delivery,
	opts TargetOptions,
) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{
			Decision: jetstream.TERM,
			Err:      jetstream.ErrInvalidDelivery,
		}
	}
	if opts.Store == nil {
		return retryTarget(store.ErrInvalidRecord)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return retryTarget(err)
	}
	message, payload, err := events.DecodeRaw(
		registry,
		delivery.RawData,
		delivery.Subject,
		delivery.RawMessageID,
		delivery.ContentType,
	)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	request, ok := payload.(*tradeeventpb.TargetIntent)
	if !ok || message.GetEventName() != events.TradeTargetRequested.Name() ||
		message.GetEventVersion() != events.TradeTargetRequested.Version() {
		return jetstream.HandlerResult{
			Decision: jetstream.TERM,
			Err:      fmt.Errorf("trade target: unexpected event payload %T", payload),
		}
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now()
	}
	intent, err := targetIntent(request)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	if err := intent.Validate(now); err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	if message.GetEventId() != intent.ExecutionID ||
		message.GetSubjectId() != intent.ExecutionBindingID {
		return jetstream.HandlerResult{
			Decision: jetstream.TERM,
			Err:      fmt.Errorf("%w: envelope identity mismatch", execution.ErrInvalidTarget),
		}
	}
	account, err := opts.Store.GetExchangeAccountByID(ctx, intent.ExchangeAccountID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
		}
		return retryTarget(err)
	}
	if account.SpaceID != message.GetSpaceId() || account.Status != "ENABLED" {
		return jetstream.HandlerResult{
			Decision: jetstream.TERM,
			Err:      fmt.Errorf("%w: unsupported Exchange account", execution.ErrInvalidTarget),
		}
	}
	positions := make([]store.TargetPosition, 0, len(intent.Targets))
	for _, target := range intent.Targets {
		if exchange.MarketType(account.MarketType) == exchange.MarketTypeSpot &&
			target.TargetQuantity.IsNegative() {
			return jetstream.HandlerResult{
				Decision: jetstream.TERM,
				Err:      fmt.Errorf("%w: negative SPOT target", execution.ErrInvalidTarget),
			}
		}
		instrument, instrumentErr := opts.Store.GetInstrument(
			ctx,
			account.Exchange,
			account.MarketType,
			target.Symbol,
		)
		if instrumentErr != nil {
			if errors.Is(instrumentErr, gorm.ErrRecordNotFound) {
				if !account.Ready {
					return retryTarget(instrumentErr)
				}
				return jetstream.HandlerResult{
					Decision: jetstream.TERM,
					Err:      instrumentErr,
				}
			}
			return retryTarget(instrumentErr)
		}
		if instrument.InstrumentID != target.InstrumentID ||
			(instrument.Status != "TRADING" && instrument.Status != "live") {
			return jetstream.HandlerResult{
				Decision: jetstream.TERM,
				Err:      fmt.Errorf("%w: unsupported instrument %s", execution.ErrInvalidTarget, target.Symbol),
			}
		}
		positions = append(positions, store.TargetPosition{
			InstrumentID: target.InstrumentID,
			Symbol:       target.Symbol, TargetQuantity: target.TargetQuantity.String(),
		})
	}
	accepted, err := opts.Store.AcceptTarget(ctx, store.TargetExecutionRecord{
		SpaceID: message.GetSpaceId(), ExecutionID: intent.ExecutionID,
		EventID: message.GetEventId(), StrategyRunID: intent.StrategyRunID,
		ExecutionBindingID: intent.ExecutionBindingID,
		ExchangeAccountID:  intent.ExchangeAccountID,
		CommandSequence:    intent.CommandSequence,
		NotAfter:           intent.NotAfter.UnixMilli(), DataRevision: intent.DataRevision,
		Targets: positions, Status: "RUNNING",
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidRecord) || errors.Is(err, store.ErrConflict) {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
		}
		return retryTarget(err)
	}
	if accepted && opts.Wake != nil {
		opts.Wake()
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func targetIntent(request *tradeeventpb.TargetIntent) (execution.TargetIntent, error) {
	if request == nil {
		return execution.TargetIntent{}, execution.ErrInvalidTarget
	}
	intent := execution.TargetIntent{
		ExecutionID: request.GetExecutionId(), StrategyRunID: request.GetStrategyRunId(),
		ExecutionBindingID: request.GetExecutionBindingId(),
		ExchangeAccountID:  request.GetExchangeAccountId(),
		CommandSequence:    request.GetCommandSequence(),
		NotAfter:           time.UnixMilli(request.GetNotAfterUnixMs()),
		DataRevision:       request.GetDataRevision(),
		Targets:            make([]execution.Target, 0, len(request.GetTargets())),
	}
	for _, target := range request.GetTargets() {
		if target == nil {
			return execution.TargetIntent{}, execution.ErrInvalidTarget
		}
		quantity, err := shared.ParseDecimal(target.GetTargetQuantity())
		if err != nil {
			return execution.TargetIntent{}, err
		}
		intent.Targets = append(intent.Targets, execution.Target{
			InstrumentID: target.GetInstrumentId(),
			Symbol:       target.GetSymbol(), TargetQuantity: quantity,
		})
	}
	return intent, nil
}

func retryTarget(err error) jetstream.HandlerResult {
	return jetstream.HandlerResult{
		Decision: jetstream.RETRY,
		Delay:    time.Second,
		Err:      err,
	}
}
