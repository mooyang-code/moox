package equity

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/ashare"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type sessionCatalog map[exchange.Exchange]execution.ExchangeDescriptor

func (c sessionCatalog) Descriptor(name exchange.Exchange) (execution.ExchangeDescriptor, bool) {
	descriptor, ok := c[name]
	return descriptor, ok
}

// beijing 把北京时间写成 UTC 时刻。
func beijing(value string) time.Time {
	parsed, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil {
		panic(err)
	}
	return parsed.Add(-8 * time.Hour).UTC()
}

func seedStockAccount(t *testing.T, lastSync time.Time) *store.Store {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tradeStore.Close() })
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "stock-1", Name: "A 股模拟",
			Exchange: "ASHARE", Markets: []string{"STOCK"}, ExecutionMode: "PAPER", Environment: "PAPER",
			SettlementAsset: "CNY", Status: "ENABLED", Ready: true, LastSyncAt: lastSync.UnixMilli(),
			Snapshot: store.TradingAccountSnapshot{
				Balances: []store.AssetBalance{
					{Wallet: "UNIFIED", Asset: "CNY", Available: "1000000", Total: "1000000"},
					{Wallet: "UNIFIED", Asset: "600519.XSHG", Available: "100", Total: "100"},
				},
				ExchangeUpdatedAt: lastSync.UnixMilli(),
			},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical", ExecutionMode: "PAPER",
			SettlementAsset: "CNY", AutomationState: "PAUSED", PauseReason: "test",
		}); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "ASHARE", MarketType: "STOCK", ExchangeSymbol: "600519.XSHG", InstrumentID: "600519.XSHG",
			BaseAsset: "600519.XSHG", QuoteAsset: "CNY", SettlementAsset: "CNY",
			ExchangeQuantityStep: "100", MinExchangeQuantity: "100", PriceTick: "0.01", Status: "TRADING",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "stock-1", Enabled: true,
		}); err != nil {
			return err
		}
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", TradingAccountID: "stock-1", OrderID: "bought", ClientOrderID: "bought",
			Exchange: "ASHARE", MarketType: "STOCK", ExchangeSymbol: "600519.XSHG", InstrumentID: "600519.XSHG",
			OrderType: "MARKET", Side: "BUY", Quantity: "100", FilledQuantity: "100", ReferencePrice: "1450", State: "FILLED",
			OwnerType: "OPERATOR", OwnerID: "action-1", LogicalAccountID: "logical-1", Version: 1,
		})
	}))
	return tradeStore
}

func stockEquityService(t *testing.T, tradeStore *store.Store, now time.Time, quoteAt time.Time) *Service {
	t.Helper()
	session, err := ashare.NewSession()
	require.NoError(t, err)
	adapter := &equityTestAdapter{price: exchange.ReferencePrice{Price: shared.MustDecimal("1453"), UpdatedAt: quoteAt}}
	return &Service{
		Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return now },
		Exchanges: sessionCatalog{exchange.ExchangeAShare: {Exchange: exchange.ExchangeAShare, Session: session}},
	}
}

// 周六上午：余额每 30 秒同步一次，持仓报价是周五收盘后的，权益照常可用，周五 15:00 的目标第二天也能换算。
func TestStockEquityStaysFreshOutsideSessionWithCloseQuote(t *testing.T) {
	now := beijing("2026-11-07 10:00:00")
	tradeStore := seedStockAccount(t, now.Add(-20*time.Second))
	service := stockEquityService(t, tradeStore, now, beijing("2026-11-06 16:14:58"))

	require.NoError(t, service.SampleAccount(context.Background(), "stock-1"))
	equity, sourceTime, err := service.ResolveLogicalAccountEquity(context.Background(), "space-1", "logical-1")
	require.NoError(t, err, "时段外有效的收盘价不拉低权益的来源时间")
	require.Equal(t, "1145300", equity.String())
	require.Equal(t, now.Add(-20*time.Second).UnixMilli(), sourceTime)
}

// 反例：夜里余额同步失败超过 90 秒，权益仍然判为过期（只放宽报价时间，不放宽同步时间）。
func TestStockEquityStillRequiresRecentBalanceSync(t *testing.T) {
	now := beijing("2026-11-07 10:00:00")
	tradeStore := seedStockAccount(t, now.Add(-2*time.Minute))
	service := stockEquityService(t, tradeStore, now, beijing("2026-11-06 16:14:58"))

	require.ErrorContains(t, service.SampleAccount(context.Background(), "stock-1"), "stale Spot balance snapshot")
}

// 时段内照原来的 90 秒：两分钟前的报价过期，估值失败（降级规则里也没有可用的最后已知价）。
func TestStockEquityInSessionUsesNormalWindow(t *testing.T) {
	now := beijing("2026-11-06 10:00:00")
	tradeStore := seedStockAccount(t, now.Add(-10*time.Second))
	service := stockEquityService(t, tradeStore, now, now.Add(-2*time.Minute))

	require.ErrorContains(t, service.SampleAccount(context.Background(), "stock-1"), "stale Spot quote")
}
