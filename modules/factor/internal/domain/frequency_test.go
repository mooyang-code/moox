package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseFrequencySupportsCryptoPeriods(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"1m": time.Minute, "5m": 5 * time.Minute, "15m": 15 * time.Minute,
		"30m": 30 * time.Minute, "1h": time.Hour, "4h": 4 * time.Hour, "1d": 24 * time.Hour,
		"45m": 45 * time.Minute, "2h": 2 * time.Hour, "12h": 12 * time.Hour,
	} {
		got, err := ParseFrequency(value)
		require.NoError(t, err, value)
		require.Equal(t, want, got, value)
	}
}

func TestParseFrequencyRejectsUnsupportedPeriods(t *testing.T) {
	for _, value := range []string{"", "0m", "7m", "5h", "60m", "1H", "1M", "1w", "30s", "1s", " 1m "} {
		_, err := ParseFrequency(value)
		require.Error(t, err, value)
	}
}
