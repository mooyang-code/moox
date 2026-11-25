package storetest

import (
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// CreateFilledOrder 建一笔已成交的订单，并把它的成交记进标的汇总（受管、数量、成本、最近成交价），
// 效果等同于这笔订单的成交已经入账，但不写成交行、不动模拟余额。只有持仓来自余额的市场写汇总。
func CreateFilledOrder(tx *store.Tx, order store.OrderRecord) error {
	if err := tx.CreateOrder(order); err != nil {
		return err
	}
	if !exchange.RuleFor(order.MarketType).PositionFromBalance {
		return nil
	}
	if order.FilledQuantity == "" || order.FilledQuantity == "0" {
		return nil
	}
	quantity, err := shared.ParseDecimal(order.FilledQuantity)
	if err != nil || quantity.IsZero() {
		return err
	}
	price, err := shared.ParseDecimal(order.AveragePrice)
	if err != nil || price.IsZero() {
		if price, err = shared.ParseDecimal(order.ReferencePrice); err != nil {
			return err
		}
	}
	summary := store.InstrumentSummaryRecord{
		SpaceID: order.SpaceID, TradingAccountID: order.TradingAccountID, MarketType: order.MarketType,
		ExchangeSymbol: order.ExchangeSymbol, InstrumentID: order.InstrumentID,
		Managed:  order.OwnerType == "TARGET" || order.OwnerType == "OPERATOR",
		Quantity: shared.Zero(), Cost: shared.Zero(), LastPrice: price, LastTradedAt: 1,
	}
	if order.Side == string(exchange.SideBuy) {
		summary.Quantity, summary.Cost = quantity, quantity.Mul(price)
	}
	return tx.SeedInstrumentSummaryForTest(summary)
}
