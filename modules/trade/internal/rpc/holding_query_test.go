package rpc

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestBalanceInstrumentIgnoresSwapInstrumentOfSameCoin(t *testing.T) {
	spot := store.InstrumentRecord{
		Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SPOT",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
	}
	swap := store.InstrumentRecord{
		Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SWAP",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
	}
	markets := []string{"SPOT", "SWAP"}
	// 无论现货和永续谁排在后面，余额都只对应现货标的
	for name, records := range map[string][]store.InstrumentRecord{
		"现货在前": {spot, swap},
		"永续在前": {swap, spot},
	} {
		index := holdingInstrumentIndex(records, "USDT")
		got, found := balanceInstrument(index, markets, "btc")
		if !found || got.InstrumentID != "BTC-USDT-SPOT" || got.MarketType != exchange.MarketTypeSpot {
			t.Fatalf("%s: balanceInstrument = %+v, %v，期望现货标的", name, got, found)
		}
	}
	// 只开通现货的账户，不会因为索引里有永续标的而多出匹配；计价币不是结算资产的标的不收
	if _, found := balanceInstrument(holdingInstrumentIndex([]store.InstrumentRecord{swap}, "USDT"), markets, "BTC"); found {
		t.Fatal("只有永续标的时，现货余额不应该配上永续标的")
	}
	if _, found := balanceInstrument(holdingInstrumentIndex([]store.InstrumentRecord{spot}, "USDC"), markets, "BTC"); found {
		t.Fatal("计价币不是结算资产的标的不应该被收进索引")
	}
}

// holdingAdaptersStub 按账户返回固定的适配器或错误。
type holdingAdaptersStub struct {
	adapter execution.ExecutionAdapter
	err     error
}

func (s holdingAdaptersStub) Adapter(string) (execution.ExecutionAdapter, error) {
	return s.adapter, s.err
}

// holdingQuoteStub 只实现持币查询用到的批量预取和逐只取价，按顺序记下调用。
type holdingQuoteStub struct {
	execution.ExecutionAdapter
	prices map[string]string
	calls  []string
}

func (s *holdingQuoteStub) PrefetchQuotes(_ context.Context, keys []exchange.SymbolKey) error {
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		names = append(names, key.String())
	}
	s.calls = append(s.calls, "prefetch "+strings.Join(names, ","))
	return nil
}

func (s *holdingQuoteStub) GetReferencePrice(_ context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	s.calls = append(s.calls, "quote "+key.String())
	price, found := s.prices[key.Symbol]
	if !found {
		return exchange.ReferencePrice{}, errors.New("没有报价")
	}
	return exchange.ReferencePrice{Price: shared.MustDecimal(price), UpdatedAt: time.UnixMilli(9_000).UTC()}, nil
}

func openHoldingStore(t *testing.T) *store.Store {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tradeStore.Close() })
	return tradeStore
}

// 普通账户合约钱包里的币是保证金，不是持币：现货 0.1 BNB、合约钱包 1 BNB 时只返回现货那一行。
func TestHoldingQueryListsOnlySpotWalletBalances(t *testing.T) {
	ctx := context.Background()
	tradeStore := openHoldingStore(t)
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "live-1", Name: "live", Exchange: "BINANCE", Markets: []string{"SPOT", "SWAP"},
			ExecutionMode: "LIVE", Environment: "PRODUCTION", AccountMode: string(exchange.AccountModeBinanceStandard),
			CredentialSecretID: "secret-1", SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED",
			LeverageSettings: store.LeverageSettings{"*": "5"},
			Snapshot: store.TradingAccountSnapshot{Balances: []store.AssetBalance{
				{Wallet: string(exchange.WalletSpot), Asset: "BNB", Total: "0.1", Available: "0.1"},
				{Wallet: string(exchange.WalletFutures), Asset: "BNB", Total: "1", Available: "1"},
			}, ExchangeUpdatedAt: 1_000},
		})
	}))
	query := &HoldingQuery{Store: tradeStore, Adapters: holdingAdaptersStub{err: errors.New("会话未启动")}}

	holdings, err := query.List(ctx, "space-1", "live-1")

	require.NoError(t, err)
	require.Len(t, holdings, 1)
	require.Equal(t, "BNB", holdings[0].Asset)
	require.Equal(t, "0.1", holdings[0].Quantity.String())
}

// 模拟盘：先一次批量预取全部要估值的标的，再逐只取价；均价一次读完成交计算，数值与原来分页读全量一致。
func TestHoldingQueryPaperPrefetchesQuotesAndKeepsAverageCost(t *testing.T) {
	ctx := context.Background()
	tradeStore := openHoldingStore(t)
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "paper-1", Name: "paper", Exchange: "BINANCE", Markets: []string{"SPOT"},
			ExecutionMode: "PAPER", SettlementAsset: "USDT", Status: "ENABLED",
			Snapshot: store.TradingAccountSnapshot{Balances: []store.AssetBalance{
				{Wallet: string(exchange.WalletUnified), Asset: "USDT", Total: "500", Available: "500"},
				{Wallet: string(exchange.WalletUnified), Asset: "BTC", Total: "3", Available: "3"},
				{Wallet: string(exchange.WalletUnified), Asset: "ETH", Total: "1", Available: "1"},
				{Wallet: string(exchange.WalletUnified), Asset: "DOGE", Total: "5", Available: "5"},
			}, ExchangeUpdatedAt: 1_000},
		}); err != nil {
			return err
		}
		for _, base := range []string{"BTC", "ETH"} {
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: base + "USDT", InstrumentID: base + "-USDT-SPOT",
				BaseAsset: base, QuoteAsset: "USDT", SettlementAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	sqlDB, err := tradeStore.SQLDB()
	require.NoError(t, err)
	_, err = sqlDB.Exec(`INSERT INTO t_trade_orders (c_space_id, c_order_id, c_trading_account_id, c_client_order_id, c_exchange, c_market_type, c_instrument_id,
		c_exchange_symbol, c_order_type, c_side, c_quantity, c_reference_price, c_reference_price_at, c_owner_type, c_owner_id, c_state)
		VALUES ('space-1', 'o1', 'paper-1', 'c1', 'BINANCE', 'SPOT', 'BTC-USDT-SPOT', 'BTCUSDT', 'MARKET', 'BUY', '1', '100', 1, 'EXTERNAL', 'ext', 'FILLED')`)
	require.NoError(t, err)
	// BTC：同一毫秒里有一买一卖，原来的次序是同一毫秒按成交号倒序（先卖后买），均价 120；先买后卖会算成 115。
	for _, fill := range []struct {
		id, symbol, side, price, quantity string
		at                                int64
	}{
		{"f-1", "BTCUSDT", "BUY", "100", "2", 1},
		{"f-a", "BTCUSDT", "BUY", "130", "2", 2},
		{"f-b", "BTCUSDT", "SELL", "120", "1", 2},
		{"f-eth", "ETHUSDT", "BUY", "10", "1", 5},
	} {
		_, err = sqlDB.Exec(`INSERT INTO t_order_fills (c_space_id, c_fill_id, c_exchange_trade_id, c_order_id, c_trading_account_id, c_exchange,
			c_market_type, c_instrument_id, c_exchange_symbol, c_side, c_price, c_quantity, c_traded_at)
			VALUES ('space-1', ?, ?, 'o1', 'paper-1', 'BINANCE', 'SPOT', 'x', ?, ?, ?, ?, ?)`,
			fill.id, fill.id, fill.symbol, fill.side, fill.price, fill.quantity, fill.at)
		require.NoError(t, err)
	}
	quotes := &holdingQuoteStub{prices: map[string]string{"BTCUSDT": "150"}}
	query := &HoldingQuery{Store: tradeStore, Adapters: holdingAdaptersStub{adapter: quotes}}

	holdings, err := query.List(ctx, "space-1", "paper-1")

	require.NoError(t, err)
	require.Equal(t, []string{"prefetch SPOT:BTCUSDT,SPOT:ETHUSDT", "quote SPOT:BTCUSDT", "quote SPOT:ETHUSDT"}, quotes.calls)
	require.Len(t, holdings, 4)
	byAsset := make(map[string]int, len(holdings))
	for i, holding := range holdings {
		byAsset[holding.Asset] = i
	}
	usdt, btc, eth, doge := holdings[byAsset["USDT"]], holdings[byAsset["BTC"]], holdings[byAsset["ETH"]], holdings[byAsset["DOGE"]]
	require.Equal(t, "1", usdt.MarkPrice.String())
	require.Equal(t, "500", usdt.MarketValue.String())

	require.Equal(t, "BTC-USDT-SPOT", btc.InstrumentID)
	require.Equal(t, "120", btc.AverageCost.String())
	require.Equal(t, "150", btc.MarkPrice.String())
	require.Equal(t, "450", btc.MarketValue.String())
	require.NotNil(t, btc.UnrealizedPnL)
	require.Equal(t, "90", btc.UnrealizedPnL.String())
	require.Equal(t, int64(9_000), btc.SourceTime.UnixMilli())

	require.Equal(t, "10", eth.AverageCost.String())
	require.True(t, eth.MarkPrice.IsZero(), "取不到报价不编价格")
	require.Nil(t, eth.UnrealizedPnL)

	require.Empty(t, doge.InstrumentID, "没有标的的资产照样展示，不估值")
	require.True(t, doge.MarkPrice.IsZero())

	// 与原来的做法（按时间倒序分页读出全部成交再算均价）逐项对比。
	for _, item := range []struct {
		symbol string
		got    shared.Decimal
	}{{"BTCUSDT", btc.AverageCost}, {"ETHUSDT", eth.AverageCost}} {
		var all []store.FillRecord
		for offset := 0; ; offset++ {
			page, total, pageErr := tradeStore.ListFills(ctx, "space-1", store.FillQuery{
				TradingAccountID: "paper-1", Market: "SPOT", ExchangeSymbol: item.symbol, Offset: offset, Limit: 1,
			})
			require.NoError(t, pageErr)
			all = append(all, page...)
			if len(page) == 0 || int64(offset+len(page)) >= total {
				break
			}
		}
		require.Equal(t, paperAverageCostFromFills(all).String(), item.got.String(), item.symbol)
	}
}
