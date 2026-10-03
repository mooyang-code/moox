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
	} {
		got, err := ParseFrequency(value)
		require.NoError(t, err, value)
		require.Equal(t, want, got, value)
	}
}

func TestParseFrequencyRejectsUnsupportedPeriods(t *testing.T) {
	for _, value := range []string{"", "0m", "2h", "1M", "1w", "1s", " 1m "} {
		_, err := ParseFrequency(value)
		require.Error(t, err, value)
	}
}
