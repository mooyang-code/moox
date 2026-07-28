package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func TestOpenConfiguresSQLiteAndTransactionRollback(t *testing.T) {
	s := openTestStore(t)

	var foreignKeys, busyTimeout int
	require.NoError(t, s.db.Raw(`PRAGMA foreign_keys`).Scan(&foreignKeys).Error)
	require.Equal(t, 1, foreignKeys)
	require.NoError(t, s.db.Raw(`PRAGMA busy_timeout`).Scan(&busyTimeout).Error)
	require.Equal(t, sqliteBusyTimeoutMS, busyTimeout)

	stop := errors.New("stop")
	err := s.Transaction(context.Background(), func(tx *Tx) error {
		require.NoError(t, tx.UpsertInstrument(InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTCUSDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", PriceTick: "0.01",
			QuantityStep: "0.0001", ContractSize: "1", Status: "TRADING",
		}))
		return stop
	})
	require.ErrorIs(t, err, stop)

	var count int64
	require.NoError(t, s.db.Table("t_exchange_instruments").Count(&count).Error)
	require.Zero(t, count)
}
