package marketfetch

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestStockKlineRowWritesNullForEstimatedAmount(t *testing.T) {
	start := time.Date(2026, 10, 12, 1, 30, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{
		SubjectID: "600000", ProviderID: "tencent", ProviderSymbol: "sh600000", Frequency: "1m",
		BarStart: start, BarEnd: start.Add(time.Minute), Open: 10, High: 11, Low: 9, Close: 10.5,
		VolumeShares: 1000, AmountCNY: 10500, AmountEstimated: true,
		ProviderTimestamp: start, FetchedAt: start.Add(time.Minute), RequestID: "req",
	}
	amount := func(bar marketdata.NormalizedKline) *storagepb.FieldValue {
		row, err := stockKlineRow(bar, "", "route", 1, "name")
		require.NoError(t, err)
		for _, field := range row.GetFields() {
			if field.GetFieldId() == "amount" {
				return field
			}
		}
		return nil
	}
	estimated := amount(bar)
	require.NotNil(t, estimated)
	_, isNull := estimated.GetValue().GetValue().(*storagepb.TypedValue_NullValue)
	require.True(t, isNull, "估算的成交额必须写显式 NULL")
	bar.AmountEstimated = false
	require.Equal(t, 10500.0, amount(bar).GetValue().GetDoubleValue())
}
