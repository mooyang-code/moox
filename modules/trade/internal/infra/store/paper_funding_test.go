package store

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

// seedPaperSwapAccount 建一个只开永续的模拟账户（初始资金 100000 USDT）和 BTCUSDT、ETHUSDT 两个永续标的。
func seedPaperSwapAccount(t *testing.T, s *Store) TradingAccountRecord {
	t.Helper()
	account := testAccount()
	account.Markets = []string{"SWAP"}
	account.MarginMode = "CROSS"
	account.LeverageSettings = LeverageSettings{DefaultLeverageKey: "10"}
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
		if err := tx.CreateTradingAccount(account); err != nil {
			return err
		}
		for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
			if err := tx.UpsertInstrument(InstrumentRecord{
				Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: symbol, InstrumentID: symbol, BaseAsset: symbol[:3],
				QuoteAsset: "USDT", SettlementAsset: "USDT", Linear: true, ContractValue: "1", ContractValueAsset: symbol[:3],
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	return account
}

// setPaperPosition 直接写当前持仓（真实路径由成交投影，这里只验证结算读到的数）。
func setPaperPosition(t *testing.T, s *Store, account TradingAccountRecord, symbol, quantity string) {
	t.Helper()
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
		return tx.UpsertPosition(PositionRecord{
			SpaceID: account.SpaceID, TradingAccountID: account.TradingAccountID, MarketType: "SWAP", ExchangeSymbol: symbol,
			PositionSide: "NET", SignedQuantity: quantity, EntryPrice: "100", MarkPrice: "100", Leverage: "10", MarginMode: "CROSS",
			ExchangeUpdatedAt: 1,
		})
	}))
}

// addPaperSwapFill 记一笔没有手续费、没有盈亏的永续成交，只影响成交表。
func addPaperSwapFill(t *testing.T, s *Store, account TradingAccountRecord, id, symbol, side, quantity string, tradedAt int64) {
	t.Helper()
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
		if err := tx.CreateOrder(OrderRecord{
			SpaceID: account.SpaceID, TradingAccountID: account.TradingAccountID, OrderID: "order-" + id, ClientOrderID: "client-" + id,
			MarketType: "SWAP", ExchangeSymbol: symbol, OrderType: "MARKET", Side: side, PositionSide: "NET", Quantity: quantity,
			ReferencePrice: "100", OwnerType: "EXTERNAL", OwnerID: "client-" + id, State: "FILLED",
		}); err != nil {
			return err
		}
		_, err := tx.InsertFill(FillRecord{
			SpaceID: account.SpaceID, TradingAccountID: account.TradingAccountID, FillID: "fill-" + id, ExchangeTradeID: "trade-" + id,
			OrderID: "order-" + id, Price: "100", Quantity: quantity, Fee: "0", SettlementAsset: "USDT", TradedAt: tradedAt,
		})
		return err
	}))
}

func rate(symbol string, at int64, value, mark string) exchange.FundingRate {
	return exchange.FundingRate{ExchangeSymbol: symbol, FundingTime: time.UnixMilli(at), Rate: shared.MustDecimal(value), MarkPrice: shared.MustDecimal(mark)}
}

func paperUSDT(t *testing.T, s *Store, account TradingAccountRecord) string {
	t.Helper()
	snapshot, err := s.GetPaperBalanceSnapshot(context.Background(), account.SpaceID, account.TradingAccountID)
	require.NoError(t, err)
	return snapshot.Totals["USDT"].String()
}

func TestPaperFundingAmountForLongAndShort(t *testing.T) {
	s := openTestStore(t)
	account := seedPaperSwapAccount(t, s)
	setPaperPosition(t, s, account, "BTCUSDT", "2")
	setPaperPosition(t, s, account, "ETHUSDT", "-3")
	inserted, err := s.SettlePaperFunding(context.Background(), account.SpaceID, account.TradingAccountID, []exchange.FundingRate{
		rate("BTCUSDT", 1_000, "0.0001", "50000"),   // 多头、正费率：支付 2 × 50000 × 0.0001 = 10
		rate("ETHUSDT", 1_000, "0.0002", "2000"),    // 空头、正费率：收到 3 × 2000 × 0.0002 = 1.2
		rate("BTCUSDT", 2_000, "-0.00005", "40000"), // 多头、负费率：收到 2 × 40000 × 0.00005 = 4
		rate("ETHUSDT", 2_000, "-0.0001", "1000"),   // 空头、负费率：支付 3 × 1000 × 0.0001 = 0.3
	})
	require.NoError(t, err)
	require.Len(t, inserted, 4)
	amounts := map[string]string{}
	for _, record := range inserted {
		amounts[record.ExchangeSymbol+"@"+time.UnixMilli(record.SettledAt).UTC().Format("05.000")] = record.Amount
		require.Equal(t, "USDT", record.Asset)
	}
	require.Equal(t, map[string]string{"BTCUSDT@01.000": "-10", "ETHUSDT@01.000": "1.2", "BTCUSDT@02.000": "4", "ETHUSDT@02.000": "-0.3"}, amounts)
	require.Equal(t, "99994.9", paperUSDT(t, s, account))
}

func TestPaperFundingIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	account := seedPaperSwapAccount(t, s)
	setPaperPosition(t, s, account, "BTCUSDT", "1")
	rates := []exchange.FundingRate{rate("BTCUSDT", 3_600_000, "0.0001", "50000")}
	first, err := s.SettlePaperFunding(context.Background(), account.SpaceID, account.TradingAccountID, rates)
	require.NoError(t, err)
	require.Len(t, first, 1)
	// 同一结算点再结算一次（即使标记价格不同）：不插入、不改余额，原记录不变。
	again, err := s.SettlePaperFunding(context.Background(), account.SpaceID, account.TradingAccountID,
		append(rates, rate("BTCUSDT", 3_600_000, "0.0001", "60000")))
	require.NoError(t, err)
	require.Empty(t, again)
	require.Equal(t, "99995", paperUSDT(t, s, account))
	records, total, err := s.ListPaperFundingFees(context.Background(), account.SpaceID, account.TradingAccountID, 0, 10_000_000, 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, "50000", records[0].MarkPrice)
	latest, err := s.LatestPaperFundingSettlement(context.Background(), account.SpaceID, account.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, int64(3_600_000), latest)
}

// 结算时刻的持仓按成交推：严格早于结算时刻的成交算在持仓里，同一毫秒及之后的不算；空仓的点不记。
func TestPaperFundingQuantityFollowsFills(t *testing.T) {
	s := openTestStore(t)
	account := seedPaperSwapAccount(t, s)
	addPaperSwapFill(t, s, account, "1", "BTCUSDT", "BUY", "2", 100)
	addPaperSwapFill(t, s, account, "2", "BTCUSDT", "SELL", "3", 300)
	setPaperPosition(t, s, account, "BTCUSDT", "-1")
	inserted, err := s.SettlePaperFunding(context.Background(), account.SpaceID, account.TradingAccountID, []exchange.FundingRate{
		rate("BTCUSDT", 400, "0.01", "100"),
		rate("BTCUSDT", 50, "0.01", "100"),
		rate("BTCUSDT", 300, "0.01", "100"),
		rate("BTCUSDT", 200, "0.01", "100"),
		rate("ETHUSDT", 200, "0.01", "100"),
	})
	require.NoError(t, err)
	quantities := map[int64]string{}
	for _, record := range inserted {
		quantities[record.SettledAt] = record.Quantity
	}
	require.Equal(t, map[int64]string{200: "2", 300: "2", 400: "-1"}, quantities)
	// 2 × 100 × 0.01 支付两次，-1 × 100 × 0.01 收到一次。
	require.Equal(t, "99997", paperUSDT(t, s, account))
}

func TestPaperFundingSkipsMissingMarkPrice(t *testing.T) {
	s := openTestStore(t)
	account := seedPaperSwapAccount(t, s)
	setPaperPosition(t, s, account, "BTCUSDT", "1")
	inserted, err := s.SettlePaperFunding(context.Background(), account.SpaceID, account.TradingAccountID, []exchange.FundingRate{
		{ExchangeSymbol: "BTCUSDT", FundingTime: time.UnixMilli(1_000), Rate: shared.MustDecimal("0.0001")},
	})
	require.NoError(t, err)
	require.Empty(t, inserted)
	require.Equal(t, "100000", paperUSDT(t, s, account))
}

func TestPaperFundingLedgerQueryIsNewestFirst(t *testing.T) {
	s := openTestStore(t)
	account := seedPaperSwapAccount(t, s)
	setPaperPosition(t, s, account, "BTCUSDT", "1")
	setPaperPosition(t, s, account, "ETHUSDT", "1")
	_, err := s.SettlePaperFunding(context.Background(), account.SpaceID, account.TradingAccountID, []exchange.FundingRate{
		rate("BTCUSDT", 1_000, "0.0001", "100"), rate("ETHUSDT", 2_000, "0.0001", "100"), rate("BTCUSDT", 3_000, "0.0001", "100"),
	})
	require.NoError(t, err)
	records, total, err := s.ListPaperFundingFees(context.Background(), account.SpaceID, account.TradingAccountID, 1_500, 3_000, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, records, 1)
	require.Equal(t, int64(3_000), records[0].SettledAt)
}

func TestPaperSwapSweepAndExposure(t *testing.T) {
	activity := PaperSwapActivity{
		Positions: map[string]shared.Decimal{"BTCUSDT": shared.MustDecimal("0")},
		Fills: []PaperSwapFill{
			{ExchangeSymbol: "BTCUSDT", TradedAt: 200, Delta: shared.MustDecimal("1")},
			{ExchangeSymbol: "BTCUSDT", TradedAt: 500, Delta: shared.MustDecimal("-1")},
		},
	}
	sweep := activity.Sweep()
	sweep.AdvanceTo(100)
	require.True(t, sweep.Quantity("BTCUSDT").IsZero())
	require.False(t, sweep.ExposedUntil(150), "开仓之前没有持仓")
	require.True(t, sweep.ExposedUntil(200), "区间里有成交")
	sweep.AdvanceTo(300)
	require.Equal(t, "1", sweep.Quantity("BTCUSDT").String())
	sweep.AdvanceTo(600)
	require.True(t, sweep.Quantity("BTCUSDT").IsZero())
	require.False(t, sweep.ExposedUntil(10_000), "平仓之后没有持仓")
	require.Equal(t, map[string]struct{}{"BTCUSDT": {}}, activity.Symbols())
}
