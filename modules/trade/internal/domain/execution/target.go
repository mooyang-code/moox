package execution

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

var ErrInvalidTarget = errors.New("trade: invalid target intent")

type Target struct {
	InstrumentID   string
	Symbol         string
	TargetQuantity shared.Decimal
}

type TargetIntent struct {
	ExecutionID        string
	StrategyRunID      string
	ExecutionBindingID string
	ExchangeAccountID  string
	CommandSequence    uint64
	NotAfter           time.Time
	DataRevision       string
	Targets            []Target
}

func (i TargetIntent) Validate(now time.Time) error {
	if blank(i.ExecutionID) ||
		blank(i.StrategyRunID) ||
		blank(i.ExecutionBindingID) ||
		blank(i.ExchangeAccountID) ||
		i.CommandSequence == 0 ||
		i.NotAfter.IsZero() ||
		!i.NotAfter.After(now) ||
		blank(i.DataRevision) ||
		len(i.Targets) == 0 {
		return invalidTarget("missing or expired intent")
	}
	symbols := make(map[string]struct{}, len(i.Targets))
	for _, target := range i.Targets {
		if blank(target.InstrumentID) || blank(target.Symbol) {
			return invalidTarget("missing target identity")
		}
		if _, duplicate := symbols[target.Symbol]; duplicate {
			return invalidTarget("duplicate target symbol")
		}
		symbols[target.Symbol] = struct{}{}
	}
	return nil
}

func invalidTarget(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidTarget, reason)
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}
