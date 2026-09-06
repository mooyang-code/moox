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

type Event struct {
	Type    string
	Version uint64
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

func New(id shared.OrderID, spec OrderSpec) (*Order, []Event, error) {
	if id == "" ||
		strings.TrimSpace(spec.TradingAccountID) == "" ||
		strings.TrimSpace(spec.ClientOrderID) == "" ||
		strings.TrimSpace(spec.InstrumentID) == "" ||
		spec.Quantity.Cmp(shared.Zero()) <= 0 {
		return nil, nil, ErrInvalidOrder
	}
	if err := spec.Owner.Validate(); err != nil {
		return nil, nil, ErrInvalidOrder
	}
	order := &Order{
		ID:           id,
		Spec:         spec,
		AppliedFills: make(map[shared.FillID]shared.Decimal),
		State:        Pending,
	}
	return order, order.emit("OrderIntentCreated"), nil
}

func (o *Order) BeginSubmit() ([]Event, error) {
	return o.transition(Submitting, Pending)
}

func (o *Order) MarkSubmitUnknown() ([]Event, error) {
	return o.transition(SubmitUnknown, Submitting)
}

// AbortSubmit is only valid before the adapter call. Unlike ReturnToPending,
// it records that submission is known not to have left the process.
func (o *Order) AbortSubmit() ([]Event, error) {
	return o.transition(Pending, Submitting)
}

func (o *Order) ReturnToPending() ([]Event, error) {
	return o.transition(Pending, SubmitUnknown)
}

func (o *Order) Acknowledge(exchangeOrderID string) ([]Event, error) {
	if strings.TrimSpace(exchangeOrderID) == "" {
		return nil, ErrInvalidTransition
	}
	if o.ExchangeOrderID != "" && o.ExchangeOrderID != exchangeOrderID {
		return nil, fmt.Errorf("%w: conflicting Exchange order ID", ErrInvalidTransition)
	}
	switch o.State {
	case Submitting, SubmitUnknown:
		o.State = Open
	case Open, PartiallyFilled, Filled, Canceling, CancelUnknown:
	default:
		return nil, ErrInvalidTransition
	}
	o.ExchangeOrderID = exchangeOrderID
	return o.emit("OrderAcknowledged"), nil
}

func (o *Order) Reject() ([]Event, error) {
	return o.transition(Rejected, Pending, Submitting, SubmitUnknown)
}

func (o *Order) Expire() ([]Event, error) {
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

func (o *Order) BeginCancel() ([]Event, error) {
	return o.transition(Canceling, Open, PartiallyFilled, SubmitUnknown)
}

func (o *Order) DiscardPending() ([]Event, error) {
	return o.transition(Canceled, Pending)
}

func (o *Order) MarkCancelUnknown() ([]Event, error) {
	return o.transition(CancelUnknown, Canceling)
}

func (o *Order) CancelRejected() ([]Event, error) {
	to := Open
	if !o.FilledQuantity.IsZero() {
		to = PartiallyFilled
	}
	return o.transition(to, Canceling)
}

func (o *Order) CancelStillOpen() ([]Event, error) {
	to := Open
	if !o.FilledQuantity.IsZero() {
		to = PartiallyFilled
	}
	return o.transition(to, CancelUnknown)
}

func (o *Order) ApplyFill(fill Fill) ([]Event, error) {
	if fill.ID == "" || fill.Quantity.Cmp(shared.Zero()) <= 0 {
		return nil, ErrInvalidTransition
	}
	if applied, exists := o.AppliedFills[fill.ID]; exists {
		if applied.Cmp(fill.Quantity) != 0 {
			return nil, fmt.Errorf("%w: conflicting duplicate Fill", ErrInvalidTransition)
		}
		return nil, nil
	}
	switch o.State {
	case Submitting,
		SubmitUnknown,
		Open,
		PartiallyFilled,
		Canceling,
		CancelUnknown,
		Canceled,
		PartiallyCanceled,
		Rejected:
	default:
		return nil, ErrInvalidTransition
	}
	next := o.FilledQuantity.Add(fill.Quantity)
	if next.Cmp(o.Spec.Quantity) > 0 {
		return nil, fmt.Errorf("%w: Fill exceeds order quantity", ErrInvalidTransition)
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
	} else if o.State != Canceling && o.State != CancelUnknown {
		o.State = PartiallyFilled
	}
	return o.emit("FillApplied"), nil
}

func (o *Order) ConfirmCancel() ([]Event, error) {
	to := Canceled
	if !o.FilledQuantity.IsZero() {
		to = PartiallyCanceled
	}
	return o.transition(
		to,
		Submitting,
		SubmitUnknown,
		Open,
		PartiallyFilled,
		Canceling,
		CancelUnknown,
	)
}

func (o *Order) transition(to State, allowed ...State) ([]Event, error) {
	if o.State.Terminal() {
		return nil, ErrInvalidTransition
	}
	for _, state := range allowed {
		if o.State == state {
			o.State = to
			return o.emit("OrderStateChanged"), nil
		}
	}
	return nil, ErrInvalidTransition
}

func (o *Order) emit(eventType string) []Event {
	o.Version++
	return []Event{{Type: eventType, Version: o.Version}}
}
