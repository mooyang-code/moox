package order

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/ashare"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type closeCatalog map[exchange.Exchange]execution.ExchangeDescriptor

func (c closeCatalog) Descriptor(name exchange.Exchange) (execution.ExchangeDescriptor, bool) {
	descriptor, ok := c[name]
	return descriptor, ok
}

func beijingAt(value string) time.Time {
	parsed, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil {
		panic(err)
	}
	return parsed.Add(-8 * time.Hour).UTC()
}

func seedCloseOrders(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	limit := "10"
	require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
		for _, account := range []store.TradingAccountRecord{
			{SpaceID: "space-1", TradingAccountID: "stock-1", Name: "A 股", Exchange: "ASHARE", Markets: []string{"STOCK"},
				ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "CNY", Status: "ENABLED"},
			{SpaceID: "space-1", TradingAccountID: "crypto-1", Name: "币安", Exchange: "BINANCE", Markets: []string{"SPOT"},
				ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT", Status: "ENABLED"},
		} {
			if err := tx.CreateTradingAccount(account); err != nil {
				return err
			}
		}
		for _, item := range []struct {
			id, account, state, submitted string
		}{
			{"friday-open", "stock-1", "OPEN", "2026-11-06 10:00:00"},
			{"friday-canceling", "stock-1", "CANCELING", "2026-11-06 14:00:00"},
			{"friday-unknown", "stock-1", "SUBMIT_UNKNOWN", "2026-11-06 10:00:00"},
			{"friday-cancel-unknown", "stock-1", "CANCEL_UNKNOWN", "2026-11-06 10:00:00"},
			{"monday-open", "stock-1", "OPEN", "2026-11-09 10:00:00"},
			{"never-submitted", "stock-1", "PENDING", ""},
			{"crypto-open", "crypto-1", "OPEN", "2026-11-06 10:00:00"},
		} {
			submitted := int64(0)
			if item.submitted != "" {
				submitted = beijingAt(item.submitted).UnixMilli()
			}
			symbol, market := "600519.XSHG", "STOCK"
			if item.account == "crypto-1" {
				symbol, market = "BTCUSDT", "SPOT"
			}
			if err := tx.CreateOrder(store.OrderRecord{
				SpaceID: "space-1", TradingAccountID: item.account, OrderID: item.id, ClientOrderID: item.id, ExchangeOrderID: item.id,
				MarketType: market, ExchangeSymbol: symbol, InstrumentID: symbol, OrderType: "LIMIT", TimeInForce: "GTC", LimitPrice: &limit,
				Side: "BUY", Quantity: "100", FilledQuantity: "0", ReferencePrice: "10", ReservedAsset: "CNY", ReservedQuantity: "1005",
				RemainingReservedQuantity: "1005", OwnerType: "EXTERNAL", OwnerID: item.id, State: item.state, SubmittedAt: submitted,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	return db
}

func orderState(t *testing.T, db *store.Store, id string) store.OrderRecord {
	t.Helper()
	record, err := db.GetOrder(context.Background(), "space-1", id)
	require.NoError(t, err)
	return record
}

// 收盘撤单只按 15:00 的日收盘判断：午间不碰上午的挂单；结果不明的单不在这里收尾；先同步再结束；重启后补做。
func TestSessionCloserCancelsOrdersSubmittedBeforeDailyClose(t *testing.T) {
	db := seedCloseOrders(t)
	session, err := ashare.NewSession()
	require.NoError(t, err)
	now := beijingAt("2026-11-09 12:00:00") // 周一午休：最近一次日收盘是周五 15:00
	var synced []string
	closer := &SessionCloser{
		Store: db, Exchanges: closeCatalog{exchange.ExchangeAShare: {Exchange: exchange.ExchangeAShare, Session: session}},
		Sync: func(_ context.Context, accountID string) error {
			synced = append(synced, accountID)
			return nil
		},
		Now: func() time.Time { return now },
	}
	require.NoError(t, closer.Run(context.Background()))

	require.Equal(t, []string{"stock-1"}, synced, "只有 A 股账户、且有要撤的单时才同步")
	for _, id := range []string{"friday-open", "friday-canceling"} {
		record := orderState(t, db, id)
		require.Equal(t, "CANCELED", record.State, id)
		require.Equal(t, ReasonSessionClose, record.RejectReason, id)
		require.Equal(t, "0", record.RemainingReservedQuantity, id)
	}
	for id, state := range map[string]string{
		"friday-unknown": "SUBMIT_UNKNOWN", "friday-cancel-unknown": "CANCEL_UNKNOWN",
		"monday-open": "OPEN", "never-submitted": "PENDING", "crypto-open": "OPEN",
	} {
		require.Equal(t, state, orderState(t, db, id).State, id)
	}

	// 进程重启（新的实例、不记进度）后，15:00 收盘之后再扫一遍：周一上午的挂单也收尾。
	synced = nil
	now = beijingAt("2026-11-09 15:00:30")
	restarted := *closer
	require.NoError(t, restarted.Run(context.Background()))
	require.Equal(t, "CANCELED", orderState(t, db, "monday-open").State)
	require.Equal(t, []string{"stock-1"}, synced)

	// 没有要撤的单时不再同步。
	synced = nil
	require.NoError(t, restarted.Run(context.Background()))
	require.Empty(t, synced)
}

// 收盘后的同步失败时不强制结束，下一轮重试。
func TestSessionCloserWaitsForSync(t *testing.T) {
	db := seedCloseOrders(t)
	session, err := ashare.NewSession()
	require.NoError(t, err)
	closer := &SessionCloser{
		Store: db, Exchanges: closeCatalog{exchange.ExchangeAShare: {Exchange: exchange.ExchangeAShare, Session: session}},
		Sync: func(context.Context, string) error { return context.DeadlineExceeded },
		Now:  func() time.Time { return beijingAt("2026-11-06 15:05:00") },
	}
	require.ErrorIs(t, closer.Run(context.Background()), context.DeadlineExceeded)
	require.Equal(t, "OPEN", orderState(t, db, "friday-open").State)
}
