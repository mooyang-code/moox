package store

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func cacheTestInstrument(tick string) InstrumentRecord {
	return InstrumentRecord{
		Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SPOT",
		BaseAsset: "BTC", QuoteAsset: "USDT", PriceTick: tick, ExchangeQuantityStep: "0.001", Status: "TRADING",
	}
}

func countInstrumentQueries(t *testing.T, s *Store) *int {
	t.Helper()
	count := 0
	require.NoError(t, s.DBForTest().Callback().Query().After("gorm:query").Register("test:count_instruments", func(db *gorm.DB) {
		if db.Statement.Table == "t_trade_instruments" {
			count++
		}
	}))
	return &count
}

// 标的定义命中缓存时不查库；写入后读到新值。
func TestInstrumentCacheServesReadsAndInvalidatesOnWrite(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.UpsertInstrument(cacheTestInstrument("0.01")) }))
	queries := countInstrumentQueries(t, s)
	for range 5 {
		record, err := s.GetInstrument(ctx, "BINANCE", "SPOT", "BTCUSDT")
		require.NoError(t, err)
		require.Equal(t, "0.01", record.PriceTick)
	}
	require.Equal(t, 1, *queries)
	_, err := s.GetInstrument(ctx, "BINANCE", "SPOT", "ETHUSDT")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.UpsertInstrument(cacheTestInstrument("0.1")) }))
	record, err := s.GetInstrument(ctx, "BINANCE", "SPOT", "BTCUSDT")
	require.NoError(t, err)
	require.Equal(t, "0.1", record.PriceTick)
}

// 事务内写入后读到自己的写入；回滚后缓存里不留未提交的值。
func TestInstrumentCacheIgnoresUncommittedWrites(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.UpsertInstrument(cacheTestInstrument("0.01")) }))
	abort := errors.New("abort")
	require.ErrorIs(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.UpsertInstrument(cacheTestInstrument("0.5")); err != nil {
			return err
		}
		record, err := tx.GetInstrument("BINANCE", "SPOT", "BTCUSDT")
		require.NoError(t, err)
		require.Equal(t, "0.5", record.PriceTick)
		return abort
	}), abort)
	record, err := s.GetInstrument(ctx, "BINANCE", "SPOT", "BTCUSDT")
	require.NoError(t, err)
	require.Equal(t, "0.01", record.PriceTick)
}

// 绕过存储层直接改表后，清缓存才能读到新值（系统重置清表就是这种情况）。
func TestInvalidateCachesAfterDirectTableChange(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.UpsertInstrument(cacheTestInstrument("0.01")) }))
	_, err := s.GetInstrument(ctx, "BINANCE", "SPOT", "BTCUSDT")
	require.NoError(t, err)
	require.NoError(t, s.DBForTest().Exec("DELETE FROM t_trade_instruments").Error)
	s.InvalidateCaches()
	_, err = s.GetInstrument(ctx, "BINANCE", "SPOT", "BTCUSDT")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// 账户创建回滚后，身份缓存里不留这个账户。
func TestAccountIdentityCacheIgnoresRolledBackAccount(t *testing.T) {
	s := openTestStore(t)
	abort := errors.New("abort")
	require.ErrorIs(t, s.Transaction(context.Background(), func(tx *Tx) error {
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		identity, err := tx.accountIdentity("space-1", "account-1")
		require.NoError(t, err)
		require.Equal(t, "BINANCE", identity.Exchange)
		return abort
	}), abort)
	require.Error(t, s.Transaction(context.Background(), func(tx *Tx) error {
		_, err := tx.accountIdentity("space-1", "account-1")
		return err
	}))
}
