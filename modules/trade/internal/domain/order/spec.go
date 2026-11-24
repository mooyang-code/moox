package order

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var ErrInvalidSpec = errors.New("订单参数不合法")

var ErrReferencePriceStale = errors.New("参考价已过期")

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
	InstanceID       *string
}

func (o OrderOwner) Validate() error {
	if strings.TrimSpace(string(o.Type)) == "" ||
		strings.TrimSpace(o.OwnerID) == "" {
		return invalidSpec("缺少订单归属")
	}
	switch o.Type {
	case OwnerTarget:
		if strings.TrimSpace(o.LogicalAccountID) == "" ||
			o.InstanceID == nil || strings.TrimSpace(*o.InstanceID) == "" {
			return invalidSpec("目标单必须带组合账户与策略实例")
		}
	case OwnerOperator:
		if strings.TrimSpace(o.LogicalAccountID) == "" || o.InstanceID != nil {
			return invalidSpec("人工单必须带组合账户、不能带策略实例")
		}
	case OwnerExternal:
		if o.InstanceID != nil {
			return invalidSpec("外部单不能带策略实例")
		}
	case OwnerSystem:
		if strings.TrimSpace(o.LogicalAccountID) == "" || o.InstanceID != nil {
			return invalidSpec("系统单必须带组合账户、不能带策略实例")
		}
	default:
		return invalidSpec("不支持的订单归属类型")
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
		return invalidSpec("缺少执行账户、客户端单号或标的")
	}
	if err := s.Owner.Validate(); err != nil {
		return err
	}
	if !market.Valid() {
		return invalidSpec("不支持的市场类型")
	}
	if !s.Side.Valid() {
		return invalidSpec("不支持的买卖方向")
	}
	if s.Quantity.Cmp(shared.Zero()) <= 0 {
		return invalidSpec("数量必须大于 0")
	}
	if s.ReferencePrice.Cmp(shared.Zero()) <= 0 {
		return invalidSpec("参考价必须大于 0")
	}
	age := now.Sub(s.ReferencePriceAt)
	if maxReferenceAge <= 0 || s.ReferencePriceAt.IsZero() || age < 0 || age > maxReferenceAge {
		return fmt.Errorf("%w: %w", ErrInvalidSpec, ErrReferencePriceStale)
	}

	switch s.Type {
	case exchange.OrderTypeMarket:
		if s.LimitPrice != nil || s.FillPolicy != exchange.FillPolicyUnspecified {
			return invalidSpec("市价单不能带限价或有效方式")
		}
	case exchange.OrderTypeLimit:
		if s.LimitPrice == nil || s.LimitPrice.Cmp(shared.Zero()) <= 0 {
			return invalidSpec("限价单必须带大于 0 的限价")
		}
		if !s.FillPolicy.ValidForLimit() {
			return invalidSpec("限价单的有效方式必须是 GTC、IOC 或 FOK")
		}
	default:
		return invalidSpec("不支持的订单类型")
	}

	if rule := exchange.RuleFor(market); market.Valid() {
		if s.PositionSide != rule.PositionSide {
			return invalidSpec("持仓方向与市场不符")
		}
		if s.ReducePositionOnly && !rule.ReduceOnly {
			return invalidSpec("这个市场不支持只减仓订单")
		}
	}
	return nil
}

func invalidSpec(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidSpec, reason)
}
