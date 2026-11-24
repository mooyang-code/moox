package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBlockedInstrumentLifecycleIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	record := BlockedInstrumentRecord{
		Exchange: " binance ", MarketType: "spot", ExchangeSymbol: " FOOUSDT ",
		Reason: "公告下架", DelistID: " delist-1 ", BlockedAt: 100,
	}
	created, err := blockInstrument(t, s, record)
	require.NoError(t, err)
	require.True(t, created)

	// 重复写入不改已有记录，也不报错。
	record.Reason, record.DelistID, record.BlockedAt = "别的原因", "delist-2", 200
	created, err = blockInstrument(t, s, record)
	require.NoError(t, err)
	require.False(t, created)

	records, err := s.ListBlockedInstruments(ctx)
	require.NoError(t, err)
	require.Equal(t, []BlockedInstrumentRecord{{
		Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT", Scope: DelistScopeAll,
		Reason: "公告下架", DelistID: "delist-1", BlockedAt: 100,
	}}, records)

	live := TradingAccountRecord{Exchange: "BINANCE", ExecutionMode: "LIVE"}
	set := NewBlockedSet(records)
	require.True(t, set.Contains(live, "SPOT", "FOOUSDT"))
	require.False(t, set.Contains(live, "SWAP", "FOOUSDT"), "现货条目不匹配同名永续")
	require.False(t, set.Contains(TradingAccountRecord{Exchange: "OKX", ExecutionMode: "LIVE"}, "SPOT", "FOOUSDT"), "别的交易所不匹配")

	removed, err := s.UnblockInstrument(ctx, "binance", "SPOT", "FOOUSDT")
	require.NoError(t, err)
	require.True(t, removed)
	removed, err = s.UnblockInstrument(ctx, "binance", "SPOT", "FOOUSDT")
	require.NoError(t, err)
	require.False(t, removed, "不在名单里不算错误")
}

func TestBlockInstrumentRejectsIncompleteRecord(t *testing.T) {
	s := openTestStore(t)
	_, err := blockInstrument(t, s, BlockedInstrumentRecord{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT"})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

// 范围 PAPER_ONLY 的名单行只对模拟盘账户生效，实盘账户看不到它。
func TestBlockedSetPaperOnlyScopeOnlyAppliesToPaperAccounts(t *testing.T) {
	set := NewBlockedSet([]BlockedInstrumentRecord{
		{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT", Scope: DelistScopePaperOnly},
		{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BARUSDT", Scope: DelistScopeAll},
	})
	live := TradingAccountRecord{Exchange: "BINANCE", ExecutionMode: "LIVE"}
	paper := TradingAccountRecord{Exchange: "BINANCE", ExecutionMode: "PAPER"}
	require.False(t, set.Contains(live, "SPOT", "FOOUSDT"))
	require.True(t, set.Contains(paper, "SPOT", "FOOUSDT"))
	require.True(t, set.Contains(live, "SPOT", "BARUSDT"))
	require.True(t, set.Contains(paper, "SPOT", "BARUSDT"))
	instruments := []InstrumentRecord{
		{MarketType: "SPOT", ExchangeSymbol: "FOOUSDT", BaseAsset: "FOO"},
		{MarketType: "SPOT", ExchangeSymbol: "BARUSDT", BaseAsset: "BAR"},
	}
	require.Equal(t, map[string]struct{}{"BAR": {}}, set.SpotBaseAssets(live, instruments))
	require.Equal(t, map[string]struct{}{"FOO": {}, "BAR": {}}, set.SpotBaseAssets(paper, instruments))
}

func TestBlockedSetSpotBaseAssetsOnlyFollowsSpotEntries(t *testing.T) {
	set := NewBlockedSet([]BlockedInstrumentRecord{
		{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT"},
		{Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "BARUSDT"},
	})
	instruments := []InstrumentRecord{
		{MarketType: "SPOT", ExchangeSymbol: "FOOUSDT", BaseAsset: "FOO"},
		{MarketType: "SWAP", ExchangeSymbol: "FOOUSDT", BaseAsset: "FOO"},
		{MarketType: "SPOT", ExchangeSymbol: "BARUSDT", BaseAsset: "BAR"},
		{MarketType: "SWAP", ExchangeSymbol: "BARUSDT", BaseAsset: "BAR"},
	}
	require.Equal(t, map[string]struct{}{"FOO": {}}, set.SpotBaseAssets(TradingAccountRecord{Exchange: "BINANCE"}, instruments),
		"永续下架不能把同名现货余额也豁免掉")
	require.Empty(t, set.SpotBaseAssets(TradingAccountRecord{Exchange: "OKX"}, instruments))
}

// 解除名单在同一事务里取消这个标的所有进行中的下架记录（两种范围都算），已结束的不动。
func TestUnblockInstrumentCancelsActiveDelists(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	insert := func(id, scope, status string) {
		require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
			return tx.InsertDelist(DelistRecord{
				DelistID: id, Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT", Scope: scope,
				DelistAt: 1000, Source: "MANUAL", SourceEventID: "evt-" + id, EventOccurredAt: 1, Status: status,
				CreatedAt: 1, UpdatedAt: 1,
			})
		}))
	}
	insert("all", DelistScopeAll, DelistActive)
	insert("paper", DelistScopePaperOnly, DelistActive)
	insert("old", DelistScopeAll, DelistRescheduled)
	_, err := blockInstrument(t, s, BlockedInstrumentRecord{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT", Reason: "r", DelistID: "all"})
	require.NoError(t, err)

	removed, err := s.UnblockInstrument(ctx, "BINANCE", "SPOT", "FOOUSDT")
	require.NoError(t, err)
	require.True(t, removed)
	for id, want := range map[string]string{"all": DelistCanceled, "paper": DelistCanceled, "old": DelistRescheduled} {
		record, err := s.GetDelist(ctx, id)
		require.NoError(t, err)
		require.Equal(t, want, record.Status, id)
	}
	active, err := s.ListActiveDelists(ctx)
	require.NoError(t, err)
	require.Empty(t, active)
}

func blockInstrument(t *testing.T, s *Store, record BlockedInstrumentRecord) (created bool, err error) {
	t.Helper()
	err = s.Transaction(context.Background(), func(tx *Tx) error {
		created, err = tx.BlockInstrument(record)
		return err
	})
	return created, err
}
