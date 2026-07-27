package binance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	"github.com/mooyang-code/moox/modules/collector/internal/model/market"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	exchange "github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKlineCollectorCollectStartsAfterStorageWatermark(t *testing.T) {
	watermark := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	store := &fakeKlineStorage{latest: watermark, found: true}
	var requests []*exchange.KlineRequest
	c := &KlineCollector{
		storage: store,
		fetchKlinePage: func(_ context.Context, _ *sources.CollectParams, req *exchange.KlineRequest) ([]*exchange.Kline, error) {
			requests = append(requests, req)
			return []*exchange.Kline{{
				OpenTime: watermark.Add(time.Minute), CloseTime: watermark.Add(2 * time.Minute),
				Open: common.NewDecimal("1"), High: common.NewDecimal("2"), Low: common.NewDecimal("0.5"), Close: common.NewDecimal("1.5"),
				Volume: common.NewDecimal("10"), QuoteVolume: common.NewDecimal("15"), TradeCount: 3,
			}}, nil
		},
		now: func() time.Time { return watermark.Add(time.Hour) },
	}

	result, err := c.CollectWithResult(context.Background(), &sources.CollectParams{
		SpaceID: "space-custom", DatasetID: "kline-custom", InstType: InstTypeSPOT,
		Symbol: "BTCUSDT", SubjectID: "BTC-USDT", Interval: "1m",
	})
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.True(t, requests[0].StartTime.After(watermark))
	require.NotNil(t, store.latestKey)
	assert.Equal(t, "space-custom", store.latestKey.GetSpaceId())
	assert.Equal(t, "kline-custom", store.latestKey.GetDatasetId())
	require.Len(t, store.writes, 1)
	require.Len(t, store.writes[0], 1)
	assert.Equal(t, "space-custom", store.writes[0][0].GetKey().GetSpaceId())
	assert.Equal(t, "kline-custom", store.writes[0][0].GetKey().GetDatasetId())
	assert.Equal(t, "BTC-USDT", store.writes[0][0].GetKey().GetTimeSeries().GetSubjectId())
	assert.Equal(t, 1, result.RowsWritten)
	require.Len(t, result.WrittenRowKeySamples, 1)
	assert.Equal(t, "BTC-USDT", result.WrittenRowKeySamples[0].SubjectID)
	require.NotNil(t, result.StorageReadScope)
	assert.Equal(t, "kline-custom", result.StorageReadScope.DatasetID)
	assert.Equal(t, "1m", result.StorageReadScope.Freq)
	assert.Empty(t, result.ZeroWriteReason)
}

func TestKlineCollectorOnlyUnclosedBarCompletesWithoutWrite(t *testing.T) {
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	store := &fakeKlineStorage{}
	c := &KlineCollector{
		storage: store,
		fetchKlinePage: func(_ context.Context, _ *sources.CollectParams, _ *exchange.KlineRequest) ([]*exchange.Kline, error) {
			return []*exchange.Kline{{OpenTime: now, CloseTime: now.Add(time.Minute)}}, nil
		},
		now: func() time.Time { return now },
	}

	result, err := c.CollectWithResult(context.Background(), &sources.CollectParams{
		SpaceID: "crypto", DatasetID: "kline", InstType: InstTypeSPOT,
		Symbol: "BTCUSDT", SubjectID: "BTC-USDT", Interval: "1m",
	})
	require.NoError(t, err)
	assert.Empty(t, store.writes)
	assert.Zero(t, result.RowsWritten)
	assert.Equal(t, "no_new_closed_kline", result.ZeroWriteReason)
	require.NotNil(t, result.StorageReadScope)
	assert.Equal(t, "BTC-USDT", result.StorageReadScope.SubjectID)
}

type fakeKlineStorage struct {
	latest         time.Time
	found          bool
	latestKey      *storagepb.TimeSeriesKey
	writes         [][]*storagepb.RowFieldUpsert
	latestCalls    int
	latestFailures int
	writeCalls     int
	writeFailures  int
}

func (s *fakeKlineStorage) LatestTimeSeriesTime(_ context.Context, key *storagepb.TimeSeriesKey) (time.Time, bool, error) {
	s.latestCalls++
	if s.latestCalls <= s.latestFailures {
		return time.Time{}, false, errors.New("temporary read failure")
	}
	s.latestKey = key
	return s.latest, s.found, nil
}

func (s *fakeKlineStorage) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	s.writeCalls++
	if s.writeCalls <= s.writeFailures {
		return errors.New("temporary write failure")
	}
	s.writes = append(s.writes, rows)
	return nil
}

func TestFilterSymbols_KeepsActiveUSDTPairs(t *testing.T) {
	c := &SymbolCollector{}
	filtered := c.filterSymbols([]*exchange.SymbolInfo{
		{Symbol: "BTCUSDT", QuoteAsset: "USDT", Status: "active"},
		{Symbol: "ETHBTC", QuoteAsset: "BTC", Status: "active"},
		{Symbol: "XRPUSDT", QuoteAsset: "USDT", Status: "inactive"},
	})
	require.Len(t, filtered, 1)
	assert.Equal(t, "BTCUSDT", filtered[0].Symbol)
}

func TestConvertAndFilterClosedKlines(t *testing.T) {
	now := time.Now().UTC()
	closedAt := now.Add(-2 * time.Hour)
	openAt := now.Add(-time.Hour)
	converted := convertExchangeKlines([]*exchange.Kline{{
		OpenTime: openAt, CloseTime: closedAt,
		Open: common.NewDecimal("1"), High: common.NewDecimal("2"), Low: common.NewDecimal("0.5"), Close: common.NewDecimal("1.5"),
		Volume: common.NewDecimal("10"), QuoteVolume: common.NewDecimal("15"), TradeCount: 3,
	}}, "BTCUSDT", "1h")
	require.Len(t, converted, 1)
	closed, skipped := filterClosedKlines(converted, now)
	assert.Equal(t, 0, skipped)
	assert.Len(t, closed, 1)
	assert.Equal(t, closedAt, closed[0].CloseTime)
}

func TestLatestCloseTime_FormatsRFC3339(t *testing.T) {
	ts := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	klines := []*market.Kline{{CloseTime: ts}}
	assert.Contains(t, latestCloseTime(klines), "2026-07-12")
}

func TestKlineCollector_SourceAndDataType(t *testing.T) {
	c := &KlineCollector{}
	assert.Equal(t, "binance", c.Source())
	assert.Equal(t, "kline", c.DataType())
}

func TestKlineCollectorLiveAlsoWritesOnlyClosedKlines(t *testing.T) {
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	watermark := now.Add(-2 * time.Hour)
	store := &fakeKlineStorage{
		latest: watermark, found: true,
	}
	collector := &KlineCollector{
		storage: store,
		fetchKlinePage: func(_ context.Context, _ *sources.CollectParams, _ *exchange.KlineRequest) ([]*exchange.Kline, error) {
			return []*exchange.Kline{
				{
					OpenTime: now.Add(-time.Minute), CloseTime: now.Add(-time.Second),
					Open: common.NewDecimal("1"), High: common.NewDecimal("2"), Low: common.NewDecimal("0.5"), Close: common.NewDecimal("1.5"),
					Volume: common.NewDecimal("10"), QuoteVolume: common.NewDecimal("15"), TradeCount: 3,
				},
				{
					OpenTime: now, CloseTime: now.Add(time.Minute),
					Open: common.NewDecimal("1.5"), High: common.NewDecimal("2"), Low: common.NewDecimal("1"), Close: common.NewDecimal("1.8"),
					Volume: common.NewDecimal("8"), QuoteVolume: common.NewDecimal("14"), TradeCount: 2,
				},
			}, nil
		},
		now: func() time.Time { return now },
	}

	err := collector.Collect(context.Background(), &sources.CollectParams{
		Live: true, InstType: InstTypeSPOT, SpaceID: "crypto", DatasetID: "kline",
		Symbol: "BTCUSDT", SubjectID: "BTC-USDT", Interval: "1m",
	})
	require.NoError(t, err)
	require.Len(t, store.writes, 1)
	require.Len(t, store.writes[0], 1)
	assert.Equal(t, formatKlineTime(now.Add(-time.Minute)), store.writes[0][0].GetKey().GetTimeSeries().GetDataTime())
}

func TestNormalizeFreq_ShouldNormalizeUnits(t *testing.T) {
	got, err := normalizeFreq("1h")
	require.NoError(t, err)
	assert.Equal(t, "1H", got)

	_, err = normalizeFreq("")
	assert.Error(t, err)

	got, err = normalizeFreq("5m")
	require.NoError(t, err)
	assert.Equal(t, "5m", got)
}

func TestIsKlineClosedAndFormatTime(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-2 * time.Hour)
	kline := &market.Kline{CloseTime: past}
	assert.True(t, isKlineClosed(kline, now))
	assert.False(t, isKlineClosed(&market.Kline{CloseTime: now.Add(time.Hour)}, now))
	assert.Contains(t, formatKlineTime(past), "T")
}

func TestBuildKlineRows_ShouldEmitClosedBars(t *testing.T) {
	closedAt := time.Now().UTC().Add(-3 * time.Hour)
	openAt := closedAt.Add(-time.Hour)
	klines := []*market.Kline{{
		OpenTime: openAt, CloseTime: closedAt,
		Open: common.NewDecimal("100"), High: common.NewDecimal("110"), Low: common.NewDecimal("90"),
		Close: common.NewDecimal("105"), Volume: common.NewDecimal("12"), QuoteVolume: common.NewDecimal("1200"),
		TradeCount: 7,
	}}
	rows, err := buildKlineRows(klines, "crypto", "kline-ds", "BTCUSDT", "1H")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "crypto", rows[0].GetKey().GetSpaceId())
	assert.Equal(t, "kline-ds", rows[0].GetKey().GetDatasetId())
	assert.Equal(t, "BTCUSDT", rows[0].GetKey().GetTimeSeries().GetSubjectId())
}

func TestSymbolCollector_SourceAndDataType(t *testing.T) {
	c := &SymbolCollector{}
	assert.Equal(t, "binance", c.Source())
	assert.Equal(t, "symbol", c.DataType())
}
