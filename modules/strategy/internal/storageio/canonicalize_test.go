package storageio

import (
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/stretchr/testify/require"
)

func TestCanonicalizeMergedCatalogSubjectsStripsVenueSuffixAndDedupes(t *testing.T) {
	subjects := []input.Subject{
		{SubjectID: "BTC-USDT-SPOT", InstrumentID: "BTC-USDT-SPOT", Exchange: "binance"},
		{SubjectID: "BTC-USDT-SWAP", InstrumentID: "BTC-USDT-SWAP", Exchange: "binance"},
		{SubjectID: "ETH-USDT-SPOT", InstrumentID: "ETH-USDT-SPOT", Exchange: "binance"},
	}

	got := canonicalizeMergedCatalogSubjects("mdataset_binance_kline_1m", subjects)
	require.Equal(t, []input.Subject{
		{SubjectID: "BTC-USDT", InstrumentID: "BTC-USDT", Exchange: "binance"},
		{SubjectID: "ETH-USDT", InstrumentID: "ETH-USDT", Exchange: "binance"},
	}, got)
}

func TestCanonicalizeMergedCatalogSubjectsClearsVenueSeriesTag(t *testing.T) {
	got := canonicalizeMergedCatalogSubjects("mdataset_binance_kline_1m", []input.Subject{
		{SubjectID: "BTC-USDT-SPOT", InstrumentID: "BTC-USDT-SPOT", SeriesTag: "venue:binance"},
	})
	require.Equal(t, "", got[0].SeriesTag)
}

func TestCanonicalizeMergedCatalogSubjectsLeavesRawDatasetsUnchanged(t *testing.T) {
	subjects := []input.Subject{
		{SubjectID: "BTC-USDT-SPOT", InstrumentID: "BTC-USDT-SPOT"},
	}
	got := canonicalizeMergedCatalogSubjects("dataset_binance_spot_kline_1m", subjects)
	require.Equal(t, subjects, got)
}

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

func TestViewSnapshotRestrictSelectorsRewritesVenueSuffixToPoolSubject(t *testing.T) {
	snap := &viewSnapshot{
		selectors: map[string][]*storagepb.TimeSeriesSelector{
			"view_binance_kline_1m": {
				{SubjectId: "BTC-USDT-SPOT", Freq: "1m"},
				{SubjectId: "BTC-USDT-SWAP", Freq: "1m"},
				{SubjectId: "ETH-USDT-SPOT", Freq: "1m"},
			},
		},
	}
	snap.RestrictSelectors([]input.PoolItem{{SubjectID: "BTC-USDT"}})
	require.Len(t, snap.selectors["view_binance_kline_1m"], 1)
	require.Equal(t, "BTC-USDT", snap.selectors["view_binance_kline_1m"][0].GetSubjectId())
	require.Nil(t, snap.selectors["view_binance_kline_1m"][0].SeriesTag)
}

func TestCanonicalizeMergedSelectorsStripsVenueSuffixAndDedupes(t *testing.T) {
	got := canonicalizeMergedSelectors("mdataset_binance_kline_1m", []*storagepb.TimeSeriesSelector{
		{SubjectId: "BTC-USDT-SPOT", DatasetId: "mdataset_binance_kline_1m", Freq: "1m"},
		{SubjectId: "BTC-USDT-SWAP", DatasetId: "mdataset_binance_kline_1m", Freq: "1m"},
		{SubjectId: "ETH-USDT-SPOT", DatasetId: "mdataset_binance_kline_1m", Freq: "1m"},
	})
	require.Len(t, got, 2)
	require.Equal(t, "BTC-USDT", got[0].GetSubjectId())
	require.Equal(t, "ETH-USDT", got[1].GetSubjectId())
	require.Nil(t, got[0].SeriesTag)
	require.Nil(t, got[1].SeriesTag)
}

func TestPoolItemsForIncludeMatchesCanonicalInstrumentID(t *testing.T) {
	items := poolItemsForInclude([]input.Subject{
		{SubjectID: "BTC-USDT", InstrumentID: "BTC-USDT"},
		{SubjectID: "ETH-USDT", InstrumentID: "ETH-USDT"},
	}, []string{"BTC-USDT"})
	require.Equal(t, []input.PoolItem{{InstrumentID: "BTC-USDT", SubjectID: "BTC-USDT"}}, items)
}
