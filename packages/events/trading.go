package events

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/packages/events/tradingpb"
	"github.com/mooyang-code/moox/packages/jetstream"
)

// PublishTradingSignal publishes a strategy recommendation with symbol as
// the routing subject. Execution status and exchange fills are separate
// Trade-domain facts, not fields on this event.
func (p *Publisher) PublishTradingSignal(ctx context.Context, signal *tradingpb.TradingSignal, opts PublishOptions) (*jetstream.PublishAck, error) {
	if err := ValidateTradingSignal(signal); err != nil {
		return nil, err
	}
	if opts.SubjectID != "" && opts.SubjectID != signal.GetSymbol() {
		return nil, fmt.Errorf("trading signal subject_id %q does not match symbol %q", opts.SubjectID, signal.GetSymbol())
	}
	opts.SubjectID = signal.GetSymbol()
	return p.Publish(ctx, TradingSignal, signal, opts)
}

func ValidateTradingSignal(signal *tradingpb.TradingSignal) error {
	if signal == nil {
		return fmt.Errorf("trading signal is nil")
	}
	for name, value := range map[string]string{"strategy_id": signal.GetStrategyId(), "signal_id": signal.GetSignalId(), "symbol": signal.GetSymbol()} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("trading signal %s is required", name)
		}
	}
	if signal.GetSide() == tradingpb.SignalSide_SIGNAL_SIDE_UNSPECIFIED {
		return fmt.Errorf("trading signal side is required")
	}
	switch signal.GetAction() {
	case tradingpb.SignalAction_SIGNAL_ACTION_OPEN,
		tradingpb.SignalAction_SIGNAL_ACTION_CLOSE,
		tradingpb.SignalAction_SIGNAL_ACTION_INCREASE,
		tradingpb.SignalAction_SIGNAL_ACTION_DECREASE:
	default:
		return fmt.Errorf("trading signal action must be OPEN, CLOSE, INCREASE, or DECREASE")
	}
	if signal.GetSignalTime() == nil {
		return fmt.Errorf("trading signal signal_time is required")
	}
	if err := signal.GetSignalTime().CheckValid(); err != nil {
		return fmt.Errorf("trading signal signal_time: %w", err)
	}
	return nil
}
