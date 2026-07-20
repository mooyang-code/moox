//go:build legacy_storage

package bench

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKlineRowsToTimeSeriesRows_Basic_ShouldConvertAllFields(t *testing.T) {
	rows := []KlineRow{
		{
			Time:        time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			Open:        100.5,
			High:        101.0,
			Low:         99.5,
			Close:       100.8,
			Volume:      1000.0,
			QuoteVolume: 100800.0,
			TradeNum:    42,
			Symbol:      "BTC-USDT",
		},
	}
	out := KlineRowsToTimeSeriesRows("space1", "ds1", "subj1", "1m", rows)
	require.Len(t, out, 1)

	key := out[0].GetKey()
	assert.Equal(t, "space1", key.GetSpaceId())
	assert.Equal(t, "ds1", key.GetDatasetId())
	assert.Equal(t, "subj1", key.GetSubjectId())
	assert.Equal(t, "1m", key.GetFreq())
	assert.Equal(t, "2024-01-01T00:00:00Z", key.GetDataTime())

	// verify columns contain all expected names
	colNames := make(map[string]bool)
	for _, col := range out[0].GetColumns() {
		colNames[col.GetColumnName()] = true
	}
	for _, expected := range []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num", "symbol"} {
		assert.True(t, colNames[expected], "missing column %s", expected)
	}
}

func TestKlineRowsToTimeSeriesRows_WithFundingRate_ShouldIncludeColumn(t *testing.T) {
	fr := 0.0001
	rows := []KlineRow{
		{Time: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), FundingRate: &fr},
	}
	out := KlineRowsToTimeSeriesRows("s", "d", "sub", "1m", rows)
	require.Len(t, out, 1)

	found := false
	for _, col := range out[0].GetColumns() {
		if col.GetColumnName() == "fundingRate" {
			found = true
		}
	}
	assert.True(t, found, "fundingRate column should be present when non-nil")
}

func TestKlineRowsToTimeSeriesRows_EmptyInput_ShouldReturnEmptySlice(t *testing.T) {
	out := KlineRowsToTimeSeriesRows("s", "d", "sub", "1m", nil)
	assert.Empty(t, out)
}

func TestDoubleColumn_ShouldSetDoubleValue(t *testing.T) {
	col := doubleColumn("price", 42.5)
	assert.Equal(t, "price", col.GetColumnName())
	assert.Equal(t, 42.5, col.GetValue().GetDoubleValue())
}

func TestIntColumn_ShouldSetIntValue(t *testing.T) {
	col := intColumn("count", 99)
	assert.Equal(t, "count", col.GetColumnName())
	assert.Equal(t, int64(99), col.GetValue().GetIntValue())
}

func TestStringColumn_ShouldSetStringValue(t *testing.T) {
	col := stringColumn("symbol", "BTC-USDT")
	assert.Equal(t, "symbol", col.GetColumnName())
	assert.Equal(t, "BTC-USDT", col.GetValue().GetStringValue())
}
