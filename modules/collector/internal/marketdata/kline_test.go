package marketdata

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	"github.com/stretchr/testify/require"
)

func validKline() NormalizedKline {
	start := time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC)
	return NormalizedKline{
		SubjectID: "000001.SZ", ProviderID: "tdx", SourceID: "normal_7709",
		ProviderSymbol: "000001", Frequency: "1m", BarStart: start,
		BarEnd: start.Add(time.Minute), Open: common.NewDecimal("10"),
		High: common.NewDecimal("11"), Low: common.NewDecimal("9"),
		Close: common.NewDecimal("10.5"), Volume: common.NewDecimal("100"),
		VolumeUnit: "share", FetchedAt: start.Add(time.Minute),
	}
}

func TestNormalizedKlineRejectsInvalidOHLC(t *testing.T) {
	kline := validKline()
	kline.High = common.NewDecimal("8")
	require.EqualError(t, kline.Validate(), "high cannot be below low")
}

func TestNormalizedKlinePreservesExplicitNullAmount(t *testing.T) {
	kline := validKline()
	kline.Amount = OptionalDecimal{Null: true, Valid: true}
	require.NoError(t, kline.Validate())
}

func TestKlineRequestRejectsReversedRange(t *testing.T) {
	request := KlineRequest{
		SubjectID: "000001.SZ", ProviderSymbol: "000001", Frequency: "1m",
		StartTime: time.Unix(20, 0), EndTime: time.Unix(10, 0),
	}
	require.Error(t, request.Validate())
}
