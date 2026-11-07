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

// 受管资产取不到价（下架后 -1121、标的停止交易）时权益采样不失败：有最后已知价用它，没有按 0 计，并记关注项。
func TestSampleAccountDegradesUnvaluableManagedAssets(t *testing.T) {
	tradeStore := seedDelistingAccount(t, "BREAK")
	const now = sourceMillisDegraded + 2
	adapter := &delistingPriceAdapter{at: time.UnixMilli(sourceMillisDegraded + 1)}
	service := &Service{Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return time.UnixMilli(now) }}

	// 第一次：DEAD 还能取到价 2；HALT 停止交易，不发请求、没有最后已知价，按 0 计。
	require.NoError(t, service.SampleAccount(context.Background(), "account-1"))
	account, logical := latestEquity(t, tradeStore)
	require.Equal(t, "1006", account, "USDT 500 + BTC 0.01×50000 + DEAD 3×2 + HALT 0")
	require.Equal(t, "1006", logical)
	require.NotContains(t, adapter.seen, "HALTUSDT")
	require.NotEmpty(t, telemetry.Recent(telemetry.AttentionValuationDegraded, time.UnixMilli(now), time.Minute))

	// 之后 DEAD 下架：交易所回 -1121，沿用最后已知价 2，采样照常。
	adapter.delisted = true
	require.NoError(t, service.SampleAccount(context.Background(), "account-1"))
	account, _ = latestEquity(t, tradeStore)
	require.Equal(t, "1006", account)

	// 换一个没有最后已知价的服务实例：DEAD 按 0 计。
	fresh := &Service{Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return time.UnixMilli(now) }}
	require.NoError(t, fresh.SampleAccount(context.Background(), "account-1"))
	account, _ = latestEquity(t, tradeStore)
	require.Equal(t, "1000", account)
}

// 标的仍在交易、暂时取价失败（限流）且没有最后已知价时照旧报错，不把真实持仓按 0 计。
func TestSampleAccountKeepsFailingOnTransientQuoteErrorWithoutLastPrice(t *testing.T) {
	tradeStore := seedDelistingAccount(t, "TRADING")
	adapter := &delistingPriceAdapter{at: time.UnixMilli(sourceMillisDegraded + 1), throttled: true}
	service := &Service{Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return time.UnixMilli(sourceMillisDegraded + 2) }}
	err := service.SampleAccount(context.Background(), "account-1")
	require.True(t, exchange.IsKind(err, exchange.ErrorRateLimited), "err = %v", err)
}
