package order

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

var (
	ErrInvalidOrder      = errors.New("trade: invalid order")
	ErrInvalidTransition = errors.New("trade: invalid order transition")
)

// SyntheticOrderIDPrefix 是合成外部订单的订单号前缀：交易所查不到原单时，按收到的成交合成的外部订单。
// 订单没有单独的标记列，靠前缀区分，只有它们允许在已成交后继续放大数量（见 ApplySyntheticFill）。
const SyntheticOrderIDPrefix = "synthetic:"

// IsSyntheticOrderID 报告订单号是否属于合成外部订单。
func IsSyntheticOrderID(orderID string) bool {
	return strings.HasPrefix(orderID, SyntheticOrderIDPrefix)
}

type Fill struct {
	ID       shared.FillID
	Quantity shared.Decimal
}

type Order struct {
	ID               shared.OrderID
	Spec             OrderSpec
	ExchangeOrderID  string
	FilledQuantity   shared.Decimal
	AverageFillPrice shared.Decimal
	AppliedFills     map[shared.FillID]shared.Decimal
	State            State
	Version          uint64
}

func New(id shared.OrderID, spec OrderSpec) (*Order, error) {
	if id == "" ||
		strings.TrimSpace(spec.TradingAccountID) == "" ||
		strings.TrimSpace(spec.ClientOrderID) == "" ||
		strings.TrimSpace(spec.InstrumentID) == "" ||
		spec.Quantity.Cmp(shared.Zero()) <= 0 {
		return nil, ErrInvalidOrder
	}
	if err := spec.Owner.Validate(); err != nil {
		return nil, ErrInvalidOrder
	}
	order := &Order{
		ID:           id,
		Spec:         spec,
		AppliedFills: make(map[shared.FillID]shared.Decimal),
		State:        Pending,
	}
	order.bumpVersion()
	return order, nil
}

func (o *Order) BeginSubmit() error {
	return o.transition(Submitting, Pending)
}

func (o *Order) MarkSubmitUnknown() error {
	return o.transition(SubmitUnknown, Submitting)
}

// AbortSubmit is only valid before the adapter call. Unlike ReturnToPending,
// it records that submission is known not to have left the process.
func (o *Order) AbortSubmit() error {
	return o.transition(Pending, Submitting)
}

func (o *Order) ReturnToPending() error {
	return o.transition(Pending, SubmitUnknown)
}

func (o *Order) Acknowledge(exchangeOrderID string) error {
	if strings.TrimSpace(exchangeOrderID) == "" {
		return ErrInvalidTransition
	}
	if o.ExchangeOrderID != "" && o.ExchangeOrderID != exchangeOrderID {
		return fmt.Errorf("%w: conflicting Exchange order ID", ErrInvalidTransition)
	}
	switch o.State {
	case Submitting, SubmitUnknown:
		o.State = Open
	case Open, PartiallyFilled, Filled, Canceling, CancelUnknown:
	default:
		return ErrInvalidTransition
	}
	o.ExchangeOrderID = exchangeOrderID
	o.bumpVersion()
	return nil
}

// Claim 认领：待提交的订单收到了交易所已受理它的证据（成交或撤单快照）。订单转为挂单，
// 记下交易所单号（证据里没带就先不记，由后续订单快照补上），之后按挂单的流程继续。
func (o *Order) Claim(exchangeOrderID string) error {
	if o.State != Pending {
		return ErrInvalidTransition
	}
	exchangeOrderID = strings.TrimSpace(exchangeOrderID)
	if exchangeOrderID != "" {
		if o.ExchangeOrderID != "" && o.ExchangeOrderID != exchangeOrderID {
			return fmt.Errorf("%w: conflicting Exchange order ID", ErrInvalidTransition)
		}
		o.ExchangeOrderID = exchangeOrderID
	}
	o.State = Open
	o.bumpVersion()
	return nil
}

func (o *Order) Reject() error {
	return o.transition(Rejected, Pending, Submitting, SubmitUnknown)
}

func (o *Order) Expire() error {
	return o.transition(
		Expired,
		Pending,
		Submitting,
		SubmitUnknown,
		Open,
		PartiallyFilled,
		Canceling,
		CancelUnknown,
	)
}

// BeginCancel 只接受交易所已受理的挂单。提交结果未知的订单要先解析（查到就认领，查不到退回待提交再丢弃），
// 直接撤单被拒后会得到一个没有交易所单号的挂单。
func (o *Order) BeginCancel() error {
	return o.transition(Canceling, Open, PartiallyFilled)
}

func (o *Order) DiscardPending() error {
	return o.transition(Canceled, Pending)
}

func (o *Order) MarkCancelUnknown() error {
	return o.transition(CancelUnknown, Canceling)
}

func (o *Order) CancelRejected() error {
	to := Open
	if !o.FilledQuantity.IsZero() {
		to = PartiallyFilled
	}
	return o.transition(to, Canceling)
}

func (o *Order) CancelStillOpen() error {
	to := Open
	if !o.FilledQuantity.IsZero() {
		to = PartiallyFilled
	}
	return o.transition(to, CancelUnknown)
}

func (o *Order) ApplyFill(fill Fill) error {
	if fill.ID == "" || fill.Quantity.Cmp(shared.Zero()) <= 0 {
		return ErrInvalidTransition
	}
	if applied, exists := o.AppliedFills[fill.ID]; exists {
		if applied.Cmp(fill.Quantity) != 0 {
			return fmt.Errorf("%w: conflicting duplicate Fill", ErrInvalidTransition)
		}
		return nil
	}
	if !o.State.acceptsFill() {
		return ErrInvalidTransition
	}
	next := o.FilledQuantity.Add(fill.Quantity)
	if next.Cmp(o.Spec.Quantity) > 0 {
		return fmt.Errorf("%w: Fill exceeds order quantity", ErrInvalidTransition)
	}
	if o.AppliedFills == nil {
		o.AppliedFills = make(map[shared.FillID]shared.Decimal)
	}
	o.AppliedFills[fill.ID] = fill.Quantity
	o.FilledQuantity = next
	if next.Cmp(o.Spec.Quantity) == 0 {
		o.State = Filled
	} else if o.State == Canceled || o.State == PartiallyCanceled || o.State == Rejected {
		o.State = PartiallyCanceled
	} else if o.State != Canceling && o.State != CancelUnknown && o.State != Expired {
		o.State = PartiallyFilled
	}
	o.bumpVersion()
	return nil
}

// ApplySyntheticFill 与 ApplyFill 相同，只多一条：合成外部订单收到的成交使已入账成交量超过订单数量时，
// 把订单数量放大到已入账成交量加本笔，状态按新数量重新推导（放大后恰好等于数量，即已成交）。
// 合成订单是交易所查不到原单时按成交合成的，数量只是当时已知成交之和，后到的成交属于同一张单，数量要跟着加；
// 普通订单的数量是下单时定的，超量仍然是非法迁移。
func (o *Order) ApplySyntheticFill(fill Fill) error {
	if fill.ID != "" && fill.Quantity.Cmp(shared.Zero()) > 0 {
		if _, applied := o.AppliedFills[fill.ID]; !applied && (o.State == Filled || o.State.acceptsFill()) {
			if next := o.FilledQuantity.Add(fill.Quantity); next.Cmp(o.Spec.Quantity) > 0 {
				o.Spec.Quantity = next
				if o.State == Filled {
					// 已成交不再接收成交；放大后它又有了未成交的余量，先回到部分成交，再由 ApplyFill 推导。
					o.State = PartiallyFilled
				}
			}
		}
	}
	return o.ApplyFill(fill)
}

func (o *Order) ConfirmCancel() error {
	to := Canceled
	if !o.FilledQuantity.IsZero() {
		to = PartiallyCanceled
	}
	return o.transition(
		to,
		Pending,
		Submitting,
		SubmitUnknown,
		Open,
		PartiallyFilled,
		Canceling,
		CancelUnknown,
	)
}

func (o *Order) transition(to State, allowed ...State) error {
	if o.State.Terminal() {
		return ErrInvalidTransition
	}
	for _, state := range allowed {
		if o.State == state {
			o.State = to
			o.bumpVersion()
			return nil
		}
	}
	return ErrInvalidTransition
}

// bumpVersion 在每次成功的状态变化后把版本号加一：存储层按它做乐观锁（写回时比对旧版本号）。
func (o *Order) bumpVersion() {
	o.Version++
}

// acceptsFill 报告处于该状态的订单能否继续接收成交（终态里撤销、拒绝、过期的订单也可能有迟到的成交）。
func (s State) acceptsFill() bool {
	switch s {
	case Submitting, SubmitUnknown, Open, PartiallyFilled, Canceling, CancelUnknown, Canceled, PartiallyCanceled, Rejected, Expired:
		return true
	}
	return false
}
