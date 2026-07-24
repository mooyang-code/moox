package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"gorm.io/gorm"
)

type Engine struct {
	Store    *store.Store
	Adapter  exchange.TradingAdapter
	Resolver exchange.AdapterResolver
}
type PlaceInput struct {
	SpaceID, OrderID, ClientOrderID, AccountID, ChannelID, Symbol, MarketType, BaseAsset, QuoteAsset, Side, Quantity, Price string
	ReduceOnly                                                                                                              bool
}

func (e *Engine) Place(ctx context.Context, in PlaceInput) (store.OrderRecord, error) {
	started := time.Now()
	defer func() {
		telemetry.OperationLatency.WithLabelValues("place_command").Observe(time.Since(started).Seconds())
	}()
	paused, pauseErr := e.Store.IsPaused(ctx, in.SpaceID, in.AccountID, in.ChannelID)
	if pauseErr != nil {
		return store.OrderRecord{}, pauseErr
	}
	if paused {
		return store.OrderRecord{}, errors.New("trade: account or channel is paused")
	}
	if old, err := e.Store.GetOrderByClientID(ctx, in.SpaceID, in.ClientOrderID); err == nil {
		return old, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return store.OrderRecord{}, err
	}
	qty, err := shared.ParseDecimal(in.Quantity)
	if err != nil {
		telemetry.Commands.WithLabelValues("place", "rejected").Inc()
		return store.OrderRecord{}, err
	}
	if e.Resolver != nil {
		a, resolveErr := e.Resolver.Resolve(ctx, in.SpaceID, in.ChannelID)
		if resolveErr != nil {
			return store.OrderRecord{}, resolveErr
		}
		if provider, ok := a.(interface{ MarketType() string }); ok {
			in.MarketType = provider.MarketType()
		}
		if in.BaseAsset == "" || in.QuoteAsset == "" {
			rules, rulesErr := a.Rules(ctx, in.Symbol)
			if rulesErr != nil {
				return store.OrderRecord{}, rulesErr
			}
			in.BaseAsset, in.QuoteAsset = rules.BaseAsset, rules.QuoteAsset
		}
	} else if in.BaseAsset == "" || in.QuoteAsset == "" {
		return store.OrderRecord{}, errors.New("trade: instrument assets required")
	}
	o, _, err := order.New(shared.OrderID(in.OrderID), in.ClientOrderID, qty)
	if err != nil {
		return store.OrderRecord{}, err
	}
	o.MarkReady()
	r := record(in, o, "")
	err = e.Store.Transaction(ctx, func(tx *store.Tx) error {
		price, parseErr := shared.ParseDecimal(in.Price)
		if parseErr != nil {
			return parseErr
		}
		asset, amount := in.BaseAsset, qty
		if in.MarketType != "" && in.MarketType != "spot" {
			asset = in.QuoteAsset
			amount = qty.Mul(price)
			if in.ReduceOnly {
				amount = shared.Zero()
			}
		} else if in.Side == "BUY" {
			asset = in.QuoteAsset
			amount = qty.Mul(price)
		} else if in.Side != "SELL" {
			return errors.New("trade: invalid side")
		}
		r.ReservedAsset = asset
		r.ReservedAmount = amount.String()
		r.ConsumedReserved = "0"
		if !amount.IsZero() {
			freeze := ledger.Transaction{ID: shared.LedgerTransactionID("freeze:" + in.OrderID), BizType: "freeze", RefType: "order", RefID: in.OrderID, Entries: []ledger.Entry{{AccountID: in.AccountID, Asset: asset, Bucket: "available", Amount: amount.Neg()}, {AccountID: in.AccountID, Asset: asset, Bucket: "frozen", Amount: amount}}}
			if err := tx.PostLedger(in.SpaceID, freeze); err != nil {
				return err
			}
		}
		if err := tx.CreateOrder(&r); err != nil {
			return err
		}
		if err := outbox(tx, in.OrderID+":created", "moox.trade.order.intent.created.v1", r); err != nil {
			return err
		}
		return outbox(tx, in.OrderID+":ready", "moox.trade.execution.slice.ready.v1", r)
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return e.Store.GetOrderByClientID(ctx, in.SpaceID, in.ClientOrderID)
		}
		return store.OrderRecord{}, err
	}
	telemetry.Commands.WithLabelValues("place", "accepted").Inc()
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
	adapter, err := e.adapter(ctx, r)
	if err != nil {
		return r, err
	}
	result, callErr := adapter.Place(ctx, exchange.PlaceRequest{ClientOrderID: r.ClientOrderID, Symbol: r.Symbol, Side: r.Side, Type: "LIMIT", TimeInForce: "IOC", Quantity: o.Quantity, Price: price, ReduceOnly: r.ReduceOnly})
	if callErr == nil {
		telemetry.Submissions.WithLabelValues("acknowledged").Inc()
	} else if exchange.IsCategory(callErr, exchange.ErrorTransportUncertain) {
		telemetry.Submissions.WithLabelValues("unknown").Inc()
	} else {
		telemetry.Submissions.WithLabelValues("rejected").Inc()
	}
	latest, _ := e.Store.GetOrder(ctx, space, orderID)
	o, _ = aggregate(latest)
	expected = o.Version
	event := "moox.trade.order.state.changed.v1"
	if callErr != nil {
		if exchange.IsCategory(callErr, exchange.ErrorTransportUncertain) {
			_, err = o.MarkUnknown()
			event = "moox.trade.order.state.changed.v1"
		} else {
			_, err = o.Reject()
			event = "moox.trade.order.state.changed.v1"
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
		if o.State == order.Rejected {
			if err := ReleaseReservation(tx, latest); err != nil {
				return err
			}
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
	adapter, err := e.adapter(ctx, r)
	if err != nil {
		return r, err
	}
	result, err := adapter.QueryByClientOrderID(ctx, r.Symbol, r.ClientOrderID)
	if err != nil {
		if exchange.IsCategory(err, exchange.ErrorOrderNotFound) {
			o, aggregateErr := aggregate(r)
			if aggregateErr != nil {
				return r, aggregateErr
			}
			expected := o.Version
			if _, aggregateErr = o.RetryAfterNotFound(); aggregateErr != nil {
				return r, aggregateErr
			}
			r = recordFrom(r, o)
			if aggregateErr = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateOrder(r, expected) }); aggregateErr != nil {
				return r, aggregateErr
			}
			return e.Submit(ctx, space, orderID, r.Price)
		}
		return r, err
	}
	o, err := aggregate(r)
	if err != nil {
		return r, err
	}
	expected := o.Version
	r.ExchangeOrderID = result.ExchangeOrderID
	switch result.Status {
	case "FILLED":
		// A query response is not a settlement fact. Keep the order open until
		// exchange trade records arrive through the idempotent FillHandler.
		_, err = o.Acknowledge()
	case "CANCELED":
		// The fill reconciliation worker must ingest all exchange fills before
		// making a canceled order terminal and releasing its reservation.
		if err := e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateOrder(r, expected) }); err != nil {
			return r, err
		}
		return r, nil
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
	err = e.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateOrder(r, expected); err != nil {
			return err
		}
		if o.State == order.Rejected || o.State == order.Canceled || o.State == order.PartiallyCanceled {
			return ReleaseReservation(tx, r)
		}
		return nil
	})
	return r, err
}
func (e *Engine) RecoverSubmitting(ctx context.Context, space, orderID string) (store.OrderRecord, error) {
	r, err := e.Store.GetOrder(ctx, space, orderID)
	if err != nil {
		return r, err
	}
	if r.State != string(order.Submitting) {
		return r, nil
	}
	o, err := aggregate(r)
	if err != nil {
		return r, err
	}
	expected := o.Version
	if _, err = o.RecoverSubmitting(); err != nil {
		return r, err
	}
	r = recordFrom(r, o)
	if err = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateOrder(r, expected) }); err != nil {
		return r, err
	}
	return e.ResolveUnknown(ctx, space, orderID)
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
	return store.OrderRecord{SpaceID: in.SpaceID, OrderID: in.OrderID, ClientOrderID: in.ClientOrderID, AccountID: in.AccountID, ChannelID: in.ChannelID, Symbol: in.Symbol, MarketType: in.MarketType, BaseAsset: in.BaseAsset, QuoteAsset: in.QuoteAsset, Side: in.Side, Quantity: o.Quantity.String(), Price: in.Price, ReduceOnly: in.ReduceOnly, FilledQuantity: o.FilledQuantity.String(), State: string(o.State), ExchangeOrderID: exchangeID, Version: o.Version}
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
func (e *Engine) adapter(ctx context.Context, r store.OrderRecord) (exchange.TradingAdapter, error) {
	if e.Resolver != nil {
		return e.Resolver.Resolve(ctx, r.SpaceID, r.ChannelID)
	}
	if e.Adapter == nil {
		return nil, errors.New("trade: exchange adapter unavailable")
	}
	return e.Adapter, nil
}
func (e *Engine) AdapterFor(ctx context.Context, r store.OrderRecord) (exchange.TradingAdapter, error) {
	return e.adapter(ctx, r)
}
