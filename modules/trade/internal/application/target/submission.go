package target

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

type Submission struct {
	Store *store.Store
	Wake  func()
	Now   func() time.Time
}

func (s Submission) Accept(
	ctx context.Context,
	spaceID string,
	intent execution.TargetIntent,
) (store.TargetExecutionRecord, bool, error) {
	if s.Store == nil {
		return store.TargetExecutionRecord{}, false, ErrExecutorConfig
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now()
	}
	if err := intent.Validate(now); err != nil {
		return store.TargetExecutionRecord{}, false, err
	}
	account, err := s.Store.GetExchangeAccountByID(ctx, intent.ExchangeAccountID)
	if err != nil {
		return store.TargetExecutionRecord{}, false, err
	}
	if account.SpaceID != spaceID || account.Status != "ENABLED" {
		return store.TargetExecutionRecord{}, false, fmt.Errorf(
			"%w: unsupported Exchange account",
			execution.ErrInvalidTarget,
		)
	}
	positions := make([]store.TargetPosition, 0, len(intent.Targets))
	for _, target := range intent.Targets {
		if exchange.MarketType(account.MarketType) == exchange.MarketTypeSpot &&
			target.TargetQuantity.IsNegative() {
			return store.TargetExecutionRecord{}, false, fmt.Errorf(
				"%w: negative SPOT target",
				execution.ErrInvalidTarget,
			)
		}
		instrument, instrumentErr := s.Store.GetInstrument(
			ctx,
			account.Exchange,
			account.MarketType,
			target.Symbol,
		)
		if instrumentErr != nil {
			if errors.Is(instrumentErr, gorm.ErrRecordNotFound) && !account.Ready {
				return store.TargetExecutionRecord{}, false, instrumentErr
			}
			return store.TargetExecutionRecord{}, false, instrumentErr
		}
		if instrument.InstrumentID != target.InstrumentID ||
			(instrument.Status != "TRADING" && instrument.Status != "live") {
			return store.TargetExecutionRecord{}, false, fmt.Errorf(
				"%w: unsupported instrument %s",
				execution.ErrInvalidTarget,
				target.Symbol,
			)
		}
		positions = append(positions, store.TargetPosition{
			InstrumentID: target.InstrumentID,
			Symbol:       target.Symbol, TargetQuantity: target.TargetQuantity.String(),
		})
	}
	record := store.TargetExecutionRecord{
		SpaceID: spaceID, ExecutionID: intent.ExecutionID,
		EventID: intent.ExecutionID, StrategyRunID: intent.StrategyRunID,
		ExecutionBindingID: intent.ExecutionBindingID,
		ExchangeAccountID:  intent.ExchangeAccountID,
		CommandSequence:    intent.CommandSequence,
		NotAfter:           intent.NotAfter.UnixMilli(), DataRevision: intent.DataRevision,
		Targets: positions, Status: StatusRunning,
	}
	accepted, err := s.Store.AcceptTarget(ctx, record)
	if err != nil {
		return store.TargetExecutionRecord{}, false, err
	}
	if accepted && s.Wake != nil {
		s.Wake()
	}
	if !accepted {
		current, getErr := s.Store.GetTargetExecution(ctx, spaceID, intent.ExecutionID)
		if getErr == nil {
			return current, false, nil
		}
	}
	current, err := s.Store.GetTargetExecutionByBinding(
		ctx,
		spaceID,
		intent.ExecutionBindingID,
	)
	return current, accepted, err
}
