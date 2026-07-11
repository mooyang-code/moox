package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

type Engine struct {
	Store   *store.Store
	Adapter exchange.TradingAdapter
}
type PlaceInput struct{ SpaceID, OrderID, ClientOrderID, AccountID, Symbol, Side, Quantity, Price string }

func (e *Engine) Place(ctx context.Context, in PlaceInput) (store.OrderRecord, error) {
	if old, err := e.Store.GetOrderByClientID(ctx, in.SpaceID, in.ClientOrderID); err == nil {
		return old, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return store.OrderRecord{}, err
	}
	qty, err := shared.ParseDecimal(in.Quantity)
	if err != nil {
		return store.OrderRecord{}, err
	}
	o, _, err := order.New(shared.OrderID(in.OrderID), in.ClientOrderID, qty)
	if err != nil {
		return store.OrderRecord{}, err
	}
	o.MarkReady()
	r := record(in, o, "")
	err = e.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateOrder(&r); err != nil {
			return err
		}
		if err := outbox(tx, in.OrderID+":created", "moox.trade.order.intent_created.v1", r); err != nil {
			return err
		}
		return outbox(tx, in.OrderID+":ready", "moox.trade.execution.slice_ready.v1", r)
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return e.Store.GetOrderByClientID(ctx, in.SpaceID, in.ClientOrderID)
		}
		return store.OrderRecord{}, err
	}
	return r, nil
}

func (e *Engine) Submit(ctx context.Context, space, orderID, priceRaw string) (store.OrderRecord, error) {
	r, err := e.Store.GetOrder(ctx, space, orderID)
	if err != nil {
		return r, err
	}
	o, err := aggregate(r)
	if err != nil {
		return r, err
	}
	expected := o.Version
	if _, err = o.BeginSubmit(); err != nil {
		return r, err
	}
	r = recordFrom(r, o)
	if err = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateOrder(r, expected) }); err != nil {
		return r, err
	}
	if priceRaw == "" {
		priceRaw = r.Price
	}
	price, err := shared.ParseDecimal(priceRaw)
	if err != nil {
		return r, err
	}
	result, callErr := e.Adapter.Place(ctx, exchange.PlaceRequest{ClientOrderID: r.ClientOrderID, Symbol: r.Symbol, Side: r.Side, Type: "LIMIT", TimeInForce: "IOC", Quantity: o.Quantity, Price: price})
	latest, _ := e.Store.GetOrder(ctx, space, orderID)
	o, _ = aggregate(latest)
	expected = o.Version
	event := "moox.trade.order.acknowledged.v1"
	if callErr != nil {
		if exchange.IsCategory(callErr, exchange.ErrorTransportUncertain) {
			_, err = o.MarkUnknown()
			event = "moox.trade.order.submit_unknown.v1"
		} else {
			_, err = o.Reject()
			event = "moox.trade.order.state_changed.v1"
		}
	} else {
		_, err = o.Acknowledge()
		latest.ExchangeOrderID = result.ExchangeOrderID
	}
	if err != nil {
		return latest, err
	}
	latest = recordFrom(latest, o)
	err = e.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateOrder(latest, expected); err != nil {
			return err
		}
		return outbox(tx, fmt.Sprintf("%s:%d", orderID, o.Version), event, latest)
	})
	return latest, err
}

func (e *Engine) ResolveUnknown(ctx context.Context, space, orderID string) (store.OrderRecord, error) {
	r, err := e.Store.GetOrder(ctx, space, orderID)
	if err != nil {
		return r, err
	}
	if r.State != string(order.SubmitUnknown) {
		return r, nil
	}
	result, err := e.Adapter.QueryByClientOrderID(ctx, r.Symbol, r.ClientOrderID)
	if err != nil {
		return r, err
	}
	o, err := aggregate(r)
	if err != nil {
		return r, err
	}
	expected := o.Version
	switch result.Status {
	case "FILLED":
		if result.FilledQuantity.Cmp(shared.Zero()) <= 0 {
			result.FilledQuantity = o.Quantity
		}
		_, err = o.ApplyFill(result.FilledQuantity)
	case "CANCELED":
		if _, err = o.BeginCancel(); err == nil {
			_, err = o.ConfirmCancel()
		}
	case "REJECTED":
		_, err = o.Reject()
	default:
		_, err = o.Acknowledge()
	}
	if err != nil {
		return r, err
	}
	r = recordFrom(r, o)
	r.ExchangeOrderID = result.ExchangeOrderID
	err = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateOrder(r, expected) })
	return r, err
}

func aggregate(r store.OrderRecord) (*order.Order, error) {
	q, e := shared.ParseDecimal(r.Quantity)
	if e != nil {
		return nil, e
	}
	f, e := shared.ParseDecimal(r.FilledQuantity)
	if e != nil {
		return nil, e
	}
	return &order.Order{ID: shared.OrderID(r.OrderID), ClientOrderID: r.ClientOrderID, Quantity: q, FilledQuantity: f, State: order.State(r.State), Version: r.Version}, nil
}
func record(in PlaceInput, o *order.Order, exchangeID string) store.OrderRecord {
	return store.OrderRecord{SpaceID: in.SpaceID, OrderID: in.OrderID, ClientOrderID: in.ClientOrderID, Symbol: in.Symbol, Side: in.Side, Quantity: o.Quantity.String(), Price: in.Price, FilledQuantity: o.FilledQuantity.String(), State: string(o.State), ExchangeOrderID: exchangeID, Version: o.Version}
}
func recordFrom(r store.OrderRecord, o *order.Order) store.OrderRecord {
	r.FilledQuantity = o.FilledQuantity.String()
	r.State = string(o.State)
	r.Version = o.Version
	return r
}
func outbox(tx *store.Tx, id, topic string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return tx.AddOutbox(id, topic, b)
}
