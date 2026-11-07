package consumer

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

// 币安现货和合约的交易所代码同名（BTCUSDT），订单号、成交号是两套独立序列，可能完全相同。
// 同一个执行账户同时开通两个市场时，两边的订单、成交、持仓必须各记各的，不能串。
const sameSymbol = "BTCUSDT"

func openTwoMarketStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: testSpace, TradingAccountID: testAccount, Name: "two-market",
			Exchange: string(exchange.ExchangeBinance), Markets: []string{"SPOT", "SWAP"},
			ExecutionMode: string(exchange.ExecutionModePaper), Environment: string(exchange.AccountEnvironmentPaper),
			SettlementAsset: "USDT", MarginMode: string(exchange.MarginModeCross),
			Status:           string(exchange.AccountStatusEnabled),
			LeverageSettings: store.LeverageSettings{store.DefaultLeverageKey: "10"},
		}); err != nil {
			return err
		}
		for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: string(exchange.ExchangeBinance), MarketType: string(market),
				ExchangeSymbol: sameSymbol, InstrumentID: "BTC-USDT-" + string(market),
				BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
				Linear: market == exchange.MarketTypeSwap, ContractValue: "1", ContractValueAsset: "BTC",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	return s
}

func seedMarketOrder(t *testing.T, s *store.Store, market exchange.MarketType) {
	t.Helper()
	positionSide := ""
	if market == exchange.MarketTypeSwap {
		positionSide = string(exchange.PositionSideNet)
	}
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: testSpace, OrderID: "order-" + string(market), TradingAccountID: testAccount,
			ClientOrderID: "client-" + string(market), ExchangeOrderID: "100", // 两个市场的交易所订单号相同
			Exchange: string(exchange.ExchangeBinance), MarketType: string(market),
			ExchangeSymbol: sameSymbol, OrderType: string(exchange.OrderTypeMarket),
			Side: string(exchange.SideBuy), PositionSide: positionSide,
			Quantity: "1", ReferencePrice: "100", ReferencePriceAt: time.Now().UnixMilli(),
			OwnerType: "EXTERNAL", OwnerID: "100", State: "OPEN", Version: 1,
		})
	}))
}

func TestSameSymbolOrdersAndFillsInSpotAndSwapStaySeparate(t *testing.T) {
	ctx := context.Background()
	s := openTwoMarketStore(t)
	seedMarketOrder(t, s, exchange.MarketTypeSpot)
	seedMarketOrder(t, s, exchange.MarketTypeSwap)
	reducer := Reducer{Store: s}

	for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
		fill := exchange.Fill{
			Market: market, ExchangeTradeID: "7", ExchangeOrderID: "100", // 两个市场的成交号也相同
			ExchangeSymbol: sameSymbol, Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"),
			Price: shared.MustDecimal("100"), FeeAsset: "USDT", TradedAt: time.Now(),
		}
		if market == exchange.MarketTypeSwap {
			fill.PositionSide = exchange.PositionSideNet
		}
		applied, err := reducer.ApplyFill(ctx, fill, fillSource())
		require.NoError(t, err, market)
		require.True(t, applied, market)
	}

	fills, total, err := s.ListFills(ctx, testSpace, store.FillQuery{TradingAccountID: testAccount, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	byMarket := map[string]store.FillRecord{}
	for _, fill := range fills {
		byMarket[fill.MarketType] = fill
	}
	require.Equal(t, testAccount+":SPOT:BTCUSDT:7", byMarket["SPOT"].FillID)
	require.Equal(t, testAccount+":SWAP:BTCUSDT:7", byMarket["SWAP"].FillID)
	require.Equal(t, "order-SPOT", byMarket["SPOT"].OrderID)
	require.Equal(t, "order-SWAP", byMarket["SWAP"].OrderID)
	require.Equal(t, "BTC-USDT-SPOT", byMarket["SPOT"].InstrumentID)
	require.Equal(t, "BTC-USDT-SWAP", byMarket["SWAP"].InstrumentID)

	// 同一笔成交重放仍是重复，不会因为市场不同而漏判或误判。
	applied, err := reducer.ApplyFill(ctx, exchange.Fill{
		Market: exchange.MarketTypeSwap, ExchangeTradeID: "7", ExchangeOrderID: "100", ExchangeSymbol: sameSymbol,
		PositionSide: exchange.PositionSideNet, Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"),
		Price: shared.MustDecimal("100"), FeeAsset: "USDT", TradedAt: time.Now(),
	}, fillSource())
	require.NoError(t, err)
	require.False(t, applied)

	// 按（市场，代码，交易所订单号）各取各的订单。
	for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
		got, err := s.GetOrderByExchangeID(ctx, testSpace, testAccount, exchange.SymbolKey{Market: market, Symbol: sameSymbol}, "100")
		require.NoError(t, err)
		require.Equal(t, "order-"+string(market), got.OrderID)
		require.Equal(t, "FILLED", got.State)
	}

	// 只有合约有持仓表里的持仓，而且按（市场，代码）取：现货键取不到。
	swapKey := exchange.SymbolKey{Market: exchange.MarketTypeSwap, Symbol: sameSymbol}
	position, found, err := s.GetPosition(ctx, testSpace, testAccount, swapKey, string(exchange.PositionSideNet))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "1", position.SignedQuantity)
	require.Equal(t, "SWAP", position.MarketType)
	_, found, err = s.GetPosition(ctx, testSpace, testAccount, exchange.SymbolKey{Market: exchange.MarketTypeSpot, Symbol: sameSymbol}, string(exchange.PositionSideNet))
	require.NoError(t, err)
	require.False(t, found)
}

func TestSamePositionKeyInTwoMarketsCoexistsAndTombstonesAreSeparate(t *testing.T) {
	ctx := context.Background()
	s := openTwoMarketStore(t)
	positionOf := func(market, quantity string) store.PositionRecord {
		return store.PositionRecord{
			SpaceID: testSpace, TradingAccountID: testAccount, MarketType: market, ExchangeSymbol: sameSymbol,
			PositionSide: "NET", SignedQuantity: quantity, Leverage: "10", MarginMode: "CROSS", ExchangeUpdatedAt: 1_000,
		}
	}
	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		for _, record := range []store.PositionRecord{positionOf("SPOT", "3"), positionOf("SWAP", "-2")} {
			if err := tx.UpsertPosition(record); err != nil {
				return err
			}
		}
		// 权威快照里只剩合约持仓：现货那条被删除并留下现货的墓碑，合约的墓碑不受影响。
		return tx.ReplacePositionsForAccount(testSpace, testAccount, []store.PositionRecord{positionOf("SWAP", "-2")}, 2_000, 0)
	}))

	positions, err := s.ListPositions(ctx, testSpace, testAccount, exchange.SymbolKey{})
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, "SWAP", positions[0].MarketType)
	require.Equal(t, "-2", positions[0].SignedQuantity)

	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		spot, err := tx.PositionSnapshotCoveredUntil(testSpace, testAccount, exchange.SymbolKey{Market: exchange.MarketTypeSpot, Symbol: sameSymbol}, "NET")
		require.NoError(t, err)
		require.Equal(t, int64(2_000), spot, "现货被快照确认为空仓，留下墓碑")
		swap, err := tx.PositionSnapshotCoveredUntil(testSpace, testAccount, exchange.SymbolKey{Market: exchange.MarketTypeSwap, Symbol: sameSymbol}, "NET")
		require.NoError(t, err)
		require.Equal(t, int64(1_000), swap, "合约仍有持仓，覆盖时间是它自己的")
		return nil
	}))
}

func TestFillOfAMarketTheAccountDidNotOpenIsRejected(t *testing.T) {
	ctx := context.Background()
	s := openFillStore(t, exchange.MarketTypeSpot)
	seedFillOrder(t, s, exchange.MarketTypeSpot, "OPEN", "1", "100")
	fill := spotFill("trade-1", "1")
	fill.Market = exchange.MarketTypeSwap

	_, err := (&Reducer{Store: s}).ApplyFill(ctx, fill, fillSource())

	require.Error(t, err, "按（市场，代码）找不到订单，不能把合约成交记到现货订单上")
}
