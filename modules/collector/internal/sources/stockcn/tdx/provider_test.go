package tdx

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	tdxwire "github.com/mooyang-code/moox/packages/tdx"
	"github.com/stretchr/testify/require"
)

func TestCategoryForFrequencyCoversSupportedA股Bars(t *testing.T) {
	tests := map[string]tdxwire.KlineCategory{
		"1m":  tdxwire.Category1Min,
		"5m":  tdxwire.Category5Min,
		"15m": tdxwire.Category15Min,
		"30m": tdxwire.Category30Min,
		"60m": tdxwire.Category60Min,
		"1d":  tdxwire.CategoryDay,
		"1w":  tdxwire.CategoryWeek,
		"1M":  tdxwire.CategoryMonth,
	}
	for frequency, want := range tests {
		t.Run(frequency, func(t *testing.T) {
			got, err := categoryForFrequency(frequency)
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
	_, err := categoryForFrequency("2m")
	require.ErrorIs(t, err, marketdata.ErrUnsupportedFrequency)
}

func TestProviderDescriptorUsesNormalTDXSourceIdentity(t *testing.T) {
	provider := New(Config{Host: "quotes.example", Port: 7709})
	descriptor := provider.Descriptor()
	require.Equal(t, "tdx", descriptor.ID)
	require.Equal(t, "normal_7709", descriptor.SourceID)
	require.Equal(t, []string{"quotes.example"}, descriptor.Hosts)
}

func TestNormalizeBarsUsesA股LogicalStartAndSourceIdentity(t *testing.T) {
	fetchedAt := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	bars := []tdxwire.Bar{{
		Time:   time.Date(2026, 9, 1, 9, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)),
		Open:   10,
		High:   10.2,
		Low:    9.8,
		Close:  10.1,
		Volume: 1200,
		Amount: 12000,
	}}
	rows, err := normalizeBars("600000.XSHG", "sh600000", "1m", bars, fetchedAt, "request-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "tdx", rows[0].ProviderID)
	require.Equal(t, "normal_7709", rows[0].SourceID)
	require.Equal(t, time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC), rows[0].BarStart)
	require.Equal(t, rows[0].BarStart.Add(time.Minute), rows[0].BarEnd)
	require.Equal(t, float64(1200), rows[0].VolumeShares)
	require.Equal(t, float64(12000), rows[0].AmountCNY)
}
