package position

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

type stubQuoteAdapter struct {
	execution.ExecutionAdapter
	quotes map[exchange.SymbolKey]execution.MarketQuote
}

func (a stubQuoteAdapter) GetQuote(_ context.Context, symbol exchange.SymbolKey) (execution.MarketQuote, error) {
	return a.quotes[symbol], nil
}

func (a stubQuoteAdapter) GetReferencePrice(_ context.Context, symbol exchange.SymbolKey) (exchange.ReferencePrice, error) {
	q := a.quotes[symbol]
	return exchange.ReferencePrice{Price: q.Last, UpdatedAt: q.SourceTime}, nil
}

type stubAdapterSource struct {
	adapters map[string]execution.ExecutionAdapter
}

func (s stubAdapterSource) Adapter(tradingAccountID string) (execution.ExecutionAdapter, error) {
	adapter, ok := s.adapters[tradingAccountID]
	if !ok {
		return nil, exchange.ErrInvalidInstrumentID
	}
	return adapter, nil
}

func TestQueryListUnifiesSwapAndSpotPositionsAndIncludesDisabledMembers(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		for _, acc := range []store.TradingAccountRecord{
			{
				SpaceID: "space-1", TradingAccountID: "acc-1", Name: "acc-1",
				Exchange: "binance", Markets: []string{"spot", "swap"},
				ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT",
				MarginMode: "CROSS", Status: "ENABLED", LeverageSettings: store.LeverageSettings{"*": "3"},
				Snapshot: store.TradingAccountSnapshot{
					Balances: []store.AssetBalance{
						{Wallet: "UNIFIED", Asset: "USDT", Available: "1000", Total: "1000"},
						{Wallet: "UNIFIED", Asset: "ETH", Available: "2", Total: "2"},
					},
					ExchangeUpdatedAt: now.UnixMilli(),
				},
			},
			{
				SpaceID: "space-1", TradingAccountID: "acc-disabled", Name: "acc-disabled",
				Exchange: "binance", Markets: []string{"spot", "swap"},
				ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT",
				MarginMode: "CROSS", Status: "ENABLED", LeverageSettings: store.LeverageSettings{"*": "3"},
				Snapshot: store.TradingAccountSnapshot{
					Balances: []store.AssetBalance{
						{Wallet: "UNIFIED", Asset: "SOL", Available: "5", Total: "5"},
					},
					ExchangeUpdatedAt: now.UnixMilli(),
				},
			},
		} {
			if err := tx.CreateTradingAccount(acc); err != nil {
				return err
			}
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical-1",
			Environment: "PAPER", SettlementAsset: "USDT", PauseReason: "init",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "acc-1", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "acc-disabled", Enabled: false, Priority: 2,
		}); err != nil {
			return err
		}
		for _, inst := range []store.InstrumentRecord{
			{
				Exchange: "binance", Environment: "PRODUCTION", MarketType: "spot",
				ExchangeSymbol: "ETHUSDT", InstrumentID: "ETH-USDT-SPOT",
				BaseAsset: "ETH", QuoteAsset: "USDT", SettlementAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			},
			{
				Exchange: "binance", Environment: "PRODUCTION", MarketType: "spot",
				ExchangeSymbol: "SOLUSDT", InstrumentID: "SOL-USDT-SPOT",
				BaseAsset: "SOL", QuoteAsset: "USDT", SettlementAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			},
		} {
			if err := tx.UpsertInstrument(inst); err != nil {
				return err
			}
		}
		// 写入非零永续（含停用成员的未平仓永续）与零永续，零永续不应出现在结果中。
		for _, pos := range []store.PositionRecord{
			{
				SpaceID: "space-1", TradingAccountID: "acc-1", MarketType: "swap",
				InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT", PositionSide: "NET",
				SignedQuantity: "0.5", EntryPrice: "60000", MarkPrice: "62000", Leverage: "3",
				MarginMode: "CROSS", UsedMargin: "10000", UnrealizedPnL: "1000", ExchangeUpdatedAt: now.UnixMilli(),
			},
			{
				SpaceID: "space-1", TradingAccountID: "acc-1", MarketType: "swap",
				InstrumentID: "DOGE-USDT-SWAP", ExchangeSymbol: "DOGEUSDT", PositionSide: "NET",
				SignedQuantity: "0", EntryPrice: "0", MarkPrice: "0", Leverage: "3", ExchangeUpdatedAt: now.UnixMilli(),
			},
			{
				SpaceID: "space-1", TradingAccountID: "acc-disabled", MarketType: "swap",
				InstrumentID: "XRP-USDT-SWAP", ExchangeSymbol: "XRPUSDT", PositionSide: "NET",
				SignedQuantity: "100", EntryPrice: "0.5", MarkPrice: "0.6", Leverage: "3",
				MarginMode: "CROSS", UsedMargin: "20", UnrealizedPnL: "10", ExchangeUpdatedAt: now.UnixMilli(),
			},
		} {
			if err := tx.UpsertPosition(pos); err != nil {
				return err
			}
		}
		// 写入现货买入成交，供平均成本与浮盈亏计算。
		if err := tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "ord-eth", TradingAccountID: "acc-1", ClientOrderID: "c-eth",
			Exchange: "binance", MarketType: "spot", InstrumentID: "ETH-USDT-SPOT", ExchangeSymbol: "ETHUSDT",
			OrderType: "MARKET", Side: "BUY", Quantity: "2", FilledQuantity: "2", ReferencePrice: "2000",
			ReferencePriceAt: now.UnixMilli(), OwnerType: "OPERATOR", OwnerID: "act-1", LogicalAccountID: "logical-1",
			State: "FILLED", Version: 1,
		}); err != nil {
			return err
		}
		_, err := tx.InsertFill(store.FillRecord{
			SpaceID: "space-1", FillID: "fill-eth", ExchangeTradeID: "tr-eth", OrderID: "ord-eth",
			TradingAccountID: "acc-1", Exchange: "binance", MarketType: "spot",
			InstrumentID: "ETH-USDT-SPOT", ExchangeSymbol: "ETHUSDT", Side: "BUY",
			Price: "2000", Quantity: "2", Fee: "0", FeeAsset: "USDT", SettlementAsset: "USDT",
			RealizedPnL: "0", TradedAt: now.Add(-time.Hour).UnixMilli(),
		})
		return err
	}))

	query := &Query{
		Store: db.ReadOnly(),
		Adapters: stubAdapterSource{adapters: map[string]execution.ExecutionAdapter{
			"acc-1": stubQuoteAdapter{quotes: map[exchange.SymbolKey]execution.MarketQuote{
				exchange.NewSymbolKey(exchange.MarketTypeSpot, "ETHUSDT"): {
					Bid: shared.MustDecimal("2500"), Ask: shared.MustDecimal("2500"), Last: shared.MustDecimal("2500"), SourceTime: now,
				},
			}},
		}},
	}

	// 按组合账户查：返回启用成员的永续（BTC-USDT-SWAP）、结算资产余额（USDT）与现货（ETH-USDT-SPOT），
	// 同时包含停用成员仍持有的永续（XRP-USDT-SWAP）与现货（SOL-USDT-SPOT），不含零仓位（DOGE）。
	rows, err := query.List(ctx, ListRequest{SpaceID: "space-1", LogicalAccountID: "logical-1"})
	require.NoError(t, err)
	require.Len(t, rows, 5)

	byKey := map[string]Position{}
	for _, row := range rows {
		key := row.InstrumentID
		if key == "" {
			key = row.Asset
		}
		byKey[key] = row
	}
	btc := byKey["BTC-USDT-SWAP"]
	require.Equal(t, "swap", btc.MarketType)
	require.Equal(t, "0.5", btc.SignedQuantity)
	require.Equal(t, "31000", btc.MarketValue)
	require.NotNil(t, btc.UnrealizedPnL)
	require.Equal(t, "1000", *btc.UnrealizedPnL)

	usdt := byKey["USDT"]
	require.Equal(t, "spot", usdt.MarketType)
	require.Empty(t, usdt.InstrumentID)
	require.Equal(t, "1000", usdt.SignedQuantity)
	require.Equal(t, "1000", usdt.MarketValue)
	require.Nil(t, usdt.UnrealizedPnL)

	eth := byKey["ETH-USDT-SPOT"]
	require.Equal(t, "spot", eth.MarketType)
	require.Equal(t, "ETH", eth.Asset)
	require.Equal(t, "2", eth.SignedQuantity)
	require.Equal(t, "2000", eth.EntryPrice)
	require.Equal(t, "2500", eth.MarkPrice)
	require.Equal(t, "5000", eth.MarketValue)
	require.Equal(t, "2", eth.Sellable)
	require.NotNil(t, eth.UnrealizedPnL)
	require.Equal(t, "1000", *eth.UnrealizedPnL)

	xrp := byKey["XRP-USDT-SWAP"]
	require.Equal(t, "acc-disabled", xrp.TradingAccountID)
	require.Equal(t, "swap", xrp.MarketType)
	require.Equal(t, "100", xrp.SignedQuantity)

	sol := byKey["SOL-USDT-SPOT"]
	require.Equal(t, "acc-disabled", sol.TradingAccountID)
	require.Equal(t, "spot", sol.MarketType)
	require.Equal(t, "5", sol.SignedQuantity)

	// 按 instrument_id 过滤。
	filtered, err := query.List(ctx, ListRequest{SpaceID: "space-1", LogicalAccountID: "logical-1", InstrumentID: "ETH-USDT-SPOT"})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Equal(t, "ETH-USDT-SPOT", filtered[0].InstrumentID)

	// 同时传不属于该组合的执行账户时返回 ErrNotLogicalMember。
	_, err = query.List(ctx, ListRequest{SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "non-member"})
	require.ErrorIs(t, err, ErrNotLogicalMember)
}

func TestBalanceInstrumentIgnoresSwapInstrumentOfSameCoin(t *testing.T) {
	spot := store.InstrumentRecord{
		Exchange: "binance", MarketType: "spot", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SPOT",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
	}
	swap := store.InstrumentRecord{
		Exchange: "binance", MarketType: "swap", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SWAP",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
	}
	markets := []string{"spot", "swap"}
	for name, records := range map[string][]store.InstrumentRecord{
		"现货在前": {spot, swap},
		"永续在前": {swap, spot},
	} {
		index := balanceInstrumentIndex(records, "USDT")
		got, found := balanceInstrument(index, markets, "btc")
		require.True(t, found, name)
		require.Equal(t, "BTC-USDT-SPOT", got.InstrumentID, name)
		require.Equal(t, exchange.MarketTypeSpot, got.MarketType, name)
	}
	_, found := balanceInstrument(balanceInstrumentIndex([]store.InstrumentRecord{swap}, "USDT"), markets, "BTC")
	require.False(t, found, "只有永续标的时，现货余额不应该配上永续标的")
	_, found = balanceInstrument(balanceInstrumentIndex([]store.InstrumentRecord{spot}, "USDC"), markets, "BTC")
	require.False(t, found, "计价币不是结算资产的标的不应该被收进索引")
}

type prefetchQuoteStub struct {
	execution.ExecutionAdapter
	prices map[string]string
	calls  []string
}

func (s *prefetchQuoteStub) PrefetchQuotes(_ context.Context, keys []exchange.SymbolKey) error {
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		names = append(names, key.String())
	}
	s.calls = append(s.calls, "prefetch "+strings.Join(names, ","))
	return nil
}

func (s *prefetchQuoteStub) GetReferencePrice(_ context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	s.calls = append(s.calls, "quote "+key.String())
	price, found := s.prices[key.Symbol]
	if !found {
		return exchange.ReferencePrice{}, errors.New("没有报价")
	}
	return exchange.ReferencePrice{Price: shared.MustDecimal(price), UpdatedAt: time.UnixMilli(9_000).UTC()}, nil
}

// 普通账户合约钱包里的币是保证金，不是持币：现货 0.1 BNB、合约钱包 1 BNB 时只返回现货那一行。
func TestQueryListsOnlySpotWalletBalances(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "live-1", Name: "live", Exchange: "binance", Markets: []string{"spot", "swap"},
			ExecutionMode: "LIVE", Environment: "PRODUCTION", AccountMode: string(exchange.AccountModeBinanceStandard),
			CredentialSecretID: "secret-1", SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED",
			LeverageSettings: store.LeverageSettings{"*": "5"},
			Snapshot: store.TradingAccountSnapshot{Balances: []store.AssetBalance{
				{Wallet: string(exchange.WalletSpot), Asset: "BNB", Total: "0.1", Available: "0.1"},
				{Wallet: string(exchange.WalletFutures), Asset: "BNB", Total: "1", Available: "1"},
			}, ExchangeUpdatedAt: 1_000},
		})
	}))
	query := &Query{Store: db, Adapters: stubAdapterSource{}}

	rows, err := query.List(ctx, ListRequest{SpaceID: "space-1", TradingAccountID: "live-1"})

	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "BNB", rows[0].Asset)
	require.Equal(t, "0.1", rows[0].SignedQuantity)
}

// 模拟盘：先一次批量预取全部要估值的标的，再逐只取价；均价读标的汇总。
func TestQueryPaperPrefetchesQuotesAndKeepsAverageCost(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "paper-1", Name: "paper", Exchange: "binance", Markets: []string{"spot"},
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
				Exchange: "binance", MarketType: "spot", ExchangeSymbol: base + "USDT", InstrumentID: base + "-USDT-SPOT",
				BaseAsset: base, QuoteAsset: "USDT", SettlementAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		for _, order := range []struct{ base, side string }{{"BTC", "BUY"}, {"BTC", "SELL"}, {"ETH", "BUY"}} {
			id := order.base + "-" + order.side
			if err := tx.CreateOrder(store.OrderRecord{
				SpaceID: "space-1", OrderID: "o-" + id, TradingAccountID: "paper-1", ClientOrderID: "c-" + id,
				Exchange: "binance", MarketType: "spot", InstrumentID: order.base + "-USDT-SPOT", ExchangeSymbol: order.base + "USDT",
				OrderType: "MARKET", Side: order.side, Quantity: "1", ReferencePrice: "100", ReferencePriceAt: 1,
				OwnerType: "EXTERNAL", OwnerID: "ext-" + id, State: "FILLED", Version: 1,
			}); err != nil {
				return err
			}
		}
		for _, fill := range []struct {
			id, base, side, price, quantity string
			at                              int64
		}{
			{"f-1", "BTC", "BUY", "100", "2", 1},
			{"f-b", "BTC", "SELL", "120", "1", 2},
			{"f-a", "BTC", "BUY", "130", "2", 2},
			{"f-eth", "ETH", "BUY", "10", "1", 5},
		} {
			if _, err := tx.InsertFill(store.FillRecord{
				SpaceID: "space-1", FillID: fill.id, ExchangeTradeID: fill.id, OrderID: "o-" + fill.base + "-" + fill.side,
				Side: fill.side, Price: fill.price, Quantity: fill.quantity, Fee: "0", FeeAsset: "USDT",
				SettlementAsset: "USDT", RealizedPnL: "0", TradedAt: fill.at,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	quotes := &prefetchQuoteStub{prices: map[string]string{"BTCUSDT": "150"}}
	query := &Query{Store: db, Adapters: stubAdapterSource{adapters: map[string]execution.ExecutionAdapter{"paper-1": quotes}}}

	rows, err := query.List(ctx, ListRequest{SpaceID: "space-1", TradingAccountID: "paper-1"})

	require.NoError(t, err)
	require.Equal(t, []string{"prefetch spot:BTCUSDT,spot:ETHUSDT", "quote spot:BTCUSDT", "quote spot:ETHUSDT"}, quotes.calls)
	require.Len(t, rows, 4)
	byAsset := make(map[string]Position, len(rows))
	for _, row := range rows {
		byAsset[row.Asset] = row
	}
	usdt, btc, eth, doge := byAsset["USDT"], byAsset["BTC"], byAsset["ETH"], byAsset["DOGE"]
	require.Equal(t, "1", usdt.MarkPrice)
	require.Equal(t, "500", usdt.MarketValue)

	require.Equal(t, "BTC-USDT-SPOT", btc.InstrumentID)
	require.Equal(t, "120", btc.EntryPrice)
	require.Equal(t, "150", btc.MarkPrice)
	require.Equal(t, "450", btc.MarketValue)
	require.NotNil(t, btc.UnrealizedPnL)
	require.Equal(t, "90", *btc.UnrealizedPnL)
	require.Equal(t, int64(9_000), btc.ExchangeUpdatedAt)

	require.Equal(t, "10", eth.EntryPrice)
	require.Empty(t, eth.MarkPrice, "取不到报价不编价格")
	require.Nil(t, eth.UnrealizedPnL)

	require.Empty(t, doge.InstrumentID, "没有标的的资产照样展示，不估值")
	require.Empty(t, doge.MarkPrice)
}
