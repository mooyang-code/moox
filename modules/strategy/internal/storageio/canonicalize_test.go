package storageio

import (
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/stretchr/testify/require"
)

func TestViewSnapshotRestrictSelectorsKeepsPoolSubjects(t *testing.T) {
	snap := &viewSnapshot{
		selectors: map[string][]*storagepb.TimeSeriesSelector{
			"view_binance_kline_1m": {
				{SubjectId: "BTC-USDT", Freq: "1m"},
				{SubjectId: "ETH-USDT", Freq: "1m"},
			},
		},
	}
	snap.RestrictSelectors([]input.PoolItem{{SubjectID: "BTC-USDT"}})
	require.Len(t, snap.selectors["view_binance_kline_1m"], 1)
	require.Equal(t, "BTC-USDT", snap.selectors["view_binance_kline_1m"][0].GetSubjectId())
	require.Nil(t, snap.selectors["view_binance_kline_1m"][0].SeriesTag)
}

func TestViewSnapshotRestrictSelectorsRemovesAllWhenPoolDoesNotMatch(t *testing.T) {
	snap := &viewSnapshot{
		selectors: map[string][]*storagepb.TimeSeriesSelector{
			"view_binance_kline_1m": {{SubjectId: "BTC-USDT", Freq: "1m"}},
		},
	}
	snap.RestrictSelectors([]input.PoolItem{{SubjectID: "ETH-USDT"}})
	require.Empty(t, snap.selectors["view_binance_kline_1m"])
}

func TestViewSnapshotRestrictSelectorsClearsForEmptyPool(t *testing.T) {
	snap := &viewSnapshot{
		selectors: map[string][]*storagepb.TimeSeriesSelector{
			"view_binance_kline_1m": {{SubjectId: "BTC-USDT", Freq: "1m"}},
		},
	}
	snap.RestrictSelectors(nil)
	require.Empty(t, snap.selectors["view_binance_kline_1m"])
	rows, err := snap.readPinnedRowsWithRequirement(t.Context(), "crypto", "view_binance_kline_1m", time.Time{}, time.Time{}, false)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestViewSnapshotRestrictSelectorsPreservesSeriesTags(t *testing.T) {
	spot, swap := "spot", "swap"
	snap := &viewSnapshot{
		selectors: map[string][]*storagepb.TimeSeriesSelector{
			"view_binance_kline_1m": {
				{SubjectId: "BTC-USDT", SeriesTag: &spot, Freq: "1m"},
				{SubjectId: "BTC-USDT", SeriesTag: &swap, Freq: "1m"},
			},
		},
	}
	snap.RestrictSelectors([]input.PoolItem{{SubjectID: "BTC-USDT"}})
	got := snap.selectors["view_binance_kline_1m"]
	require.Len(t, got, 2)
	require.Equal(t, "spot", got[0].GetSeriesTag())
	require.Equal(t, "swap", got[1].GetSeriesTag())
}

func TestPoolItemsForIncludeMatchesCanonicalInstrumentID(t *testing.T) {
	items := poolItemsForInclude([]input.Subject{
		{SubjectID: "BTC-USDT", InstrumentID: "BTC-USDT"},
		{SubjectID: "ETH-USDT", InstrumentID: "ETH-USDT"},
	}, []string{"BTC-USDT"})
	require.Equal(t, []input.PoolItem{{InstrumentID: "BTC-USDT", SubjectID: "BTC-USDT"}}, items)
}
