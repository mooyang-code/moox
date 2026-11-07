package equity

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// perMarketPriceAdapter 现货和合约各有自己的报价，并记下取过价的标的。
type perMarketPriceAdapter struct {
	equityTestAdapter
	at   time.Time
	seen []exchange.SymbolKey
}

func (a *perMarketPriceAdapter) GetReferencePrice(_ context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	a.seen = append(a.seen, key)
	price := "50000"
	if key.Market == exchange.MarketTypeSwap {
		price = "51000"
	}
	return exchange.ReferencePrice{Price: shared.MustDecimal(price), UpdatedAt: a.at}, nil
}

// 统一权益公式：结算资产各钱包净额 + 永续未实现盈亏 + 受管现货市值 + 保证金币配置值；
// 交易所给的总权益只展示不参与；现货币按现货报价估值，不会取到同名合约的价格。
func TestUnifiedEquityFormulaOnStandardAccount(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer tradeStore.Close()

	const sourceMillis = int64(1_700_000_000_000)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "live",
			Exchange: string(exchange.ExchangeBinance), Markets: []string{"SPOT", "SWAP"}, AccountMode: string(exchange.AccountModeBinanceStandard),
			ExecutionMode: string(exchange.ExecutionModeLive), Environment: string(exchange.AccountEnvironmentProduction),
			CredentialSecretID: "secret-1", SettlementAsset: "USDT", MarginMode: "CROSS", LeverageSettings: store.LeverageSettings{"*": "5"},
			Status: string(exchange.AccountStatusEnabled), Ready: true,
			Snapshot: store.TradingAccountSnapshot{
				Balances: []store.AssetBalance{
					{Wallet: "SPOT", Asset: "USDT", Available: "500", Total: "500"},
					{Wallet: "SPOT", Asset: "BTC", Available: "0.01", Total: "0.01"},
					{Wallet: "SPOT", Asset: "DOGE", Available: "1000", Total: "1000"}, // 用户自持、从没交易过：不受管，不估值
					{Wallet: "FUTURES", Asset: "USDT", Available: "900", Total: "1000"},
				},
				UnrealizedPnL: "20", ExchangeEquity: "99999", Equity: "99999", ExchangeUpdatedAt: sourceMillis,
			},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical", ExecutionMode: string(exchange.ExecutionModeLive),
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "test",
			FundSettingsJSON: `{"margin_coins":{"ETH":"100"}}`,
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true,
		}); err != nil {
			return err
		}
		// 合约标的后写入：同名的合约不能顶替现货标的去给 BTC 估值。
		for _, market := range []string{"SPOT", "SWAP"} {
			record := store.InstrumentRecord{
				Exchange: string(exchange.ExchangeBinance), Environment: string(exchange.AccountEnvironmentProduction),
				MarketType: market, ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-" + market,
				BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001",
				PriceTick: "0.1", Status: "TRADING",
			}
			if market == "SWAP" {
				record.Linear, record.ContractValue, record.ContractValueAsset = true, "1", "BTC"
			}
			if err := tx.UpsertInstrument(record); err != nil {
				return err
			}
		}
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", OrderID: "bought", ClientOrderID: "bought",
			Exchange: string(exchange.ExchangeBinance), MarketType: "SPOT",
			ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SPOT", OrderType: "MARKET", Side: "BUY",
			Quantity: "0.01", FilledQuantity: "0.01", ReferencePrice: "50000", State: "FILLED",
			OwnerType: "OPERATOR", OwnerID: "action-1", LogicalAccountID: "logical-1", Version: 1,
		})
	}))
	adapter := &perMarketPriceAdapter{at: time.UnixMilli(sourceMillis + 1)}
	service := &Service{Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return time.UnixMilli(sourceMillis + 2) }}

	require.NoError(t, service.SampleAccount(context.Background(), "account-1"))

	points, err := tradeStore.ListAccountEquityPoints(context.Background(), "space-1", "account-1", 0, 0)
	require.NoError(t, err)
	require.Len(t, points, 1)
	// USDT 500 + 1000 + 未实现盈亏 20 + 现货 BTC 0.01 × 50000；保证金币配置值在组合层加
	require.Equal(t, "2020", points[0].Equity)
	logicalPoints, err := tradeStore.ListLogicalAccountEquityPoints(context.Background(), "space-1", "logical-1", 0, 0)
	require.NoError(t, err)
	require.Len(t, logicalPoints, 1)
	require.Equal(t, "2120", logicalPoints[0].Equity, "组合权益再加保证金币 ETH 的配置值 100，只加一次")
	require.Equal(t, []exchange.SymbolKey{{Market: exchange.MarketTypeSpot, Symbol: "BTCUSDT"}}, adapter.seen, "只取现货报价")
}

// 统一账户借款算负债：结算资产净额 = 余额 - 借款。
func TestUnifiedEquitySubtractsBorrowedSettlement(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer tradeStore.Close()
	const sourceMillis = int64(1_700_000_000_000)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "live",
			Exchange: string(exchange.ExchangeBinance), Markets: []string{"SPOT", "SWAP"}, AccountMode: string(exchange.AccountModeBinancePortfolioMargin),
			ExecutionMode: string(exchange.ExecutionModeLive), Environment: string(exchange.AccountEnvironmentProduction),
			CredentialSecretID: "secret-1", SettlementAsset: "USDT", MarginMode: "CROSS", LeverageSettings: store.LeverageSettings{"*": "5"},
			Status: string(exchange.AccountStatusEnabled), Ready: true,
			Snapshot: store.TradingAccountSnapshot{
				Balances: []store.AssetBalance{
					{Wallet: "MARGIN", Asset: "USDT", Available: "300", Total: "300", Borrowed: "100"},
					{Wallet: "UNIFIED", Asset: "USDT", Available: "800", Total: "1000"},
				},
				UnrealizedPnL: "-50", ExchangeUpdatedAt: sourceMillis,
			},
		})
	}))
	service := &Service{Store: tradeStore, Now: func() time.Time { return time.UnixMilli(sourceMillis + 2) }}

	require.NoError(t, service.SampleAccount(context.Background(), "account-1"))

	points, err := tradeStore.ListAccountEquityPoints(context.Background(), "space-1", "account-1", 0, 0)
	require.NoError(t, err)
	require.Len(t, points, 1)
	require.Equal(t, "1150", points[0].Equity, "(300 - 100) + 1000 - 50")
}

// 缺结算币明细的告警按账户限频：权益采样每 20 秒一次，同一个账户一小时只告警一次。
func TestMissingSettlementWarningIsRateLimited(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	service := &Service{Now: func() time.Time { return now }}

	require.True(t, service.shouldWarnMissingSettlement("account-1"))
	now = now.Add(20 * time.Second)
	require.False(t, service.shouldWarnMissingSettlement("account-1"))
	require.True(t, service.shouldWarnMissingSettlement("account-2"))
	now = now.Add(time.Hour)
	require.True(t, service.shouldWarnMissingSettlement("account-1"))
}

// symbolPriceAdapter 按交易所代码给固定报价。
type symbolPriceAdapter struct {
	equityTestAdapter
	at     time.Time
	prices map[string]string
}

func (a *symbolPriceAdapter) GetReferencePrice(_ context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	price, ok := a.prices[key.Symbol]
	if !ok {
		return exchange.ReferencePrice{}, context.DeadlineExceeded
	}
	return exchange.ReferencePrice{Price: shared.MustDecimal(price), UpdatedAt: a.at}, nil
}

func seedUnifiedAccount(t *testing.T, tradeStore *store.Store, fundSettings string, balances []store.AssetBalance, unrealized string, sourceMillis int64) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "live",
			Exchange: string(exchange.ExchangeBinance), Markets: []string{"SPOT", "SWAP"}, AccountMode: string(exchange.AccountModeBinancePortfolioMargin),
			ExecutionMode: string(exchange.ExecutionModeLive), Environment: string(exchange.AccountEnvironmentProduction),
			CredentialSecretID: "secret-1", SettlementAsset: "USDT", MarginMode: "CROSS", LeverageSettings: store.LeverageSettings{"*": "5"},
			Status: string(exchange.AccountStatusEnabled), Ready: true,
			Snapshot: store.TradingAccountSnapshot{Balances: balances, UnrealizedPnL: unrealized, ExchangeUpdatedAt: sourceMillis},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical", ExecutionMode: string(exchange.ExecutionModeLive),
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "test", FundSettingsJSON: fundSettings,
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true,
		}); err != nil {
			return err
		}
		for _, base := range []string{"BTC", "USDC", "ETH"} {
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: string(exchange.ExchangeBinance), Environment: string(exchange.AccountEnvironmentProduction),
				MarketType: "SPOT", ExchangeSymbol: base + "USDT", InstrumentID: base + "-USDT-SPOT",
				BaseAsset: base, QuoteAsset: "USDT", SettlementAsset: "USDT", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001",
				PriceTick: "0.0001", Status: "TRADING",
			}); err != nil {
				return err
			}
		}
		return nil
	}))
}

func sampleEquity(t *testing.T, tradeStore *store.Store, prices map[string]string, sourceMillis int64) (account, logical string) {
	t.Helper()
	adapter := &symbolPriceAdapter{at: time.UnixMilli(sourceMillis + 1), prices: prices}
	service := &Service{Store: tradeStore, Adapters: equityTestAdapters{adapter: adapter}, Now: func() time.Time { return time.UnixMilli(sourceMillis + 2) }}
	require.NoError(t, service.SampleAccount(context.Background(), "account-1"))
	points, err := tradeStore.ListAccountEquityPoints(context.Background(), "space-1", "account-1", 0, 0)
	require.NoError(t, err)
	require.Len(t, points, 1)
	logicalPoints, err := tradeStore.ListLogicalAccountEquityPoints(context.Background(), "space-1", "logical-1", 0, 0)
	require.NoError(t, err)
	require.Len(t, logicalPoints, 1)
	return points[0].Equity, logicalPoints[0].Equity
}

// 多稳定币保证金：非结算币的稳定币按它对结算币的现货价折算后计入权益，调平前后权益不会跳变。
func TestMultiStablecoinBalancesCountTowardEquity(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer tradeStore.Close()
	const sourceMillis = int64(1_700_000_000_000)
	seedUnifiedAccount(t, tradeStore, `{"stablecoin_margin":{"USDT":"0.6","USDC":"0.4"}}`, []store.AssetBalance{
		{Wallet: "MARGIN", Asset: "USDT", Available: "600", Total: "600"},
		{Wallet: "MARGIN", Asset: "USDC", Available: "400", Total: "400"},
	}, "0", sourceMillis)
	account, _ := sampleEquity(t, tradeStore, map[string]string{"USDCUSDT": "0.9995"}, sourceMillis)
	require.Equal(t, "999.8", account, "600 + 400 × 0.9995")
}

// 取不到稳定币报价时按 1 计，不中断权益采样。
func TestStablecoinWithoutAQuoteIsValuedAtOne(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer tradeStore.Close()
	const sourceMillis = int64(1_700_000_000_000)
	seedUnifiedAccount(t, tradeStore, `{"stablecoin_margin":{"USDT":"0.6","USDC":"0.4"}}`, []store.AssetBalance{
		{Wallet: "MARGIN", Asset: "USDT", Available: "600", Total: "600"},
		{Wallet: "MARGIN", Asset: "USDC", Available: "400", Total: "400"},
	}, "0", sourceMillis)
	account, _ := sampleEquity(t, tradeStore, map[string]string{}, sourceMillis)
	require.Equal(t, "1000", account)
}

// 没配置稳定币保证金时，其他稳定币不是受管资产，不计入权益（和以前一致）。
func TestStablecoinIsIgnoredWithoutTheSetting(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer tradeStore.Close()
	const sourceMillis = int64(1_700_000_000_000)
	seedUnifiedAccount(t, tradeStore, `{}`, []store.AssetBalance{
		{Wallet: "MARGIN", Asset: "USDT", Available: "600", Total: "600"},
		{Wallet: "MARGIN", Asset: "USDC", Available: "400", Total: "400"},
	}, "0", sourceMillis)
	account, _ := sampleEquity(t, tradeStore, map[string]string{"USDCUSDT": "1"}, sourceMillis)
	require.Equal(t, "600", account)
}

// 保证金币只按配置值计入权益（组合层加一次），账户里它的实际余额不计入；
// 套利底仓按净值计入：底仓现货市值 + 对冲腿的未实现盈亏（后者已在永续未实现盈亏里）。
func TestMarginCoinsAndArbitrageBaseFollowThePlanFormula(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer tradeStore.Close()
	const sourceMillis = int64(1_700_000_000_000)
	seedUnifiedAccount(t, tradeStore, `{"margin_coins":{"ETH":"100"},"arbitrage_base_assets":["BTC"]}`, []store.AssetBalance{
		{Wallet: "MARGIN", Asset: "USDT", Available: "500", Total: "500"},
		{Wallet: "UNIFIED", Asset: "USDT", Available: "400", Total: "400"},
		{Wallet: "MARGIN", Asset: "BTC", Available: "0.1", Total: "0.1"},
		{Wallet: "MARGIN", Asset: "ETH", Available: "10", Total: "10"},
	}, "-300", sourceMillis)
	account, logical := sampleEquity(t, tradeStore, map[string]string{"BTCUSDT": "50000", "ETHUSDT": "3000"}, sourceMillis)
	// 500 + 400 + 对冲腿盈亏 -300 + 底仓 BTC 0.1 × 50000；ETH 的实际余额（10 个 = 30000）不计入。
	require.Equal(t, "5600", account)
	require.Equal(t, "5700", logical, "组合层再加 ETH 的配置值 100")
}
