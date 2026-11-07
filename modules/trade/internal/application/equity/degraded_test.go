package equity

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/stretchr/testify/require"
)

// delistingPriceAdapter 给 BTC 正常报价；DEAD 在 delisted 之后交易所回 -1121（标的不存在）；
// throttled 时所有取价都被限流。
type delistingPriceAdapter struct {
	equityTestAdapter
	at        time.Time
	delisted  bool
	throttled bool
	seen      []string
}

func (a *delistingPriceAdapter) GetReferencePrice(_ context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	a.seen = append(a.seen, key.Symbol)
	if a.throttled {
		return exchange.ReferencePrice{}, &exchange.Error{Kind: exchange.ErrorRateLimited, HTTPStatus: 429}
	}
	if key.Symbol == "DEADUSDT" {
		if a.delisted {
			return exchange.ReferencePrice{}, &exchange.Error{Kind: exchange.ErrorRejected, HTTPStatus: 400, Code: exchange.CodeBinanceInvalidSymbol}
		}
		return exchange.ReferencePrice{Price: shared.MustDecimal("2"), UpdatedAt: a.at}, nil
	}
	return exchange.ReferencePrice{Price: shared.MustDecimal("50000"), UpdatedAt: a.at}, nil
}

func seedDelistingAccount(t *testing.T, haltStatus string) *store.Store {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tradeStore.Close() })
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "live",
			Exchange: string(exchange.ExchangeBinance), Markets: []string{"SPOT"}, AccountMode: string(exchange.AccountModeBinanceStandard),
			ExecutionMode: string(exchange.ExecutionModeLive), Environment: string(exchange.AccountEnvironmentProduction),
			CredentialSecretID: "secret-1", SettlementAsset: "USDT",
			Status: string(exchange.AccountStatusEnabled), Ready: true,
			Snapshot: store.TradingAccountSnapshot{
				Balances: []store.AssetBalance{
					{Wallet: "SPOT", Asset: "USDT", Available: "500", Total: "500"},
					{Wallet: "SPOT", Asset: "BTC", Available: "0.01", Total: "0.01"},
					{Wallet: "SPOT", Asset: "DEAD", Available: "3", Total: "3"},
					{Wallet: "SPOT", Asset: "HALT", Available: "4", Total: "4"},
				},
				ExchangeUpdatedAt: sourceMillisDegraded,
			},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical", ExecutionMode: string(exchange.ExecutionModeLive),
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "test",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true,
		}); err != nil {
			return err
		}
		for asset, status := range map[string]string{"BTC": "TRADING", "DEAD": "TRADING", "HALT": haltStatus} {
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: string(exchange.ExchangeBinance), Environment: string(exchange.AccountEnvironmentProduction),
				MarketType: "SPOT", ExchangeSymbol: asset + "USDT", InstrumentID: asset + "-USDT-SPOT",
				BaseAsset: asset, QuoteAsset: "USDT", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001",
				PriceTick: "0.1", Status: status,
			}); err != nil {
				return err
			}
			if err := tx.CreateOrder(store.OrderRecord{
				SpaceID: "space-1", TradingAccountID: "account-1", OrderID: "bought-" + asset, ClientOrderID: "bought-" + asset,
				Exchange: string(exchange.ExchangeBinance), MarketType: "SPOT",
				ExchangeSymbol: asset + "USDT", InstrumentID: asset + "-USDT-SPOT", OrderType: "MARKET", Side: "BUY",
				Quantity: "1", FilledQuantity: "1", ReferencePrice: "1", State: "FILLED",
				OwnerType: "OPERATOR", OwnerID: "action-" + asset, LogicalAccountID: "logical-1", Version: 1,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	return tradeStore
}

const sourceMillisDegraded = int64(1_700_000_000_000)

func latestEquity(t *testing.T, tradeStore *store.Store) (string, string) {
	t.Helper()
	points, err := tradeStore.ListAccountEquityPoints(context.Background(), "space-1", "account-1", 0, 0)
	require.NoError(t, err)
	require.NotEmpty(t, points)
	logical, err := tradeStore.ListLogicalAccountEquityPoints(context.Background(), "space-1", "logical-1", 0, 0)
	require.NoError(t, err)
	require.NotEmpty(t, logical, "组合权益也要照常写入")
	return points[len(points)-1].Equity, logical[len(logical)-1].Equity
}

// haltFill 给 HALT 记一笔成交（价格 7）：标的停止交易后按它估值。
func haltFill(t *testing.T, tradeStore *store.Store) {
	t.Helper()
	require.NoError(t, tradeStore.DBForTest().Exec(`INSERT INTO t_order_fills (
		c_space_id, c_fill_id, c_exchange_trade_id, c_order_id, c_trading_account_id, c_exchange, c_market_type,
		c_instrument_id, c_exchange_symbol, c_side, c_price, c_quantity, c_traded_at
	) VALUES ('space-1', 'fill-halt', 'trade-halt', 'bought-HALT', 'account-1', 'BINANCE', 'SPOT',
		'HALT-USDT-SPOT', 'HALTUSDT', 'BUY', '7', '1', 1)`).Error)
}

// 受管资产取不到价时的降级（计划 4.6，总审查第 2 轮收窄）：
//   - 标的停止交易（BREAK）：不发取价请求，没有最后已知价就按本账户最近一笔成交价计；
//   - 交易所确认标的不存在（-1121）：沿用不超龄的最后已知价，没有就按 0 计。
//
// 这里原来的预期是 BREAK 按 0 计；现在按 0 计只留给“标的表里没有、在下架名单里、交易所确认不存在”三种情况，
// 停止交易可能恢复，按 0 计会让权益骤降、目标换算去缩仓，所以改成按最近成交价计（没有成交就报错，见下一个用例）。
func TestSampleAccountDegradesUnvaluableManagedAssets(t *testing.T) {
	tradeStore := seedDelistingAccount(t, "BREAK")
	haltFill(t, tradeStore)
	const now = sourceMillisDegraded + 2
	adapter := &delistingPriceAdapter{at: time.UnixMilli(sourceMillisDegraded + 1)}
	service := &Service{Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return time.UnixMilli(now) }}

	// 第一次：DEAD 还能取到价 2；HALT 停止交易，不发请求、没有最后已知价，按最近一笔成交价 7 计。
	require.NoError(t, service.SampleAccount(context.Background(), "account-1"))
	account, logical := latestEquity(t, tradeStore)
	require.Equal(t, "1034", account, "USDT 500 + BTC 0.01×50000 + DEAD 3×2 + HALT 4×7")
	require.Equal(t, "1034", logical)
	require.NotContains(t, adapter.seen, "HALTUSDT")
	require.NotEmpty(t, telemetry.Recent(telemetry.AttentionValuationDegraded, time.UnixMilli(now), time.Minute))

	// 之后 DEAD 下架：交易所回 -1121，沿用最后已知价 2，采样照常。
	adapter.delisted = true
	require.NoError(t, service.SampleAccount(context.Background(), "account-1"))
	account, _ = latestEquity(t, tradeStore)
	require.Equal(t, "1034", account)

	// 换一个没有最后已知价的服务实例：DEAD 按 0 计。
	fresh := &Service{Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return time.UnixMilli(now) }}
	require.NoError(t, fresh.SampleAccount(context.Background(), "account-1"))
	account, _ = latestEquity(t, tradeStore)
	require.Equal(t, "1028", account)
}

// 停止交易、不在下架名单里、没有最后已知价也没有成交记录：照旧报错，不按 0 计。
func TestSampleAccountFailsOnHaltedAssetWithoutAnyPrice(t *testing.T) {
	tradeStore := seedDelistingAccount(t, "BREAK")
	adapter := &delistingPriceAdapter{at: time.UnixMilli(sourceMillisDegraded + 1)}
	service := &Service{Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return time.UnixMilli(sourceMillisDegraded + 2) }}
	err := service.SampleAccount(context.Background(), "account-1")
	require.ErrorContains(t, err, "HALTUSDT 状态为 BREAK")
}

// 停止交易而且在下架名单里：可以按 0 计，采样照常。
func TestSampleAccountZeroesBlockedHaltedAsset(t *testing.T) {
	tradeStore := seedDelistingAccount(t, "BREAK")
	_, err := tradeStore.BlockInstrument(context.Background(), store.BlockedInstrumentRecord{
		Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "HALTUSDT", Reason: "下架", BlockedAt: 1,
	})
	require.NoError(t, err)
	adapter := &delistingPriceAdapter{at: time.UnixMilli(sourceMillisDegraded + 1)}
	service := &Service{Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return time.UnixMilli(sourceMillisDegraded + 2) }}
	require.NoError(t, service.SampleAccount(context.Background(), "account-1"))
	account, _ := latestEquity(t, tradeStore)
	require.Equal(t, "1006", account, "HALT 在下架名单里按 0 计")
}

// 最后已知价超过保留时间（权益过期阈值）就不再使用：标的仍在交易时照旧报错，交易所确认不存在时按 0 计。
func TestSampleAccountStopsUsingAgedLastPrice(t *testing.T) {
	tradeStore := seedDelistingAccount(t, "TRADING")
	now := time.UnixMilli(sourceMillisDegraded + 2)
	adapter := &delistingPriceAdapter{at: time.UnixMilli(sourceMillisDegraded + 1)}
	service := &Service{Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return now }}
	require.NoError(t, service.SampleAccount(context.Background(), "account-1"))

	// 限流且最后已知价还在保留时间内：沿用。
	adapter.throttled = true
	now = now.Add(60 * time.Second)
	require.NoError(t, refreshedSample(t, tradeStore, service, now))

	// 超过保留时间：不再沿用，照旧报错。
	now = now.Add(60 * time.Second)
	err := refreshedSample(t, tradeStore, service, now)
	require.True(t, exchange.IsKind(err, exchange.ErrorRateLimited), "err = %v", err)
}

// refreshedSample 把余额快照的时间推到 now 再采样（余额新鲜，只看报价的新鲜度）。
func refreshedSample(t *testing.T, tradeStore *store.Store, service *Service, now time.Time) error {
	t.Helper()
	require.NoError(t, tradeStore.DBForTest().Exec(`UPDATE t_trading_accounts SET c_last_sync_at = ? WHERE c_trading_account_id = 'account-1'`, now.UnixMilli()).Error)
	return service.SampleAccount(context.Background(), "account-1")
}

// 标的仍在交易、暂时取价失败（限流）且没有最后已知价时照旧报错，不把真实持仓按 0 计。
func TestSampleAccountKeepsFailingOnTransientQuoteErrorWithoutLastPrice(t *testing.T) {
	tradeStore := seedDelistingAccount(t, "TRADING")
	adapter := &delistingPriceAdapter{at: time.UnixMilli(sourceMillisDegraded + 1), throttled: true}
	service := &Service{Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return time.UnixMilli(sourceMillisDegraded + 2) }}
	err := service.SampleAccount(context.Background(), "account-1")
	require.True(t, exchange.IsKind(err, exchange.ErrorRateLimited), "err = %v", err)
}
