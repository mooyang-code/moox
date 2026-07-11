package order

import (
	"errors"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

var ErrInvalidTransition = errors.New("trade: invalid order transition")

type Event struct {
	Type    string
	Version uint64
}

type Order struct {
	ID             shared.OrderID
	ClientOrderID  string
	Quantity       shared.Decimal
	FilledQuantity shared.Decimal
	State          State
	Version        uint64
}

func New(id shared.OrderID, clientID string, qty shared.Decimal) (*Order, []Event, error) {
	if id == "" || clientID == "" || qty.Cmp(shared.Zero()) <= 0 {
		return nil, nil, ErrInvalidTransition
	}
	o := &Order{ID: id, ClientOrderID: clientID, Quantity: qty, State: Draft}
	return o, o.emit("OrderIntentCreated"), nil
}

func (o *Order) transition(to State, allowed ...State) ([]Event, error) {
	if o.State.Terminal() {
		return nil, ErrInvalidTransition
	}
	ok := false
	for _, s := range allowed {
		if o.State == s {
			ok = true
			break
		}
	}
	if !ok {
		return nil, ErrInvalidTransition
	}
	o.State = to
	return o.emit("OrderStateChanged"), nil
}

func (o *Order) MarkReady() ([]Event, error)   { return o.transition(Ready, Draft) }
func (o *Order) BeginSubmit() ([]Event, error) { return o.transition(Submitting, Ready, SubmitUnknown) }
func (o *Order) MarkUnknown() ([]Event, error) { return o.transition(SubmitUnknown, Submitting) }
func (o *Order) Acknowledge() ([]Event, error) { return o.transition(Open, Submitting, SubmitUnknown) }
func (o *Order) Reject() ([]Event, error) {
	return o.transition(Rejected, Submitting, SubmitUnknown, Ready)
}
func (o *Order) BeginCancel() ([]Event, error) {
	return o.transition(Canceling, Open, PartiallyFilled, SubmitUnknown)
}

func (o *Order) ApplyFill(qty shared.Decimal) ([]Event, error) {
	if (o.State != Submitting && o.State != SubmitUnknown && o.State != Open && o.State != PartiallyFilled && o.State != Canceling) || qty.Cmp(shared.Zero()) <= 0 {
		return nil, ErrInvalidTransition
	}
	next := o.FilledQuantity.Add(qty)
	if next.Cmp(o.Quantity) > 0 {
		return nil, ErrInvalidTransition
	}
	o.FilledQuantity = next
	if next.Cmp(o.Quantity) == 0 {
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
	return o.transition(to, Canceling)
}

func (o *Order) emit(kind string) []Event {
	o.Version++
	return []Event{{Type: kind, Version: o.Version}}
}
