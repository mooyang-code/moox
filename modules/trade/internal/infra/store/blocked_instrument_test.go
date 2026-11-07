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
		Reason: "公告下架", SourceEventID: "evt-1", BlockedAt: 100,
	}
	created, err := s.BlockInstrument(ctx, record)
	require.NoError(t, err)
	require.True(t, created)

	// 重复写入不改已有记录（保留首次的时间和原因），也不报错。
	record.Reason, record.BlockedAt = "别的原因", 200
	created, err = s.BlockInstrument(ctx, record)
	require.NoError(t, err)
	require.False(t, created)

	records, err := s.ListBlockedInstruments(ctx)
	require.NoError(t, err)
	require.Equal(t, []BlockedInstrumentRecord{{
		Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT",
		Reason: "公告下架", SourceEventID: "evt-1", BlockedAt: 100,
	}}, records)

	set := NewBlockedSet(records)
	require.True(t, set.Contains("BINANCE", "SPOT", "FOOUSDT"))
	require.False(t, set.Contains("BINANCE", "SWAP", "FOOUSDT"), "现货条目不匹配同名永续")
	require.False(t, set.Contains("OKX", "SPOT", "FOOUSDT"), "别的交易所不匹配")

	removed, err := s.UnblockInstrument(ctx, "binance", "SPOT", "FOOUSDT")
	require.NoError(t, err)
	require.True(t, removed)
	removed, err = s.UnblockInstrument(ctx, "binance", "SPOT", "FOOUSDT")
	require.NoError(t, err)
	require.False(t, removed, "不在名单里不算错误")
}

func TestBlockInstrumentRejectsIncompleteRecord(t *testing.T) {
	s := openTestStore(t)
	_, err := s.BlockInstrument(context.Background(), BlockedInstrumentRecord{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT"})
	require.ErrorIs(t, err, ErrInvalidRecord)
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
	require.Equal(t, map[string]struct{}{"FOO": {}}, set.SpotBaseAssets("BINANCE", instruments),
		"永续下架不能把同名现货余额也豁免掉")
	require.Empty(t, set.SpotBaseAssets("OKX", instruments))
}
