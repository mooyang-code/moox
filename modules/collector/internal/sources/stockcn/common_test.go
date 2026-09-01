package stockcn

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestCanonicalSubjectAndProviderSymbolCodec(t *testing.T) {
	tests := []struct {
		code           string
		subjectID      string
		providerSymbol string
	}{
		{code: "600000", subjectID: "600000.XSHG", providerSymbol: "sh600000"},
		{code: "000001", subjectID: "000001.XSHE", providerSymbol: "sz000001"},
		{code: "920000", subjectID: "920000.XBSE", providerSymbol: "bj920000"},
	}
	for _, tt := range tests {
		subjectID, err := CanonicalSubjectID(tt.code)
		require.NoError(t, err)
		require.Equal(t, tt.subjectID, subjectID)

		symbol, err := ProviderSymbol(subjectID)
		require.NoError(t, err)
		require.Equal(t, tt.providerSymbol, symbol)

		decoded, err := DecodeProviderSymbol(symbol)
		require.NoError(t, err)
		require.Equal(t, subjectID, decoded)
	}
}

func TestNormalizeMinuteKlineConvertsCloseLabeledMinute(t *testing.T) {
	fetchedAt := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	row, err := NormalizeMinuteKline("600000.XSHG", "tencent", "sh600000", marketdata.TimestampModeClose, "202608290931", 10, 11, 9, 10.5, 2, 2100, 100, fetchedAt, "req-1")
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 8, 29, 1, 30, 0, 0, time.UTC), row.BarStart)
	require.Equal(t, time.Date(2026, 8, 29, 1, 31, 0, 0, time.UTC), row.BarEnd)
	require.Equal(t, 200.0, row.VolumeShares)
}
