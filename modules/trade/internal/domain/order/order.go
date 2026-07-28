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

type Order struct {
	ID               shared.OrderID
	Spec             OrderSpec
	ExchangeOrderID  string
	FilledQuantity   shared.Decimal
	AverageFillPrice shared.Decimal
	State            State
	Version          uint64
}

func New(id shared.OrderID, spec OrderSpec) (*Order, []Event, error) {
	if id == "" ||
		strings.TrimSpace(spec.ExchangeAccountID) == "" ||
		strings.TrimSpace(spec.ClientOrderID) == "" ||
		strings.TrimSpace(spec.Symbol) == "" ||
		spec.Quantity.Cmp(shared.Zero()) <= 0 {
		return nil, nil, ErrInvalidOrder
	}
	order := &Order{
		ID:    id,
		Spec:  spec,
		State: Pending,
	}
	return order, order.emit("OrderIntentCreated"), nil
}

func (o *Order) BeginSubmit() ([]Event, error) {
	return o.transition(Submitting, Pending)
}

func (o *Order) MarkSubmitUnknown() ([]Event, error) {
	return o.transition(SubmitUnknown, Submitting)
}

func (o *Order) ReturnToPending() ([]Event, error) {
	return o.transition(Pending, SubmitUnknown)
}

func (o *Order) Acknowledge(exchangeOrderID string) ([]Event, error) {
	if strings.TrimSpace(exchangeOrderID) == "" {
		return nil, ErrInvalidTransition
	}
	events, err := o.transition(Open, Submitting, SubmitUnknown)
	if err != nil {
		return nil, err
	}
	o.ExchangeOrderID = exchangeOrderID
	return events, nil
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

func (o *Order) MarkCancelUnknown() ([]Event, error) {
	return o.transition(CancelUnknown, Canceling)
}

func (o *Order) CancelStillOpen() ([]Event, error) {
	to := Open
	if !o.FilledQuantity.IsZero() {
		to = PartiallyFilled
	}
	return o.transition(to, CancelUnknown)
}

func (o *Order) ApplyFill(quantity shared.Decimal) ([]Event, error) {
	switch o.State {
	case Submitting, SubmitUnknown, Open, PartiallyFilled, Canceling, CancelUnknown:
	default:
		return nil, ErrInvalidTransition
	}
	if quantity.Cmp(shared.Zero()) <= 0 {
		return nil, ErrInvalidTransition
	}
	next := o.FilledQuantity.Add(quantity)
	if next.Cmp(o.Spec.Quantity) > 0 {
		return nil, fmt.Errorf("%w: Fill exceeds order quantity", ErrInvalidTransition)
	}
	o.FilledQuantity = next
	if next.Cmp(o.Spec.Quantity) == 0 {
		o.State = Filled
	} else {
		o.State = PartiallyFilled
	}
	return o.emit("FillApplied"), nil
}

func (o *Order) ConfirmCancel() ([]Event, error) {
	to := Canceled
	if !o.FilledQuantity.IsZero() {
		to = PartiallyCanceled
	}
	return o.transition(to, Canceling, CancelUnknown)
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
