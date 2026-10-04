package test

import (
	"context"
	"testing"
	"time"

	equityapp "github.com/mooyang-code/moox/modules/trade/internal/application/equity"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/stretchr/testify/require"
)

func TestEquitySamplerPersistsOneMonotonicAccountPointE2E(t *testing.T) {
	ctx := context.Background()
	f := newPaperFixture(t, exchange.MarketTypeSpot)
	equity := &equityapp.Service{
		Store: f.store, Adapters: adapterSource{adapter: f.adapter},
		Now: func() time.Time { return testNow }, SourceMaxAge: time.Minute,
	}
	sampler := traderuntime.NewEquitySampler(equity)
	sampler.Enqueue(testAccount)
	sampler.Enqueue(testAccount)
	require.NoError(t, sampler.RunPending(ctx))

	points, err := f.store.ListAccountEquityPoints(ctx, testSpace, testAccount, 0, 0)
	require.NoError(t, err)
	require.Len(t, points, 1)
	require.Equal(t, "600000", points[0].Equity)

	// A newer source wins, while a later write carrying an older exchange
	// watermark cannot overwrite the same minute bucket.
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSnapshot(testSpace, testAccount, store.TradingAccountSnapshot{
			Balances:          []store.AssetBalance{{Asset: "USDT", Available: "200000", Total: "200000"}},
			Equity:            "200000",
			AvailableFunds:    "200000",
			ExchangeUpdatedAt: testNow.Add(time.Second).UnixMilli(),
		})
	}))
	newer := &equityapp.Service{Store: f.store, Adapters: adapterSource{adapter: f.adapter}, Now: func() time.Time { return testNow }, SourceMaxAge: time.Minute}
	require.NoError(t, newer.SampleAccount(ctx, testAccount))
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSnapshot(testSpace, testAccount, store.TradingAccountSnapshot{
			Balances: []store.AssetBalance{{Asset: "USDT", Available: "1", Total: "1"}},
			Equity:   "1", AvailableFunds: "1", ExchangeUpdatedAt: testNow.UnixMilli(),
		})
	}))
	require.NoError(t, newer.SampleAccount(ctx, testAccount))

	points, err = f.store.ListAccountEquityPoints(ctx, testSpace, testAccount, 0, 0)
	require.NoError(t, err)
	require.Len(t, points, 1)
	require.Equal(t, "200000", points[0].Equity)
}
