package resample

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpectedSourceTimesEnumeratesHalfOpenWindow(t *testing.T) {
	source := mustFrequency(t, "30m")
	start := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)

	got, err := ExpectedSourceTimes(start, start.Add(90*time.Minute), source)

	require.NoError(t, err)
	assert.Equal(t, []time.Time{start, start.Add(30 * time.Minute), start.Add(60 * time.Minute)}, got)
}

func TestBarsAggregatesCompleteSourceWindowAndSortsRows(t *testing.T) {
	start := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	spec := testRuleSpec(t, "1m", "4m")
	rows := []SourceBar{
		testSourceBar(start.Add(2*time.Minute), 110, 112, 95, 98, 3, 300, 4),
		testSourceBar(start, 100, 110, 90, 105, 1, 100, 2),
		testSourceBar(start.Add(3*time.Minute), 98, 120, 97, 119, 4, 400, 5),
		testSourceBar(start.Add(time.Minute), 105, 115, 100, 110, 2, 210, 3),
	}

	result, err := Bars(spec, "BTC-USDT", start, start.Add(4*time.Minute), rows)

	require.NoError(t, err)
	assert.Equal(t, "crypto", result.SpaceID)
	assert.Equal(t, "dataset_spot_kline_derived_4m", result.DatasetID)
	assert.Equal(t, "BTC-USDT", result.SubjectID)
	assert.Equal(t, "4m", result.Frequency)
	assert.Equal(t, "venue:binance", result.SeriesTag)
	assert.Equal(t, start, result.DataTime)
	assert.Equal(t, start.Add(4*time.Minute), result.SourceWindowEnd)
	assert.Equal(t, 100.0, result.Open)
	assert.Equal(t, 120.0, result.High)
	assert.Equal(t, 90.0, result.Low)
	assert.Equal(t, 119.0, result.Close)
	assert.Equal(t, 10.0, result.Volume)
	assert.Equal(t, 1010.0, result.QuoteVolume)
	assert.Equal(t, int64(14), result.TradeNum)
	assert.Len(t, result.SourceHash, 64)
}

func TestBarsAggregatesThirtyMinuteBarsIntoNinetyMinutes(t *testing.T) {
	start := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	spec := testRuleSpec(t, "30m", "90m")
	rows := []SourceBar{
		testSourceBar(start, 10, 12, 9, 11, 1, 10, 2),
		testSourceBar(start.Add(30*time.Minute), 11, 15, 10, 14, 2, 20, 3),
		testSourceBar(start.Add(60*time.Minute), 14, 16, 8, 9, 3, 30, 4),
	}
	for index := range rows {
		rows[index].Frequency = "30m"
	}

	result, err := Bars(spec, "BTC-USDT", start, start.Add(90*time.Minute), rows)

	require.NoError(t, err)
	assert.Equal(t, "90m", result.Frequency)
	assert.Equal(t, 10.0, result.Open)
	assert.Equal(t, 16.0, result.High)
	assert.Equal(t, 8.0, result.Low)
	assert.Equal(t, 9.0, result.Close)
	assert.Equal(t, 6.0, result.Volume)
	assert.Equal(t, int64(9), result.TradeNum)
}

func TestBarsSourceHashIsOrderIndependentAndChangesWithSourceValue(t *testing.T) {
	start := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	spec := testRuleSpec(t, "1m", "2m")
	first := testSourceBar(start, 100, 110, 90, 105, 1, 100, 2)
	second := testSourceBar(start.Add(time.Minute), 105, 115, 100, 110, 2, 210, 3)

	ordered, err := Bars(spec, "BTC-USDT", start, start.Add(2*time.Minute), []SourceBar{first, second})
	require.NoError(t, err)
	reversed, err := Bars(spec, "BTC-USDT", start, start.Add(2*time.Minute), []SourceBar{second, first})
	require.NoError(t, err)
	assert.Equal(t, ordered.SourceHash, reversed.SourceHash)

	revisedSecond := second
	revisedSecond.Close = floatPtr(111)
	revised, err := Bars(spec, "BTC-USDT", start, start.Add(2*time.Minute), []SourceBar{first, revisedSecond})
	require.NoError(t, err)
	assert.NotEqual(t, ordered.SourceHash, revised.SourceHash)
}

func TestBarsRejectsIncompleteOrMismatchedSourceKeys(t *testing.T) {
	start := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	spec := testRuleSpec(t, "1m", "3m")
	valid := []SourceBar{
		testSourceBar(start, 1, 2, 0.5, 1.5, 1, 1, 1),
		testSourceBar(start.Add(time.Minute), 1.5, 2, 1, 1.8, 1, 1, 1),
		testSourceBar(start.Add(2*time.Minute), 1.8, 2.1, 1.7, 2, 1, 1, 1),
	}

	tests := []struct {
		name string
		rows []SourceBar
	}{
		{name: "missing first", rows: valid[1:]},
		{name: "missing middle", rows: []SourceBar{valid[0], valid[2]}},
		{name: "missing last", rows: valid[:2]},
		{name: "duplicate time", rows: []SourceBar{valid[0], valid[1], valid[1]}},
		{name: "wrong subject", rows: replaceBar(valid, 1, func(row *SourceBar) { row.SubjectID = "ETH-USDT" })},
		{name: "wrong dataset", rows: replaceBar(valid, 1, func(row *SourceBar) { row.DatasetID = "other" })},
		{name: "wrong frequency", rows: replaceBar(valid, 1, func(row *SourceBar) { row.Frequency = "5m" })},
		{name: "wrong series tag", rows: replaceBar(valid, 1, func(row *SourceBar) { row.SeriesTag = "venue:okx" })},
		{name: "timestamp not on minute", rows: replaceBar(valid, 1, func(row *SourceBar) { row.DataTime = row.DataTime.Add(time.Second) })},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Bars(spec, "BTC-USDT", start, start.Add(3*time.Minute), tt.rows)
			require.Error(t, err)
		})
	}
}

func TestBarsRejectsMissingInvalidOrOverflowingFields(t *testing.T) {
	start := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	spec := testRuleSpec(t, "1m", "2m")
	valid := []SourceBar{
		testSourceBar(start, 1, 2, 0.5, 1.5, 1, 1, 1),
		testSourceBar(start.Add(time.Minute), 1.5, 2, 1, 1.8, 1, 1, 1),
	}

	tests := []struct {
		name string
		rows []SourceBar
	}{
		{name: "missing open", rows: replaceBar(valid, 0, func(row *SourceBar) { row.Open = nil })},
		{name: "missing trade num", rows: replaceBar(valid, 0, func(row *SourceBar) { row.TradeNum = nil })},
		{name: "not finite", rows: replaceBar(valid, 0, func(row *SourceBar) { row.High = floatPtr(math.Inf(1)) })},
		{name: "negative volume", rows: replaceBar(valid, 0, func(row *SourceBar) { row.Volume = floatPtr(-1) })},
		{name: "negative quote volume", rows: replaceBar(valid, 0, func(row *SourceBar) { row.QuoteVolume = floatPtr(-1) })},
		{name: "negative trade num", rows: replaceBar(valid, 0, func(row *SourceBar) { row.TradeNum = int64Ptr(-1) })},
		{name: "trade num overflow", rows: replaceBar(valid, 0, func(row *SourceBar) { row.TradeNum = int64Ptr(math.MaxInt64) })},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Bars(spec, "BTC-USDT", start, start.Add(2*time.Minute), tt.rows)
			require.Error(t, err)
		})
	}
}

func TestBarsRejectsWindowThatIsNotOneAlignedTargetBucket(t *testing.T) {
	start := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	spec := testRuleSpec(t, "1m", "4m")
	rows := []SourceBar{
		testSourceBar(start, 1, 2, 0.5, 1.5, 1, 1, 1),
		testSourceBar(start.Add(time.Minute), 1.5, 2, 1, 1.8, 1, 1, 1),
		testSourceBar(start.Add(2*time.Minute), 1.8, 2, 1, 1.9, 1, 1, 1),
		testSourceBar(start.Add(3*time.Minute), 1.9, 2.1, 1.8, 2, 1, 1, 1),
	}

	_, err := Bars(spec, "BTC-USDT", start.Add(time.Minute), start.Add(5*time.Minute), rows)
	require.Error(t, err)
	_, err = Bars(spec, "BTC-USDT", start, start.Add(3*time.Minute), rows[:3])
	require.Error(t, err)
}

func testRuleSpec(t *testing.T, sourceRaw, targetRaw string) RuleSpec {
	t.Helper()
	source := mustFrequency(t, sourceRaw)
	target := mustFrequency(t, targetRaw)
	return RuleSpec{
		RuleID:          "rule-1",
		SpaceID:         "crypto",
		SourceDatasetID: "dataset_binance_spot_kline_1m",
		SourceFrequency: source,
		SourceSeriesTag: "venue:binance",
		TargetDatasetID: "dataset_spot_kline_derived_" + target.Slug,
		TargetFrequency: target,
		Alignment:       AlignmentEpochUTC,
	}
}

func testSourceBar(at time.Time, open, high, low, close, volume, quoteVolume float64, tradeNum int64) SourceBar {
	return SourceBar{
		SpaceID:     "crypto",
		DatasetID:   "dataset_binance_spot_kline_1m",
		SubjectID:   "BTC-USDT",
		Frequency:   "1m",
		DataTime:    at,
		SeriesTag:   "venue:binance",
		Open:        floatPtr(open),
		High:        floatPtr(high),
		Low:         floatPtr(low),
		Close:       floatPtr(close),
		Volume:      floatPtr(volume),
		QuoteVolume: floatPtr(quoteVolume),
		TradeNum:    int64Ptr(tradeNum),
	}
}

func replaceBar(rows []SourceBar, index int, replace func(*SourceBar)) []SourceBar {
	cloned := append([]SourceBar(nil), rows...)
	replace(&cloned[index])
	return cloned
}

func mustFrequency(t *testing.T, raw string) FixedFrequency {
	t.Helper()
	frequency, err := ParseFixedFrequency(raw)
	require.NoError(t, err)
	return frequency
}

func floatPtr(value float64) *float64 { return &value }

func int64Ptr(value int64) *int64 { return &value }
