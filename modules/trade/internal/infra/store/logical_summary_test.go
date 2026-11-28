package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func TestBatchLogicalAccountSummariesAndListOrdersForLaneOwnerFilter(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	require.NoError(t, st.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(TradingAccountRecord{
			SpaceID: "s1", TradingAccountID: "acc-1", Name: "acc-1",
			Exchange: string(exchange.ExchangeBinance), Markets: []string{"spot", "swap"},
			ExecutionMode: string(exchange.ExecutionModePaper), Environment: string(exchange.AccountEnvironmentPaper), SettlementAsset: "USDT",
			MarginMode: "CROSS", LeverageSettings: LeverageSettings{"*": "3"}, Status: "ENABLED",
			Snapshot: TradingAccountSnapshot{
				Equity: "1150",
				Balances: []AssetBalance{
					{Wallet: "UNIFIED", Asset: "USDT", Total: "1000", Available: "1000"},
					{Wallet: "UNIFIED", Asset: "BTC", Total: "0.5", Available: "0.5"},
					{Wallet: "UNIFIED", Asset: "ETH", Total: "1", Available: "1"},
					{Wallet: "UNIFIED", Asset: "ZERO", Total: "0", Available: "0"},
				},
			},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "s1", LogicalAccountID: "log-1", Name: "log-1",
			Environment: string(exchange.AccountEnvironmentPaper), SettlementAsset: "USDT",
			PauseReason: "init",
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "s1", LogicalAccountID: "log-2", Name: "log-2",
			Environment: string(exchange.AccountEnvironmentPaper), SettlementAsset: "USDT",
			PauseReason: "init",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "s1", LogicalAccountID: "log-1", TradingAccountID: "acc-1", Enabled: true,
		}); err != nil {
			return err
		}
		for _, base := range []string{"BTC", "ETH", "SOL"} {
			if err := tx.UpsertInstrument(InstrumentRecord{
				Exchange: string(exchange.ExchangeBinance), Environment: string(exchange.AccountEnvironmentProduction),
				MarketType: "spot", ExchangeSymbol: base + "USDT", InstrumentID: base + "-USDT-SPOT",
				BaseAsset: base, QuoteAsset: "USDT", SettlementAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			}); err != nil {
				return err
			}
		}
		// BTC 同时有现货余额和永续持仓，按币去重只算 1 个；SOL 只有永续；ETH 只有现货 => 共 3 个标的。
		for _, pos := range []PositionRecord{
			{
				SpaceID: "s1", TradingAccountID: "acc-1", MarketType: "swap",
				InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT", PositionSide: "NET",
				SignedQuantity: "0.2", EntryPrice: "50000", MarkPrice: "50000", Leverage: "3",
				ExchangeUpdatedAt: now.UnixMilli(),
			},
			{
				SpaceID: "s1", TradingAccountID: "acc-1", MarketType: "swap",
				InstrumentID: "SOL-USDT-SWAP", ExchangeSymbol: "SOLUSDT", PositionSide: "NET",
				SignedQuantity: "-10", EntryPrice: "100", MarkPrice: "100", Leverage: "3",
				ExchangeUpdatedAt: now.UnixMilli(),
			},
			{
				SpaceID: "s1", TradingAccountID: "acc-1", MarketType: "swap",
				InstrumentID: "DOGE-USDT-SWAP", ExchangeSymbol: "DOGEUSDT", PositionSide: "NET",
				SignedQuantity: "0", EntryPrice: "0", MarkPrice: "0", Leverage: "3",
				ExchangeUpdatedAt: now.UnixMilli(),
			},
		} {
			if err := tx.UpsertPosition(pos); err != nil {
				return err
			}
		}

		// log-1 有 24h 内 2 个点（1000 -> 1150，+150，+15%）以及窗口外更早的 1 个点（500，不应参与）
		for _, pt := range []struct {
			at     time.Time
			equity string
		}{
			{now.Add(-30 * time.Hour), "500"},
			{now.Add(-20 * time.Hour), "1000"},
			{now.Add(-1 * time.Hour), "1150"},
		} {
			if err := tx.UpsertLogicalAccountEquityPoint(EquityPointRecord{
				SpaceID: "s1", LogicalAccountID: "log-1", BucketTime: pt.at.UnixMilli(),
				Equity: pt.equity, AvailableFunds: pt.equity, UsedMargin: "0", SourceTime: pt.at.UnixMilli(),
			}); err != nil {
				return err
			}
		}
		// log-2 只有 1 个点，不足 2 个点，24h 变化为空
		if err := tx.UpsertLogicalAccountEquityPoint(EquityPointRecord{
			SpaceID: "s1", LogicalAccountID: "log-2", BucketTime: now.Add(-1 * time.Hour).UnixMilli(),
			Equity: "500", AvailableFunds: "500", UsedMargin: "0", SourceTime: now.Add(-1 * time.Hour).UnixMilli(),
		}); err != nil {
			return err
		}

		// 写入不同 owner 的订单以验证 ListOrdersForLane 的 SQL owner 过滤
		for _, ord := range []OrderRecord{
			{
				SpaceID: "s1", OrderID: "o1", TradingAccountID: "acc-1", ClientOrderID: "c1",
				Exchange: string(exchange.ExchangeBinance), MarketType: "spot",
				InstrumentID: "BTC-USDT-SPOT", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "SELL",
				Quantity: "0.1", ReferencePrice: "50000", ReferencePriceAt: now.UnixMilli(),
				OwnerType: "OPERATOR", OwnerID: "act-1", LogicalAccountID: "log-1", State: "FILLED", Version: 1,
			},
			{
				SpaceID: "s1", OrderID: "o2", TradingAccountID: "acc-1", ClientOrderID: "c2",
				Exchange: string(exchange.ExchangeBinance), MarketType: "spot",
				InstrumentID: "BTC-USDT-SPOT", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "SELL",
				Quantity: "0.1", ReferencePrice: "50000", ReferencePriceAt: now.UnixMilli(),
				OwnerType: "OPERATOR", OwnerID: "act-2", LogicalAccountID: "log-1", State: "FILLED", Version: 1,
			},
			{
				SpaceID: "s1", OrderID: "o3", TradingAccountID: "acc-1", ClientOrderID: "c3",
				Exchange: string(exchange.ExchangeBinance), MarketType: "spot",
				InstrumentID: "BTC-USDT-SPOT", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "SELL",
				Quantity: "0.1", ReferencePrice: "50000", ReferencePriceAt: now.UnixMilli(),
				OwnerType: "EXTERNAL", OwnerID: "ext-1", State: "FILLED", Version: 1,
			},
		} {
			if err := tx.CreateOrder(ord); err != nil {
				return err
			}
		}
		return nil
	}))

	summaries, err := st.BatchLogicalAccountSummaries(ctx, "s1", []string{"log-1", "log-2"}, now)
	require.NoError(t, err)
	require.Equal(t, int32(3), summaries["log-1"].PositionCount)
	require.NotNil(t, summaries["log-1"].TotalEquity)
	require.Equal(t, "1150", *summaries["log-1"].TotalEquity)
	require.NotNil(t, summaries["log-1"].EquityChange24h)
	require.Equal(t, "150", *summaries["log-1"].EquityChange24h)
	require.NotNil(t, summaries["log-1"].EquityChangePct24h)
	require.Equal(t, "0.15", *summaries["log-1"].EquityChangePct24h)

	require.Equal(t, int32(0), summaries["log-2"].PositionCount)
	require.Nil(t, summaries["log-2"].TotalEquity)
	require.Nil(t, summaries["log-2"].EquityChange24h)
	require.Nil(t, summaries["log-2"].EquityChangePct24h)

	key := exchange.NewSymbolKey(exchange.MarketTypeSpot, "BTCUSDT")
	all, err := st.ListOrdersForLane(ctx, "s1", "acc-1", key, "", "")
	require.NoError(t, err)
	require.Len(t, all, 3)

	onlyAct1, err := st.ListOrdersForLane(ctx, "s1", "acc-1", key, "OPERATOR", "act-1")
	require.NoError(t, err)
	require.Len(t, onlyAct1, 1)
	require.Equal(t, "o1", onlyAct1[0].OrderID)
}

func TestBatchLogicalAccountSummariesTestnetFallsBackToProduction(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	require.NoError(t, st.Transaction(ctx, func(tx *Tx) error {
		// 账户 1：币安 TESTNET 现货，表里只有 PRODUCTION 标的，应自动回退到 PRODUCTION 统计 ETH。
		if err := tx.CreateTradingAccount(TradingAccountRecord{
			SpaceID: "s1", TradingAccountID: "acc-testnet-fb", Name: "acc-testnet-fb",
			Exchange: string(exchange.ExchangeBinance), Markets: []string{"spot"},
			ExecutionMode: string(exchange.ExecutionModeLive), Environment: string(exchange.AccountEnvironmentTestnet),
			CredentialSecretID: "sec-1", SettlementAsset: "USDT", Status: "ENABLED",
			Snapshot: TradingAccountSnapshot{
				Balances: []AssetBalance{
					{Wallet: "SPOT", Asset: "USDT", Total: "1000", Available: "1000"},
					{Wallet: "SPOT", Asset: "ETH", Total: "2", Available: "2"},
				},
			},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "s1", LogicalAccountID: "log-fb", Name: "log-fb",
			Environment: string(exchange.AccountEnvironmentTestnet), SettlementAsset: "USDT",
			PauseReason: "init",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "s1", LogicalAccountID: "log-fb", TradingAccountID: "acc-testnet-fb", Enabled: true,
		}); err != nil {
			return err
		}

		// 账户 2：OKX TESTNET 现货，表里已有 TESTNET 标的（只有 BTC，没有 ETH），应以 TESTNET 为准不混用 PRODUCTION 的 ETH。
		if err := tx.CreateTradingAccount(TradingAccountRecord{
			SpaceID: "s1", TradingAccountID: "acc-testnet-native", Name: "acc-testnet-native",
			Exchange: string(exchange.ExchangeOKX), Markets: []string{"spot"},
			ExecutionMode: string(exchange.ExecutionModeLive), Environment: string(exchange.AccountEnvironmentTestnet),
			CredentialSecretID: "sec-2", SettlementAsset: "USDT", Status: "ENABLED",
			Snapshot: TradingAccountSnapshot{
				Balances: []AssetBalance{
					{Wallet: "UNIFIED", Asset: "BTC", Total: "0.5", Available: "0.5"},
					{Wallet: "UNIFIED", Asset: "ETH", Total: "1", Available: "1"},
				},
			},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "s1", LogicalAccountID: "log-native", Name: "log-native",
			Environment: string(exchange.AccountEnvironmentTestnet), SettlementAsset: "USDT",
			PauseReason: "init",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "s1", LogicalAccountID: "log-native", TradingAccountID: "acc-testnet-native", Enabled: true,
		}); err != nil {
			return err
		}

		for _, inst := range []InstrumentRecord{
			{
				Exchange: string(exchange.ExchangeBinance), Environment: string(exchange.AccountEnvironmentProduction),
				MarketType: "spot", ExchangeSymbol: "ETHUSDT", InstrumentID: "ETH-USDT-SPOT",
				BaseAsset: "ETH", QuoteAsset: "USDT", SettlementAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			},
			{
				Exchange: string(exchange.ExchangeOKX), Environment: string(exchange.AccountEnvironmentTestnet),
				MarketType: "spot", ExchangeSymbol: "BTC-USDT", InstrumentID: "BTC-USDT-SPOT",
				BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			},
			{
				Exchange: string(exchange.ExchangeOKX), Environment: string(exchange.AccountEnvironmentProduction),
				MarketType: "spot", ExchangeSymbol: "ETH-USDT", InstrumentID: "ETH-USDT-SPOT",
				BaseAsset: "ETH", QuoteAsset: "USDT", SettlementAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			},
		} {
			if err := tx.UpsertInstrument(inst); err != nil {
				return err
			}
		}
		return nil
	}))

	summaries, err := st.BatchLogicalAccountSummaries(ctx, "s1", []string{"log-fb", "log-native"}, now)
	require.NoError(t, err)
	require.Equal(t, int32(1), summaries["log-fb"].PositionCount, "TESTNET 无标的表时应回退 PRODUCTION 统计到 ETH")
	require.Equal(t, int32(1), summaries["log-native"].PositionCount, "TESTNET 已有标的表时仅使用 TESTNET 标的（只计 BTC）")
}

// TotalEquity 区分“无已同步权益（nil）”“同步权益合计为 0（"0"）”“多成员求和且排除停用成员”。
func TestBatchLogicalAccountSummariesTotalEquity(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.Transaction(ctx, func(tx *Tx) error {
		for _, acc := range []TradingAccountRecord{
			{
				SpaceID: "s1", TradingAccountID: "acc-a", Name: "acc-a",
				Exchange: string(exchange.ExchangeBinance), Markets: []string{"spot"},
				ExecutionMode: string(exchange.ExecutionModePaper), Environment: string(exchange.AccountEnvironmentPaper),
				SettlementAsset: "USDT", Status: "ENABLED",
				Snapshot: TradingAccountSnapshot{Equity: "120.5"},
			},
			{
				SpaceID: "s1", TradingAccountID: "acc-b", Name: "acc-b",
				Exchange: string(exchange.ExchangeBinance), Markets: []string{"spot"},
				ExecutionMode: string(exchange.ExecutionModePaper), Environment: string(exchange.AccountEnvironmentPaper),
				SettlementAsset: "USDT", Status: "ENABLED",
				Snapshot: TradingAccountSnapshot{Equity: "79.5"},
			},
			{
				SpaceID: "s1", TradingAccountID: "acc-disabled", Name: "acc-disabled",
				Exchange: string(exchange.ExchangeBinance), Markets: []string{"spot"},
				ExecutionMode: string(exchange.ExecutionModePaper), Environment: string(exchange.AccountEnvironmentPaper),
				SettlementAsset: "USDT", Status: "ENABLED",
				Snapshot: TradingAccountSnapshot{Equity: "999"},
			},
			{
				SpaceID: "s1", TradingAccountID: "acc-zero", Name: "acc-zero",
				Exchange: string(exchange.ExchangeBinance), Markets: []string{"spot"},
				ExecutionMode: string(exchange.ExecutionModePaper), Environment: string(exchange.AccountEnvironmentPaper),
				SettlementAsset: "USDT", Status: "ENABLED",
				Snapshot: TradingAccountSnapshot{Equity: "0"},
			},
			{
				SpaceID: "s1", TradingAccountID: "acc-empty", Name: "acc-empty",
				Exchange: string(exchange.ExchangeBinance), Markets: []string{"spot"},
				ExecutionMode: string(exchange.ExecutionModePaper), Environment: string(exchange.AccountEnvironmentPaper),
				SettlementAsset: "USDT", Status: "ENABLED",
			},
			{
				SpaceID: "s1", TradingAccountID: "acc-partial-synced", Name: "acc-partial-synced",
				Exchange: string(exchange.ExchangeBinance), Markets: []string{"spot"},
				ExecutionMode: string(exchange.ExecutionModePaper), Environment: string(exchange.AccountEnvironmentPaper),
				SettlementAsset: "USDT", Status: "ENABLED",
				Snapshot: TradingAccountSnapshot{Equity: "300"},
			},
			{
				SpaceID: "s1", TradingAccountID: "acc-partial-unsynced", Name: "acc-partial-unsynced",
				Exchange: string(exchange.ExchangeBinance), Markets: []string{"spot"},
				ExecutionMode: string(exchange.ExecutionModePaper), Environment: string(exchange.AccountEnvironmentPaper),
				SettlementAsset: "USDT", Status: "ENABLED",
			},
			{
				SpaceID: "s1", TradingAccountID: "acc-disabled-empty", Name: "acc-disabled-empty",
				Exchange: string(exchange.ExchangeBinance), Markets: []string{"spot"},
				ExecutionMode: string(exchange.ExecutionModePaper), Environment: string(exchange.AccountEnvironmentPaper),
				SettlementAsset: "USDT", Status: "ENABLED",
			},
		} {
			if err := tx.CreateTradingAccount(acc); err != nil {
				return err
			}
		}
		for _, id := range []string{"log-sum", "log-zero", "log-unsynced", "log-partial"} {
			if err := tx.CreateLogicalAccount(LogicalAccountRecord{
				SpaceID: "s1", LogicalAccountID: id, Name: id,
				Environment: string(exchange.AccountEnvironmentPaper), SettlementAsset: "USDT", PauseReason: "init",
			}); err != nil {
				return err
			}
		}
		for _, member := range []LogicalAccountMemberRecord{
			{SpaceID: "s1", LogicalAccountID: "log-sum", TradingAccountID: "acc-a", Enabled: true, Priority: 1},
			{SpaceID: "s1", LogicalAccountID: "log-sum", TradingAccountID: "acc-b", Enabled: true, Priority: 2},
			{SpaceID: "s1", LogicalAccountID: "log-sum", TradingAccountID: "acc-disabled", Enabled: false, Priority: 3},
			{SpaceID: "s1", LogicalAccountID: "log-sum", TradingAccountID: "acc-disabled-empty", Enabled: false, Priority: 4},
			{SpaceID: "s1", LogicalAccountID: "log-zero", TradingAccountID: "acc-zero", Enabled: true, Priority: 1},
			{SpaceID: "s1", LogicalAccountID: "log-unsynced", TradingAccountID: "acc-empty", Enabled: true, Priority: 1},
			{SpaceID: "s1", LogicalAccountID: "log-partial", TradingAccountID: "acc-partial-synced", Enabled: true, Priority: 1},
			{SpaceID: "s1", LogicalAccountID: "log-partial", TradingAccountID: "acc-partial-unsynced", Enabled: true, Priority: 2},
		} {
			if err := tx.PutLogicalAccountMember(member); err != nil {
				return err
			}
		}
		return nil
	}))

	summaries, err := st.BatchLogicalAccountSummaries(ctx, "s1", []string{"log-sum", "log-zero", "log-unsynced", "log-partial"}, time.Now())
	require.NoError(t, err)
	require.NotNil(t, summaries["log-sum"].TotalEquity)
	require.Equal(t, "200", *summaries["log-sum"].TotalEquity)
	require.NotNil(t, summaries["log-zero"].TotalEquity)
	require.Equal(t, "0", *summaries["log-zero"].TotalEquity)
	require.Nil(t, summaries["log-unsynced"].TotalEquity)
	require.Nil(t, summaries["log-partial"].TotalEquity, "任一启用成员未同步时返回 nil，避免返回误导性的部分和")
}
