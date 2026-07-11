package consumer

import (
	"context"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/position"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type FillHandler struct{ Store *store.Store }

func (h FillHandler) Handle(ctx context.Context, space, account, orderID, fillID string, f exchange.FillEvent) error {
	return h.Store.Transaction(ctx, func(tx *store.Tx) error {
		fresh, err := tx.InsertFill(space, fillID, f.ExchangeTradeID, orderID, f.Quantity.String(), f.Price.String(), f.Fee.String(), f.FeeCurrency)
		if err != nil || !fresh {
			return err
		}
		r, err := tx.GetOrder(ctx, space, orderID)
		if err != nil {
			return err
		}
		q, parseErr := shared.ParseDecimal(r.Quantity)
		if parseErr != nil {
			return parseErr
		}
		if q.IsZero() {
			return errors.New("trade: corrupted order quantity")
		}
		fq, parseErr := shared.ParseDecimal(r.FilledQuantity)
		if parseErr != nil {
			return parseErr
		}
		o := &order.Order{ID: shared.OrderID(r.OrderID), ClientOrderID: r.ClientOrderID, Quantity: q, FilledQuantity: fq, State: order.State(r.State), Version: r.Version}
		expected := o.Version
		if _, err = o.ApplyFill(f.Quantity); err != nil {
			return err
		}
		r.FilledQuantity = o.FilledQuantity.String()
		consumed, parseErr := shared.ParseDecimal(r.ConsumedReserved)
		if parseErr != nil {
			return parseErr
		}
		used := f.Quantity
		if r.MarketType != "" && r.MarketType != "spot" {
			reserved, reserveErr := shared.ParseDecimal(r.ReservedAmount)
			if reserveErr != nil {
				return reserveErr
			}
			used = reserved.Mul(f.Quantity).Div(q)
			if r.ReduceOnly {
				used = shared.Zero()
			}
		} else if f.Side == "BUY" {
			used = f.Quantity.Mul(f.Price)
		}
		r.ConsumedReserved = consumed.Add(used).String()
		r.State = string(o.State)
		r.Version = o.Version
		if err = tx.UpdateOrder(r, expected); err != nil {
			return err
		}
		if f.BaseAsset == "" || f.QuoteAsset == "" || (f.Side != "BUY" && f.Side != "SELL") {
			return errors.New("trade: incomplete normalized fill")
		}
		amount := f.Quantity.Mul(f.Price)
		var entries []ledger.Entry
		if r.MarketType != "" && r.MarketType != "spot" {
			if !r.ReduceOnly {
				entries = []ledger.Entry{{AccountID: account, Asset: f.QuoteAsset, Bucket: "frozen", Amount: used.Neg()}, {AccountID: account, Asset: f.QuoteAsset, Bucket: "margin", Amount: used}}
			}
		} else if f.Side == "BUY" {
			entries = []ledger.Entry{
				{AccountID: account, Asset: f.QuoteAsset, Bucket: "frozen", Amount: amount.Neg()}, {AccountID: "exchange-clearing", Asset: f.QuoteAsset, Bucket: "clearing", Amount: amount},
				{AccountID: "exchange-clearing", Asset: f.BaseAsset, Bucket: "clearing", Amount: f.Quantity.Neg()}, {AccountID: account, Asset: f.BaseAsset, Bucket: "available", Amount: f.Quantity},
			}
		} else {
			entries = []ledger.Entry{
				{AccountID: account, Asset: f.BaseAsset, Bucket: "frozen", Amount: f.Quantity.Neg()}, {AccountID: "exchange-clearing", Asset: f.BaseAsset, Bucket: "clearing", Amount: f.Quantity},
				{AccountID: "exchange-clearing", Asset: f.QuoteAsset, Bucket: "clearing", Amount: amount.Neg()}, {AccountID: account, Asset: f.QuoteAsset, Bucket: "available", Amount: amount},
			}
		}
		posting := ledger.Transaction{ID: shared.LedgerTransactionID("fill:" + fillID), BizType: "fill", RefType: "fill", RefID: fillID, Entries: entries}
		if f.Fee.Cmp(shared.Zero()) > 0 {
			posting.Entries = append(posting.Entries, ledger.Entry{AccountID: account, Asset: f.FeeCurrency, Bucket: "available", Amount: f.Fee.Neg()}, ledger.Entry{AccountID: "exchange-fees", Asset: f.FeeCurrency, Bucket: "fees", Amount: f.Fee})
		}
		if len(posting.Entries) > 0 {
			if err = tx.PostLedger(space, posting); err != nil {
				return err
			}
		}
		if err = tx.ApplyPosition(space, account, f.Symbol, position.Fill{Side: f.Side, Quantity: f.Quantity, Price: f.Price}); err != nil {
			return err
		}
		if o.State == order.Filled {
			if err = command.ReleaseReservation(tx, r); err != nil {
				return err
			}
		}
		return tx.AddOutbox(fmt.Sprintf("%s:fill:%s", orderID, fillID), "moox.trade.fill.received.v1", []byte(fillID))
	})
}
