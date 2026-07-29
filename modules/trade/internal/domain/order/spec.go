package order

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var ErrInvalidSpec = errors.New("trade: invalid order specification")

type ClientOrderSpec struct {
	ExchangeAccountID string
	ClientOrderID     string
	InstrumentID      string
	Side              exchange.Side
	PositionSide      exchange.PositionSide
	Type              exchange.OrderType
	FillPolicy        exchange.FillPolicy
	Quantity          shared.Decimal
	LimitPrice        *shared.Decimal
}

type OwnerType string

const (
	OwnerTarget   OwnerType = "TARGET"
	OwnerOperator OwnerType = "OPERATOR"
	OwnerExternal OwnerType = "EXTERNAL"
)

type OrderOwner struct {
	Type             OwnerType
	OwnerID          string
	LogicalAccountID string
	RunnerID         *string
}

func (o OrderOwner) Validate() error {
	if strings.TrimSpace(string(o.Type)) == "" ||
		strings.TrimSpace(o.OwnerID) == "" {
		return invalidSpec("missing order ownership")
	}
	switch o.Type {
	case OwnerTarget:
		if strings.TrimSpace(o.LogicalAccountID) == "" ||
			o.RunnerID == nil || strings.TrimSpace(*o.RunnerID) == "" {
			return invalidSpec("TARGET requires logical account and runner ownership")
		}
	case OwnerOperator:
		if strings.TrimSpace(o.LogicalAccountID) == "" || o.RunnerID != nil {
			return invalidSpec("OPERATOR requires logical account without runner")
		}
	case OwnerExternal:
		if o.RunnerID != nil {
			return invalidSpec("EXTERNAL cannot have runner ownership")
		}
	default:
		return invalidSpec("unsupported order owner type")
	}
	return nil
}

type OrderSpec struct {
	ClientOrderSpec
	ReferencePrice     shared.Decimal
	ReferencePriceAt   time.Time
	ReducePositionOnly bool
	Owner              OrderOwner
}

func (s OrderSpec) Validate(
	market exchange.MarketType,
	now time.Time,
	maxReferenceAge time.Duration,
) error {
	if strings.TrimSpace(s.ExchangeAccountID) == "" ||
		strings.TrimSpace(s.ClientOrderID) == "" ||
		strings.TrimSpace(s.InstrumentID) == "" {
		return invalidSpec("missing identity")
	}
	if err := s.Owner.Validate(); err != nil {
		return err
	}
	if !market.Valid() {
		return invalidSpec("unsupported market type")
	}
	if !s.Side.Valid() {
		return invalidSpec("unsupported side")
	}
	if s.Quantity.Cmp(shared.Zero()) <= 0 {
		return invalidSpec("quantity must be positive")
	}
	if s.ReferencePrice.Cmp(shared.Zero()) <= 0 {
		return invalidSpec("reference price must be positive")
	}
	age := now.Sub(s.ReferencePriceAt)
	if maxReferenceAge <= 0 || s.ReferencePriceAt.IsZero() || age < 0 || age > maxReferenceAge {
		return invalidSpec("reference price is stale")
	}

	switch s.Type {
	case exchange.OrderTypeMarket:
		if s.LimitPrice != nil || s.FillPolicy != exchange.FillPolicyUnspecified {
			return invalidSpec("MARKET cannot have limit price or time in force")
		}
	case exchange.OrderTypeLimit:
		if s.LimitPrice == nil || s.LimitPrice.Cmp(shared.Zero()) <= 0 {
			return invalidSpec("LIMIT requires a positive limit price")
		}
		if !s.FillPolicy.ValidForLimit() {
			return invalidSpec("LIMIT requires GTC, IOC, or FOK")
		}
	default:
		return invalidSpec("unsupported order type")
	}

	switch market {
	case exchange.MarketTypeSpot:
		if s.PositionSide != exchange.PositionSideUnspecified || s.ReducePositionOnly {
			return invalidSpec("SPOT cannot have position side or reduce-only")
		}
	case exchange.MarketTypeSwap:
		if s.PositionSide != exchange.PositionSideNet {
			return invalidSpec("SWAP requires NET position side")
		}
	}
	return nil
}

func invalidSpec(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidSpec, reason)
}
