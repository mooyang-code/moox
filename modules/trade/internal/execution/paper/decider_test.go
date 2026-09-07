package paper

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type deciderAdapter struct {
	execution.ExecutionAdapter
	execution.MarketDataSource
	quote      execution.MarketQuote
	quoteErr   error
	quoteCalls int
	snapshot   exchange.AccountSnapshot
}

func (a *deciderAdapter) GetQuote(context.Context, shared.ExchangeSymbol) (execution.MarketQuote, error) {
	a.quoteCalls++
	return a.quote, a.quoteErr
}

func (a *deciderAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return a.snapshot, nil
}

type deciderAdapters struct{ adapter execution.ExecutionAdapter }

func (a deciderAdapters) Adapter(string) (execution.ExecutionAdapter, error) { return a.adapter, nil }

type deciderReferenceAdapter struct {
	execution.ExecutionAdapter
	quote    exchange.ReferencePrice
	snapshot exchange.AccountSnapshot
}

func (a deciderReferenceAdapter) GetReferencePrice(context.Context, string) (exchange.ReferencePrice, error) {
	return a.quote, nil
}

func (a deciderReferenceAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return a.snapshot, nil
}

func deciderFixture(t *testing.T, market string) (*Decider, *deciderAdapter, store.OrderRecord) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space", TradingAccountID: "paper", Name: "Decider", Exchange: "BINANCE",
			MarketType: market, ExecutionMode: "PAPER", SettlementAsset: "USDT", Status: "ENABLED",
			LeverageSettings: store.LeverageSettings{"BTC-USDT": "10"},
			PaperConfig: &store.PaperAccountConfigRecord{SpaceID: "space", TradingAccountID: "paper",
				InitialBalance: "10000", MakerFeeRate: "0.001", TakerFeeRate: "0.002", SlippageBPS: "100"},
		})
	}))
	now := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	adapter := &deciderAdapter{
		quote: execution.MarketQuote{Bid: shared.MustDecimal("99"), Ask: shared.MustDecimal("101"), SourceTime: now},
		snapshot: exchange.AccountSnapshot{AvailableFunds: shared.MustDecimal("1000"), Balances: []exchange.AssetBalance{
			{Asset: "USDT", Available: shared.MustDecimal("1000")},
		}},
	}
	return &Decider{Store: db, Adapters: deciderAdapters{adapter}, Now: func() time.Time { return now }}, adapter, store.OrderRecord{
		SpaceID: "space", TradingAccountID: "paper", OrderID: "order", ClientOrderID: "client", ExchangeOrderID: "exchange-order",
		InstrumentID: "BTC-USDT", ExchangeSymbol: "BTCUSDT", MarketType: market, Side: "BUY", PositionSide: "NET",
		OrderType: "MARKET", Quantity: "1", ReferencePrice: "100", ReservedAsset: "USDT", RemainingReservedQuantity: "100.2",
	}
}

func TestDeciderFrozenMarketAndFirstLimitPrices(t *testing.T) {
	for _, orderType := range []string{"MARKET", "LIMIT"} {
		t.Run(orderType, func(t *testing.T) {
			d, adapter, order := deciderFixture(t, "SPOT")
			price := "100"
			order.OrderType, order.TimeInForce = orderType, "GTC"
			if orderType == "MARKET" {
				order.PaperExecutionPrice = &price
			} else {
				order.LimitPrice, order.FirstMatchPending = &price, true
			}
			adapter.quoteErr = errors.New("public quote down")
			decision, err := d.Decide(context.Background(), order)
			require.NoError(t, err)
			require.False(t, decision.Cancel || decision.Rest)
			require.Equal(t, "100", decision.Fill.Price.String())
			require.Equal(t, "0.2", decision.Fill.Fee.String())
			require.Equal(t, "TAKER", decision.Fill.LiquidityRole)
			require.Equal(t, d.Now(), decision.Fill.TradedAt)
			require.Zero(t, adapter.quoteCalls)
		})
	}
}

func TestDeciderLiveQuotePoliciesAndFees(t *testing.T) {
	for _, tc := range []struct {
		name, kind, policy, limit, side, price, fee, role string
		rest, cancel                                      bool
	}{
		{"market-buy-slippage", "MARKET", "", "", "BUY", "102.01", "0.20402", "TAKER", false, false},
		{"market-sell-slippage", "MARKET", "", "", "SELL", "98.01", "0.19602", "TAKER", false, false},
		{"resting-maker", "LIMIT", "GTC", "102", "BUY", "101", "0.101", "MAKER", false, false},
		{"resting-sell-maker", "LIMIT", "GTC", "98", "SELL", "99", "0.099", "MAKER", false, false},
		{"gtc-not-marketable", "LIMIT", "GTC", "100", "BUY", "", "", "", true, false},
		{"ioc-not-marketable", "LIMIT", "IOC", "100", "BUY", "", "", "", false, true},
		{"fok-not-marketable", "LIMIT", "FOK", "100", "BUY", "", "", "", false, true},
		{"ioc-marketable", "LIMIT", "IOC", "101", "BUY", "101", "0.202", "TAKER", false, false},
		{"fok-marketable", "LIMIT", "FOK", "101", "BUY", "101", "0.202", "TAKER", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, order := deciderFixture(t, "SPOT")
			order.OrderType, order.TimeInForce, order.Side = tc.kind, tc.policy, tc.side
			if tc.limit != "" {
				order.LimitPrice = &tc.limit
			}
			decision, err := d.Decide(context.Background(), order)
			require.NoError(t, err)
			require.Equal(t, tc.rest, decision.Rest)
			require.Equal(t, tc.cancel, decision.Cancel)
			if tc.rest || tc.cancel {
				return
			}
			require.Equal(t, tc.price, decision.Fill.Price.String())
			require.Equal(t, tc.fee, decision.Fill.Fee.String())
			require.Equal(t, tc.role, decision.Fill.LiquidityRole)
			require.Equal(t, "USDT", decision.Fill.FeeAsset)
			require.Equal(t, "1", decision.Fill.Quantity.String())
		})
	}
}

func TestDeciderStaleQuoteUsesInjectedClock(t *testing.T) {
	for _, policy := range []string{"GTC", "IOC", "FOK"} {
		t.Run(policy, func(t *testing.T) {
			d, adapter, order := deciderFixture(t, "SPOT")
			limit := "102"
			order.OrderType, order.TimeInForce, order.LimitPrice = "LIMIT", policy, &limit
			adapter.quote.SourceTime = d.Now().Add(-11 * time.Second)
			decision, err := d.Decide(context.Background(), order)
			require.ErrorContains(t, err, "stale")
			require.False(t, decision.Rest || decision.Cancel)
		})
	}
}

func TestDeciderQuoteFailureRemainsRetryableForEveryPolicy(t *testing.T) {
	for _, policy := range []string{"GTC", "IOC", "FOK"} {
		t.Run(policy, func(t *testing.T) {
			d, adapter, order := deciderFixture(t, "SPOT")
			limit := "102"
			order.OrderType, order.TimeInForce, order.LimitPrice = "LIMIT", policy, &limit
			failure := errors.New("quote provider unavailable")
			adapter.quoteErr = failure
			decision, err := d.Decide(context.Background(), order)
			require.ErrorIs(t, err, failure)
			require.False(t, decision.Rest || decision.Cancel)
		})
	}
}

func TestDeciderReservationUsesAvailablePlusOwnReserve(t *testing.T) {
	for _, market := range []string{"SPOT", "SWAP"} {
		t.Run(market, func(t *testing.T) {
			d, adapter, order := deciderFixture(t, market)
			price := "100"
			order.PaperExecutionPrice = &price
			order.RemainingReservedQuantity = "10.1"
			available := "90.1"
			if market == "SWAP" {
				available = "0.1"
			}
			adapter.snapshot.AvailableFunds = shared.MustDecimal(available)
			adapter.snapshot.Balances[0].Available = shared.MustDecimal(available)
			decision, err := d.Decide(context.Background(), order)
			require.NoError(t, err)
			require.False(t, decision.Cancel)
			order.RemainingReservedQuantity = "10.099999"
			decision, err = d.Decide(context.Background(), order)
			require.NoError(t, err)
			require.True(t, decision.Cancel)
			require.Equal(t, "paper reservation insufficient at match", decision.Reason)
		})
	}
}

func TestDeciderSwapRealizedPnLCapsClosingQuantity(t *testing.T) {
	for _, tc := range []struct{ position, side, want string }{
		{"0.4", "SELL", "8"}, {"-0.4", "BUY", "-8"}, {"0.4", "BUY", "0"}, {"-0.4", "SELL", "0"},
	} {
		t.Run(tc.position+tc.side, func(t *testing.T) {
			d, _, order := deciderFixture(t, "SWAP")
			require.NoError(t, d.Store.Transaction(context.Background(), func(tx *store.Tx) error {
				return tx.UpsertPosition(store.PositionRecord{SpaceID: "space", TradingAccountID: "paper", InstrumentID: "BTC-USDT",
					ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: tc.position, EntryPrice: "100", Leverage: "10"})
			}))
			price := "120"
			order.PaperExecutionPrice, order.Side = &price, tc.side
			decision, err := d.Decide(context.Background(), order)
			require.NoError(t, err)
			require.False(t, decision.Cancel || decision.Rest)
			require.Equal(t, tc.want, decision.Fill.RealizedPnL.String())
		})
	}
}

func TestDeciderReferenceOnlySourceUsesClockAndSlippage(t *testing.T) {
	for _, side := range []string{"BUY", "SELL"} {
		t.Run(side, func(t *testing.T) {
			d, adapter, order := deciderFixture(t, "SPOT")
			ref := deciderReferenceAdapter{snapshot: adapter.snapshot,
				quote: exchange.ReferencePrice{Price: shared.MustDecimal("100"), UpdatedAt: d.Now()},
			}
			d.Adapters = deciderAdapters{ref}
			order.Side = side
			decision, err := d.Decide(context.Background(), order)
			require.NoError(t, err)
			require.False(t, decision.Cancel || decision.Rest)
			want := "101"
			if side == "SELL" {
				want = "99"
			}
			require.Equal(t, want, decision.Fill.Price.String())
			for _, invalid := range []time.Time{d.Now().Add(-11 * time.Second), {}, d.Now().Add(time.Nanosecond)} {
				ref.quote.UpdatedAt = invalid
				d.Adapters = deciderAdapters{ref}
				decision, err = d.Decide(context.Background(), order)
				require.ErrorContains(t, err, "stale")
				require.False(t, decision.Cancel || decision.Rest)
			}
		})
	}
}

func TestDeciderStorageFailureIsInfrastructureError(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "closed.db"))
	require.NoError(t, err)
	require.NoError(t, db.Close())
	d := Decider{Store: db, Adapters: deciderAdapters{&deciderAdapter{}}}
	_, err = d.Decide(context.Background(), store.OrderRecord{SpaceID: "space", TradingAccountID: "paper"})
	var infrastructure InfrastructureError
	require.ErrorAs(t, err, &infrastructure)
	require.ErrorContains(t, infrastructure.Err, "database is closed")
}
