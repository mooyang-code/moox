package resample

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFixedFrequencyCanonicalizesWholeMinutePeriods(t *testing.T) {
	tests := []struct {
		raw  string
		want FixedFrequency
	}{
		{raw: "1m", want: FixedFrequency{Storage: "1m", Slug: "1m", Duration: time.Minute}},
		{raw: "90m", want: FixedFrequency{Storage: "90m", Slug: "90m", Duration: 90 * time.Minute}},
		{raw: "240m", want: FixedFrequency{Storage: "4H", Slug: "4h", Duration: 4 * time.Hour}},
		{raw: "4H", want: FixedFrequency{Storage: "4H", Slug: "4h", Duration: 4 * time.Hour}},
		{raw: "24h", want: FixedFrequency{Storage: "1D", Slug: "1d", Duration: 24 * time.Hour}},
		{raw: "1440m", want: FixedFrequency{Storage: "1D", Slug: "1d", Duration: 24 * time.Hour}},
		{raw: "30d", want: FixedFrequency{Storage: "30D", Slug: "30d", Duration: 30 * 24 * time.Hour}},
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
	for _, raw := range []string{"", "0m", "-1m", "1.5h", "30s", "1M", "1w", "31d", "999999999999999999999m"} {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseFixedFrequency(raw)
			require.Error(t, err)
		})
	}
}

func TestValidateResamplePairRequiresAnIntegralBoundedExpansion(t *testing.T) {
	tests := []struct {
		name   string
		source string
		target string
		ok     bool
	}{
		{name: "one minute to four hours", source: "1m", target: "4H", ok: true},
		{name: "thirty to ninety minutes", source: "30m", target: "90m", ok: true},
		{name: "target not multiple", source: "1H", target: "90m"},
		{name: "same period", source: "1H", target: "1H"},
		{name: "target shorter", source: "4H", target: "1H"},
		{name: "too many source rows", source: "1m", target: "30D"},
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
	target, err := ParseFixedFrequency("4H")
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
	target, err := ParseFixedFrequency("7m")
	require.NoError(t, err)
	origin := time.Unix(0, 0).UTC()

	start, end := BucketAt(origin.Add(10*time.Minute), origin, target)
	assert.Equal(t, origin, start)
	assert.Equal(t, origin.Add(7*time.Minute), end)

	start, end = BucketAt(origin.Add(-time.Minute), origin, target)
	assert.Equal(t, origin.Add(-14*time.Minute), start)
	assert.Equal(t, origin.Add(-7*time.Minute), end)
}
