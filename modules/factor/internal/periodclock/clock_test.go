package periodclock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestContinuousWindow1m(t *testing.T) {
	clock := Continuous{}
	at := time.Date(2026, time.October, 4, 0, 10, 0, 0, time.UTC)

	got, err := clock.Window(at, "1m", 3)

	require.NoError(t, err)
	require.Equal(t, []time.Time{
		time.Date(2026, time.October, 4, 0, 8, 0, 0, time.UTC),
		time.Date(2026, time.October, 4, 0, 9, 0, 0, time.UTC),
		at,
	}, got)
}

func TestContinuousWindowCrossesDay(t *testing.T) {
	clock := Continuous{}
	at := time.Date(2026, time.October, 4, 0, 0, 0, 0, time.UTC)

	got, err := clock.Window(at, "1h", 3)

	require.NoError(t, err)
	require.Equal(t, []time.Time{
		time.Date(2026, time.October, 3, 22, 0, 0, 0, time.UTC),
		time.Date(2026, time.October, 3, 23, 0, 0, 0, time.UTC),
		at,
	}, got)
}

func TestAlignRejectsUnalignedTime(t *testing.T) {
	clock := Continuous{}
	unaligned := time.Date(2026, time.October, 4, 0, 10, 30, 0, time.UTC)

	_, err := clock.Align(unaligned, "1m")

	require.Error(t, err)
}

func TestForSpaceStockCNUnsupported(t *testing.T) {
	_, err := ForSpace("stock_cn")

	require.ErrorIs(t, err, ErrUnsupportedCalendar)
}

func TestDurationSupportedFrequencies(t *testing.T) {
	clock := Continuous{}
	for frequency, want := range map[string]time.Duration{
		"1m":  time.Minute,
		"5m":  5 * time.Minute,
		"15m": 15 * time.Minute,
		"30m": 30 * time.Minute,
		"1h":  time.Hour,
		"4h":  4 * time.Hour,
		"1d":  24 * time.Hour,
	} {
		got, err := clock.Duration(frequency)
		require.NoError(t, err, frequency)
		require.Equal(t, want, got, frequency)
	}

	_, err := clock.Duration("2h")
	require.Error(t, err)
}

func TestForSpaceCryptoUsesContinuousCalendar(t *testing.T) {
	clock, err := ForSpace("crypto")

	require.NoError(t, err)
	require.NotNil(t, clock)
	_, err = ForSpace("stock_cn")
	require.ErrorIs(t, err, ErrUnsupportedCalendar)
}
