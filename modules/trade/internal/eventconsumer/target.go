package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
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
	SetReady     func(bool)
	Gate         sync.Locker
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
	_, _, err = (targetapp.Submission{
		Store: opts.Store,
		Wake:  opts.Wake,
		Now:   opts.Now,
		Gate:  opts.Gate,
	}).Accept(ctx, message.GetSpaceId(), intent)
	if err != nil {
		if errors.Is(err, store.ErrInvalidRecord) ||
			errors.Is(err, store.ErrConflict) ||
			errors.Is(err, execution.ErrInvalidTarget) {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			account, accountErr := opts.Store.GetExchangeAccountByID(
				ctx,
				intent.ExchangeAccountID,
			)
			if accountErr != nil || account.Ready {
				return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
			}
		}
		return retryTarget(err)
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
