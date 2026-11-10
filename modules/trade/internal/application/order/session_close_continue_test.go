package order

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/ashare"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 一笔收盘撤单失败不挡住后面的：其余订单照常结束，错误最后一起返回。
func TestSessionCloserContinuesAfterOneOrderFails(t *testing.T) {
	db := seedCloseOrders(t)
	limit := "10"
	require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", TradingAccountID: "stock-1", OrderID: "friday-broken", ClientOrderID: "friday-broken", ExchangeOrderID: "friday-broken",
			MarketType: "STOCK", ExchangeSymbol: "600519.XSHG", InstrumentID: "600519.XSHG", OrderType: "LIMIT", TimeInForce: "GTC", LimitPrice: &limit,
			Side: "BUY", Quantity: "100", FilledQuantity: "0", ReferencePrice: "10", ReservedAsset: "CNY", ReservedQuantity: "1005",
			RemainingReservedQuantity: "1005", OwnerType: "EXTERNAL", OwnerID: "friday-broken", State: "OPEN",
			SubmittedAt: beijingAt("2026-11-06 09:30:00").UnixMilli(),
		})
	}))
	// 把数量改坏，模拟一笔收盘撤单必然失败的订单。
	require.NoError(t, db.DBForTest().Exec(`UPDATE t_trade_orders SET c_quantity = 'broken' WHERE c_order_id = 'friday-broken'`).Error)
	session, err := ashare.NewSession()
	require.NoError(t, err)
	closer := &SessionCloser{
		Store: db, Exchanges: closeCatalog{exchange.ExchangeAShare: {Exchange: exchange.ExchangeAShare, Session: session}},
		Now: func() time.Time { return beijingAt("2026-11-06 15:05:00") },
	}
	account, err := db.GetTradingAccountByID(context.Background(), "stock-1")
	require.NoError(t, err)

	closed, err := closer.CloseAccount(context.Background(), account)
	require.ErrorContains(t, err, "friday-broken")
	require.Equal(t, 2, closed)
	require.Equal(t, "OPEN", orderState(t, db, "friday-broken").State)
	for _, id := range []string{"friday-open", "friday-canceling"} {
		require.Equal(t, "CANCELED", orderState(t, db, id).State, id)
	}
}
