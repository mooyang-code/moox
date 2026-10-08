package resample

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFixedFrequencyReturnsCanonicalFrequencies(t *testing.T) {
	tests := []struct {
		raw  string
		want FixedFrequency
	}{
		{raw: "1m", want: FixedFrequency{Storage: "1m", Duration: time.Minute}},
		{raw: "15m", want: FixedFrequency{Storage: "15m", Duration: 15 * time.Minute}},
		{raw: "240m", want: FixedFrequency{Storage: "4h", Duration: 4 * time.Hour}},
		{raw: "4H", want: FixedFrequency{Storage: "4h", Duration: 4 * time.Hour}},
		{raw: "1d", want: FixedFrequency{Storage: "1d", Duration: 24 * time.Hour}},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := ParseFixedFrequency(tt.raw)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseFixedFrequencyRejectsNonFixedOrOutOfRangePeriods(t *testing.T) {
	for _, raw := range []string{"", "0m", "-1m", "1.5h", "30s", "90m", "24h", "1M", "1mo", "1w", "30d", "999999999999999999999m"} {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseFixedFrequency(raw)
			require.Error(t, err)
		})
	}
}

func TestValidateResamplePairRequiresALargerTarget(t *testing.T) {
	tests := []struct {
		name   string
		source string
		target string
		ok     bool
	}{
		{name: "one minute to four hours", source: "1m", target: "4h", ok: true},
		{name: "one minute to one day", source: "1m", target: "1d", ok: true},
		{name: "same period", source: "1h", target: "1h"},
		{name: "target shorter", source: "4h", target: "1h"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, err := ParseFixedFrequency(tt.source)
			require.NoError(t, err)
			target, err := ParseFixedFrequency(tt.target)
			require.NoError(t, err)
			err = ValidateResamplePair(source, target)
			if tt.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
		})
	}
}

func TestBucketAtUsesTheLatestClosedEpochAlignedBucket(t *testing.T) {
	target, err := ParseFixedFrequency("4h")
	require.NoError(t, err)
	origin := time.Unix(0, 0).UTC()

	start, end := BucketAt(time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC), origin, target)
	assert.Equal(t, time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC), start)
	assert.Equal(t, time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC), end)

	start, end = BucketAt(time.Date(2026, 8, 29, 7, 59, 59, 0, time.UTC), origin, target)
	assert.Equal(t, time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC), start)
	assert.Equal(t, time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC), end)
}

func TestBucketAtUsesDurationGridInsteadOfWallClockModulo(t *testing.T) {
	target, err := ParseFixedFrequency("15m")
	require.NoError(t, err)
	origin := time.Unix(0, 0).UTC().Add(5 * time.Minute)

	start, end := BucketAt(origin.Add(40*time.Minute), origin, target)
	assert.Equal(t, origin.Add(15*time.Minute), start)
	assert.Equal(t, origin.Add(30*time.Minute), end)

	start, end = BucketAt(origin.Add(-time.Minute), origin, target)
	assert.Equal(t, origin.Add(-30*time.Minute), start)
	assert.Equal(t, origin.Add(-15*time.Minute), end)
}
