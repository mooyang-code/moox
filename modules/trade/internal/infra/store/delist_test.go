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
		Exchange: "binance", MarketType: "spot", ExchangeSymbol: "FOOUSDT", Scope: DelistScopeAll,
		Reason: "公告下架", DelistID: "delist-1", BlockedAt: 100,
	}}, records)

	live := TradingAccountRecord{Exchange: "binance", ExecutionMode: "LIVE"}
	set := NewBlockedSet(records)
	require.True(t, set.Contains(live, "spot", "FOOUSDT"))
	require.False(t, set.Contains(live, "swap", "FOOUSDT"), "现货条目不匹配同名永续")
	require.False(t, set.Contains(TradingAccountRecord{Exchange: "okx", ExecutionMode: "LIVE"}, "spot", "FOOUSDT"), "别的交易所不匹配")

	removed, err := s.UnblockInstrument(ctx, "binance", "spot", "FOOUSDT")
	require.NoError(t, err)
	require.True(t, removed)
	removed, err = s.UnblockInstrument(ctx, "binance", "spot", "FOOUSDT")
	require.NoError(t, err)
	require.False(t, removed, "不在名单里不算错误")
}

func TestBlockInstrumentRejectsIncompleteRecord(t *testing.T) {
	s := openTestStore(t)
	_, err := blockInstrument(t, s, BlockedInstrumentRecord{Exchange: "binance", MarketType: "spot", ExchangeSymbol: "FOOUSDT"})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

// 范围 PAPER_ONLY 的名单行只对模拟盘账户生效，实盘账户看不到它。
func TestBlockedSetPaperOnlyScopeOnlyAppliesToPaperAccounts(t *testing.T) {
	set := NewBlockedSet([]BlockedInstrumentRecord{
		{Exchange: "binance", MarketType: "spot", ExchangeSymbol: "FOOUSDT", Scope: DelistScopePaperOnly},
		{Exchange: "binance", MarketType: "spot", ExchangeSymbol: "BARUSDT", Scope: DelistScopeAll},
	})
	live := TradingAccountRecord{Exchange: "binance", ExecutionMode: "LIVE"}
	paper := TradingAccountRecord{Exchange: "binance", ExecutionMode: "PAPER"}
	require.False(t, set.Contains(live, "spot", "FOOUSDT"))
	require.True(t, set.Contains(paper, "spot", "FOOUSDT"))
	require.True(t, set.Contains(live, "spot", "BARUSDT"))
	require.True(t, set.Contains(paper, "spot", "BARUSDT"))
	instruments := []InstrumentRecord{
		{MarketType: "spot", ExchangeSymbol: "FOOUSDT", BaseAsset: "FOO"},
		{MarketType: "spot", ExchangeSymbol: "BARUSDT", BaseAsset: "BAR"},
	}
	require.Equal(t, map[string]struct{}{"BAR": {}}, set.SpotBaseAssets(live, instruments))
	require.Equal(t, map[string]struct{}{"FOO": {}, "BAR": {}}, set.SpotBaseAssets(paper, instruments))
}

func TestBlockedSetSpotBaseAssetsOnlyFollowsSpotEntries(t *testing.T) {
	set := NewBlockedSet([]BlockedInstrumentRecord{
		{Exchange: "binance", MarketType: "spot", ExchangeSymbol: "FOOUSDT"},
		{Exchange: "binance", MarketType: "swap", ExchangeSymbol: "BARUSDT"},
	})
	instruments := []InstrumentRecord{
		{MarketType: "spot", ExchangeSymbol: "FOOUSDT", BaseAsset: "FOO"},
		{MarketType: "swap", ExchangeSymbol: "FOOUSDT", BaseAsset: "FOO"},
		{MarketType: "spot", ExchangeSymbol: "BARUSDT", BaseAsset: "BAR"},
		{MarketType: "swap", ExchangeSymbol: "BARUSDT", BaseAsset: "BAR"},
	}
	require.Equal(t, map[string]struct{}{"FOO": {}}, set.SpotBaseAssets(TradingAccountRecord{Exchange: "binance"}, instruments),
		"永续下架不能把同名现货余额也豁免掉")
	require.Empty(t, set.SpotBaseAssets(TradingAccountRecord{Exchange: "okx"}, instruments))
}

// 解除名单在同一事务里取消这个标的所有进行中的下架记录（两种范围都算），已结束的不动。
func TestUnblockInstrumentCancelsActiveDelists(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	insert := func(id, scope, status string) {
		require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
			return tx.InsertDelist(DelistRecord{
				DelistID: id, Exchange: "binance", MarketType: "spot", ExchangeSymbol: "FOOUSDT", Scope: scope,
				DelistAt: 1000, Source: "MANUAL", SourceEventID: "evt-" + id, EventOccurredAt: 1, Status: status,
				CreatedAt: 1, UpdatedAt: 1,
			})
		}))
	}
	insert("all", DelistScopeAll, DelistActive)
	insert("paper", DelistScopePaperOnly, DelistActive)
	insert("old", DelistScopeAll, DelistRescheduled)
	_, err := blockInstrument(t, s, BlockedInstrumentRecord{Exchange: "binance", MarketType: "spot", ExchangeSymbol: "FOOUSDT", Reason: "r", DelistID: "all"})
	require.NoError(t, err)

	removed, err := s.UnblockInstrument(ctx, "binance", "spot", "FOOUSDT")
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

// 名单的交易所与市场按 Trade 的写法归一，交易所代码一律转大写。
func TestNormalizeBlockKeySplitsExchangeAndSymbol(t *testing.T) {
	exchangeName, marketType, symbol := normalizeBlockKey(" BINANCE ", "SWAP", " fooUSDT ")
	require.Equal(t, []string{"binance", "swap", "FOOUSDT"}, []string{exchangeName, marketType, symbol})
}
