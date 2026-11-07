package paper

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 同一份目标里现货、合约都能成交：一个模拟账户同时开通现货和合约，同名的 BTCUSDT 现货买单和合约买单各自撮合，
// 现货成交进余额，合约成交进持仓表，互不串。
func TestPaperMatchesSpotAndSwapOrdersOfOneAccount(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "mixed.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "s", TradingAccountID: "a", Name: "a", Exchange: "BINANCE", Markets: []string{"SPOT", "SWAP"},
			ExecutionMode: "PAPER", SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED",
			LeverageSettings: store.LeverageSettings{store.DefaultLeverageKey: "5"},
			PaperConfig: &store.PaperAccountConfigRecord{
				SpaceID: "s", TradingAccountID: "a", InitialBalance: "10000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBPS: "0",
			},
		}); err != nil {
			return err
		}
		for _, market := range []string{"SPOT", "SWAP"} {
			record := store.InstrumentRecord{
				Exchange: "BINANCE", MarketType: market, ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-" + market,
				BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
				PriceTick: "0.01", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", Status: "TRADING",
			}
			if market == "SWAP" {
				record.Linear, record.ContractValue, record.ContractValueAsset = true, "1", "BTC"
			}
			if err := tx.UpsertInstrument(record); err != nil {
				return err
			}
			positionSide := ""
			if market == "SWAP" {
				positionSide = string(exchange.PositionSideNet)
			}
			if err := tx.CreateOrder(store.OrderRecord{
				SpaceID: "s", TradingAccountID: "a", OrderID: "order-" + market, ClientOrderID: "client-" + market,
				ExchangeOrderID: "100", Exchange: "BINANCE", MarketType: market, InstrumentID: "BTC-USDT-" + market,
				ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY", PositionSide: positionSide, Quantity: "1",
				ReferencePrice: "100", OwnerType: "EXTERNAL", OwnerID: "o-" + market, State: "OPEN", Version: 1, FirstMatchPending: true,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	m := &Matcher{Store: s, Reducer: &consumer.Reducer{Store: s}}
	m.DecideContext = func(_ context.Context, o store.OrderRecord) (Decision, error) { return matchFill(o), nil }

	for range 2 {
		require.NoError(t, m.Scan(ctx))
	}

	for _, market := range []string{"SPOT", "SWAP"} {
		order, err := s.GetOrder(ctx, "s", "order-"+market)
		require.NoError(t, err)
		require.Equal(t, "FILLED", order.State, market)
	}
	positions, err := s.ListPositions(ctx, "s", "a", exchange.SymbolKey{})
	require.NoError(t, err)
	require.Len(t, positions, 1, "只有合约成交进持仓表")
	require.Equal(t, "SWAP", positions[0].MarketType)
	require.Equal(t, "1", positions[0].SignedQuantity)
	balances, err := s.GetPaperBalanceSnapshot(ctx, "s", "a")
	require.NoError(t, err)
	require.Equal(t, "1", balances.Totals["BTC"].String(), "现货成交进余额")
}
