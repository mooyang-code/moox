package test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/reservation"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	paperexec "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/stretchr/testify/require"
)

func TestLivePaperParityE2EUsesOneOrderPolicyAndPaperPrice(t *testing.T) {
	account := tradingaccount.Account{
		ID: "paper-1", SpaceID: "space-1", Name: "paper", Exchange: exchange.ExchangeBinance,
		MarketType: exchange.MarketTypeSpot, ExecutionMode: exchange.ExecutionModePaper,
		Environment: exchange.AccountEnvironmentPaper, SettlementAsset: "USDT",
		Status: exchange.AccountStatusEnabled, Ready: true,
		Paper: &tradingaccount.PaperConfig{InitialBalance: shared.MustDecimal("1000"), TakerFeeRate: shared.MustDecimal("0.001")},
	}
	instrument := exchange.Instrument{
		Exchange: exchange.ExchangeBinance, MarketType: exchange.MarketTypeSpot,
		Symbol: "BTCUSDT", ExchangeSymbol: "BTCUSDT", InstrumentID: "btc-usdt",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
		ExchangeQuantityStep: shared.MustDecimal("0.001"), PriceTick: shared.MustDecimal("0.1"), Status: "TRADING",
	}
	quote := execution.MarketQuote{Bid: shared.MustDecimal("100"), Ask: shared.MustDecimal("101"), Last: shared.MustDecimal("100.5"), SourceTime: time.UnixMilli(1_700_000_000_000)}
	price, err := paperexec.MarketExecutionPrice(exchange.SideBuy, quote, shared.MustDecimal("10"))
	require.NoError(t, err)
	require.Equal(t, "101.101", price.String())
	policy, err := (execution.PaperReservationPolicy{}).Evaluate(account, instrument, order.OrderSpec{
		ClientOrderSpec: order.ClientOrderSpec{TradingAccountID: account.ID, ClientOrderID: "client-1", InstrumentID: instrument.InstrumentID, Type: exchange.OrderTypeMarket, Side: exchange.SideBuy, Quantity: shared.MustDecimal("2")},
		ReferencePrice:  shared.MustDecimal("101"),
	}, quote, reservation.Facts{AvailableReducibleQuantity: shared.Zero()})
	require.NoError(t, err)
	require.Equal(t, "202.202", policy.Quantity.String())
}

func TestPaperMatcherRestartAndCASE2E(t *testing.T) {
	s := openUnifiedE2EStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{SpaceID: "space-1", TradingAccountID: "paper-1", Name: "paper", Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT", Status: "ENABLED"}); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(store.InstrumentRecord{Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTCUSDT", InstrumentID: "btc-usdt", BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT", ExchangeQuantityStep: "0.001", PriceTick: "0.1", Status: "TRADING"}); err != nil {
			return err
		}
		return tx.CreateOrder(store.OrderRecord{SpaceID: "space-1", OrderID: "order-1", TradingAccountID: "paper-1", ClientOrderID: "client-1", Symbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY", Quantity: "1", ReferencePrice: "100", OwnerType: "EXTERNAL", OwnerID: "e2e", State: "OPEN", FirstMatchPending: true, Version: 1})
	}))
	candidates, err := s.ListPaperMatchCandidates(ctx, 10)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	m := &paperexec.Matcher{Store: s, Decide: func(store.OrderRecord) (paperexec.Decision, error) {
		return paperexec.Decision{Cancel: true, Reason: "stale quote"}, nil
	}}
	require.NoError(t, m.Scan(ctx))
	got, err := s.GetOrder(ctx, "space-1", "order-1")
	require.NoError(t, err)
	require.Equal(t, "CANCELED", got.State)
	require.Equal(t, "0", got.RemainingReservedQuantity)
}

func TestPaperMatcherProductionAdapterFillAndRestartE2E(t *testing.T) {
	ctx := context.Background()
	s := openUnifiedE2EStore(t)
	fake := newFakeExchange(exchange.MarketTypeSpot)
	seedFixture(t, s, exchange.MarketTypeSpot, fake.instrument)
	account, err := s.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	market := deterministicMarketData{instrument: fake.instrument}
	adapter := &paperexec.Adapter{Account: account, Store: s, MarketData: market}
	_, err = adapter.LoadInstruments(ctx)
	require.NoError(t, err)

	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: testSpace, OrderID: "paper-match-order", TradingAccountID: testAccount,
			ClientOrderID: "paper-match-client", ExchangeOrderID: "paper-order-e2e",
			Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: fake.instrument.InstrumentID,
			ExchangeSymbol: testSymbol, Symbol: testSymbol, OrderType: "MARKET", Side: "BUY",
			Quantity: "1", ReferencePrice: "100", ReferencePriceAt: testNow.UnixMilli(),
			OwnerType: "EXTERNAL", OwnerID: "paper-match", State: "OPEN",
			FilledQuantity: "0", AveragePrice: "0", ReservedAsset: "USDT",
			ReservedQuantity: "101.2", RemainingReservedQuantity: "101.2",
			FirstMatchPending: true, Version: 1,
		})
	}))

	reducer := &consumer.Reducer{Store: s, Now: func() time.Time { return testNow }}
	matcher := &paperexec.Matcher{Store: s, Reducer: reducer, DecideContext: func(ctx context.Context, candidate store.OrderRecord) (paperexec.Decision, error) {
		quote, err := adapter.GetQuote(ctx, shared.ExchangeSymbol(candidate.ExchangeSymbol))
		if err != nil {
			return paperexec.Decision{}, err
		}
		price, err := paperexec.MarketExecutionPrice(exchange.SideBuy, quote, shared.Zero())
		if err != nil {
			return paperexec.Decision{}, err
		}
		return paperexec.Decision{Fill: exchange.Fill{
			ExchangeTradeID: "paper-match-trade", ExchangeOrderID: candidate.ExchangeOrderID,
			ClientOrderID: candidate.ClientOrderID, ExchangeSymbol: candidate.ExchangeSymbol,
			Symbol: candidate.ExchangeSymbol, Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"),
			Price: price, Fee: shared.Zero(), SettlementAsset: "USDT", FeeAsset: "USDT",
			LiquidityRole: "TAKER", TradedAt: testNow,
		}}, nil
	}}
	require.NoError(t, matcher.Scan(ctx))
	order, err := s.GetOrder(ctx, testSpace, "paper-match-order")
	require.NoError(t, err)
	require.Equal(t, "FILLED", order.State)
	require.Equal(t, "101", order.AveragePrice)
	fills, _, err := s.ListFills(ctx, testSpace, store.FillQuery{TradingAccountID: testAccount, Limit: 10})
	require.NoError(t, err)
	require.Len(t, fills, 1)

	// A restart/repeated tick must not create a second fill.
	require.NoError(t, matcher.Scan(ctx))
	fills, _, err = s.ListFills(ctx, testSpace, store.FillQuery{TradingAccountID: testAccount, Limit: 10})
	require.NoError(t, err)
	require.Len(t, fills, 1)
}

type deterministicMarketData struct{ instrument exchange.Instrument }

func (m deterministicMarketData) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return []exchange.Instrument{m.instrument}, nil
}

func (deterministicMarketData) GetQuote(context.Context, shared.ExchangeSymbol) (execution.MarketQuote, error) {
	return execution.MarketQuote{
		Bid: shared.MustDecimal("100"), Ask: shared.MustDecimal("101"), Last: shared.MustDecimal("100.5"),
		SourceTime: testNow,
	}, nil
}

func TestEquitySamplerCoalescesAndPaperSimulationClosesE2E(t *testing.T) {
	ctx := context.Background()
	s := openUnifiedE2EStore(t)
	service := &papersimulation.Service{Store: s}
	created, err := service.Create(ctx, papersimulation.CreateCommand{SpaceID: "space-1", AccountName: "account", LogicalAccountName: "logical", Exchange: exchange.ExchangeBinance, MarketType: exchange.MarketTypeSpot, SettlementAsset: "USDT", InitialBalance: shared.MustDecimal("1000"), MakerFeeRate: shared.Zero(), TakerFeeRate: shared.MustDecimal("0.001"), SlippageBPS: shared.MustDecimal("5")})
	require.NoError(t, err)
	var mu sync.Mutex
	var sampled []string
	sampler := traderuntime.NewEquitySampler(accountSamplerFunc(func(_ context.Context, id string) error {
		mu.Lock()
		sampled = append(sampled, id)
		mu.Unlock()
		return nil
	}))
	sampler.Enqueue(created.Account.TradingAccountID)
	sampler.Enqueue(created.Account.TradingAccountID)
	require.NoError(t, sampler.RunPending(ctx))
	require.Equal(t, []string{created.Account.TradingAccountID}, sampled)
	require.NoError(t, service.Close(ctx, "space-1", created.Account.TradingAccountID))
	account, err := s.GetTradingAccountByID(ctx, created.Account.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "DISABLED", account.Status)
	logical, err := s.GetLogicalAccount(ctx, "space-1", created.LogicalAccount.LogicalAccountID)
	require.NoError(t, err)
	require.Equal(t, "PAUSED", logical.AutomationState)
}

type accountSamplerFunc func(context.Context, string) error

func (f accountSamplerFunc) SampleAccount(ctx context.Context, id string) error { return f(ctx, id) }

func openUnifiedE2EStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}
