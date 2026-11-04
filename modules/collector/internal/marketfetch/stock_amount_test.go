package marketfetch

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestStockKlineRowWritesEstimatedAmountWithQualityMark(t *testing.T) {
	start := time.Date(2026, 10, 12, 1, 30, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{
		SubjectID: "600000", ProviderID: "tencent", ProviderSymbol: "sh600000", Frequency: "1m",
		BarStart: start, BarEnd: start.Add(time.Minute), Open: 10, High: 11, Low: 9, Close: 10.5,
		VolumeShares: 1000, AmountCNY: 10500, AmountEstimated: true,
		ProviderTimestamp: start, FetchedAt: start.Add(time.Minute), RequestID: "req",
	}
	fields := func(bar marketdata.NormalizedKline) map[string]*storagepb.FieldValue {
		row, err := stockKlineRow(bar, testStockDatasetID, "route", 1, "name")
		require.NoError(t, err)
		out := map[string]*storagepb.FieldValue{}
		for _, field := range row.GetFields() {
			out[field.GetFieldId()] = field
		}
		return out
	}
	estimated := fields(bar)
	require.Equal(t, 10500.0, estimated["amount"].GetValue().GetDoubleValue(), "估算的成交额照常写入")
	require.Equal(t, "estimated_close_x_volume", estimated["amount_quality"].GetValue().GetStringValue())
	bar.AmountEstimated = false
	reported := fields(bar)
	require.Equal(t, 10500.0, reported["amount"].GetValue().GetDoubleValue())
	require.Equal(t, "reported", reported["amount_quality"].GetValue().GetStringValue())
}
