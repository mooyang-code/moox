package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseFrequencyAcceptsPositiveStorageFrequencies(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"1m": time.Minute,
		"5m": 5 * time.Minute,
		"1h": time.Hour,
		"1D": 24 * time.Hour,
		"2w": 14 * 24 * time.Hour,
		"1M": 30 * 24 * time.Hour,
		"1Y": 365 * 24 * time.Hour,
	} {
		got, err := ParseFrequency(raw)
		require.NoError(t, err, raw)
		require.Equal(t, want, got, raw)
	}
}

func TestParseFrequencyRejectsNonPositiveOrMalformedValues(t *testing.T) {
	for _, raw := range []string{"", "0s", "0m", "-1m", "1s", "m", "1", " 1m "} {
		_, err := ParseFrequency(raw)
		require.Error(t, err, raw)
	}
}
