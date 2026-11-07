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

var ErrReferencePriceStale = errors.New("reference price is stale")

type ClientOrderSpec struct {
	TradingAccountID string
	ClientOrderID    string
	InstrumentID     string
	Side             exchange.Side
	PositionSide     exchange.PositionSide
	Type             exchange.OrderType
	FillPolicy       exchange.FillPolicy
	Quantity         shared.Decimal
	LimitPrice       *shared.Decimal
}

type OwnerType string

const (
	OwnerTarget   OwnerType = "TARGET"
	OwnerOperator OwnerType = "OPERATOR"
	OwnerExternal OwnerType = "EXTERNAL"
	// OwnerSystem 是系统自己发起的订单，例如 BNB 补买和稳定币调平；不暂停组合，也不算本目标的子单。
	OwnerSystem OwnerType = "SYSTEM"
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
	case OwnerSystem:
		if strings.TrimSpace(o.LogicalAccountID) == "" || o.RunnerID != nil {
			return invalidSpec("SYSTEM requires logical account without runner")
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
	// Attempt 是伪市价单的第几次尝试，从 0 开始，写进订单表的 c_attempt；不参与订单身份比较。
	Attempt int
	// PositionBefore 是目标子单下单前该执行账户在这个标的上的持仓，用于发现持仓没反映已成交子单。
	// 只是记录，不参与订单身份比较。
	PositionBefore string
}

func (s OrderSpec) Validate(
	market exchange.MarketType,
	now time.Time,
	maxReferenceAge time.Duration,
) error {
	if strings.TrimSpace(s.TradingAccountID) == "" ||
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
		return fmt.Errorf("%w: %w", ErrInvalidSpec, ErrReferencePriceStale)
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

	if rule := exchange.RuleFor(market); market.Valid() {
		if s.PositionSide != rule.PositionSide {
			return invalidSpec("position side does not match market")
		}
		if s.ReducePositionOnly && !rule.ReduceOnly {
			return invalidSpec("market cannot have reduce-only orders")
		}
	}
	return nil
}

func invalidSpec(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidSpec, reason)
}
