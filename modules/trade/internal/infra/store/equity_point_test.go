package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpsertAccountEquityPointRejectsOlderSource(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(TradingAccountRecord{
			SpaceID: "space", TradingAccountID: "account", Name: "Paper",
			Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "PAPER",
			SettlementAsset: "USDT", Status: "ENABLED",
		}); err != nil {
			return err
		}
		return tx.UpsertAccountEquityPoint(EquityPointRecord{
			SpaceID: "space", TradingAccountID: "account", BucketTime: 60_000,
			Equity: "100", AvailableFunds: "90", UsedMargin: "10", SourceTime: 200,
		})
	}))
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpsertAccountEquityPoint(EquityPointRecord{
			SpaceID: "space", TradingAccountID: "account", BucketTime: 60_000,
			Equity: "1", AvailableFunds: "1", UsedMargin: "0", SourceTime: 100,
		})
	}))
	points, err := s.ListAccountEquityPoints(ctx, "space", "account", 0, 0)
	require.NoError(t, err)
	require.Len(t, points, 1)
	require.Equal(t, "100", points[0].Equity)
}
