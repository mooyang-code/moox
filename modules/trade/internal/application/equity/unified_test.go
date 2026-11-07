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
