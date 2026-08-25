package test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	paperexec "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestPaperMatcherRestingGTCRecoversAfterSQLiteRestart(t *testing.T) {
	ctx := context.Background()
	f := newRestingPaperFixture(t, exchange.MarketTypeSpot)
	spec := marketSpec("restart-gtc", exchange.SideBuy, "0.01")
	spec.Type = exchange.OrderTypeLimit
	spec.FillPolicy = exchange.FillPolicyGTC
	limit := shared.MustDecimal("49000")
	spec.LimitPrice = &limit

	placed := mustPlace(t, f, spec)
	open, err := f.orders.Submit(ctx, testSpace, string(placed.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, open.State)

	// The first quote does not cross the limit. The matcher clears only the
	// first-decision marker and leaves the order durably OPEN.
	firstMatcher := &paperexec.Matcher{Store: f.store, Decide: func(store.OrderRecord) (paperexec.Decision, error) {
		return paperexec.Decision{Rest: true}, nil
	}}
	require.NoError(t, firstMatcher.Scan(ctx))
	f.close(t)

	restarted, err := store.Open(f.path)
	require.NoError(t, err)
	defer restarted.Close()
	reducer := &consumer.Reducer{Store: restarted}
	secondMatcher := &paperexec.Matcher{
		Store:   restarted,
		Reducer: reducer,
		Decide: func(candidate store.OrderRecord) (paperexec.Decision, error) {
			return paperexec.Decision{Fill: exchange.Fill{
				ExchangeTradeID: "restart-trade",
				ExchangeOrderID: candidate.ExchangeOrderID,
				ClientOrderID:   candidate.ClientOrderID,
				ExchangeSymbol:  candidate.ExchangeSymbol,
				Symbol:          candidate.ExchangeSymbol,
				Side:            exchange.SideBuy,
				Quantity:        shared.MustDecimal("0.01"),
				Price:           shared.MustDecimal("49000"),
				Fee:             shared.Zero(),
				FeeAsset:        "USDT",
				SettlementAsset: "USDT",
				LiquidityRole:   "MAKER",
				TradedAt:        testNow,
			}}, nil
		},
	}
	require.NoError(t, secondMatcher.Scan(ctx))

	filled, err := restarted.GetOrder(ctx, testSpace, string(placed.ID))
	require.NoError(t, err)
	require.Equal(t, "FILLED", filled.State)
	fills, total, err := restarted.ListFills(ctx, testSpace, store.FillQuery{
		TradingAccountID: testAccount, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, fills, 1)

	// Replaying the restart scan is idempotent and cannot create a second Fill.
	require.NoError(t, secondMatcher.Scan(ctx))
	_, total, err = restarted.ListFills(ctx, testSpace, store.FillQuery{
		TradingAccountID: testAccount, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
}

func newRestingPaperFixture(t *testing.T, market exchange.MarketType) *fixture {
	t.Helper()
	path := filepathForTest(t)
	tradeStore, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tradeStore.Close() })
	fake := newFakeExchange(market)
	seedFixture(t, tradeStore, market, fake.instrument)
	account, err := tradeStore.GetTradingAccountByID(context.Background(), testAccount)
	require.NoError(t, err)
	base := &paperexec.Adapter{Account: account, Store: tradeStore, MarketData: fake}
	_, err = base.LoadInstruments(context.Background())
	require.NoError(t, err)
	return buildFixture(tradeStore, path, fake, recordingAdapter{ExecutionAdapter: base, recorder: fake})
}

func filepathForTest(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "trade.db")
}
