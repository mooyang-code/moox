package test

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func TestPaperSnapshotCreditsSignedFeeRebateExactlyOnce(t *testing.T) {
	ctx := context.Background()
	f := newProductionPaperFixture(t, exchange.MarketTypeSpot)
	placed := mustPlace(t, f, marketSpec("paper-rebate", exchange.SideBuy, "0.01"))
	opened, err := f.orders.Submit(ctx, testSpace, string(placed.ID))
	require.NoError(t, err)
	fill := exchange.Fill{
		ExchangeTradeID: "paper-rebate-fill", ExchangeOrderID: opened.ExchangeOrderID,
		ClientOrderID: "paper-rebate", ExchangeSymbol: testSymbol, Side: exchange.SideBuy,
		Quantity: shared.MustDecimal("0.01"), Price: shared.MustDecimal("50000"),
		Fee: shared.MustDecimal("-0.5"), FeeAsset: "USDT", TradedAt: testNow,
	}
	source := consumer.Source{SpaceID: testSpace, TradingAccountID: testAccount, Kind: consumer.OriginPaperMatcher}
	inserted, err := f.reducer.ApplyFill(ctx, fill, source)
	require.NoError(t, err)
	require.True(t, inserted)
	inserted, err = f.reducer.ApplyFill(ctx, fill, source)
	require.NoError(t, err)
	require.False(t, inserted)
	snapshot, err := f.adapter.(recordingAdapter).ExecutionAdapter.(interface {
		GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error)
	}).GetAccountSnapshot(ctx)
	require.NoError(t, err)
	balances := make(map[string]string)
	for _, balance := range snapshot.Balances {
		balances[balance.Asset] = balance.Total.String()
	}
	require.Equal(t, "99500.5", balances["USDT"])
	require.Equal(t, "0.01", balances["BTC"])
}
