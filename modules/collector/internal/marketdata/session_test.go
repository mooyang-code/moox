package marketdata

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
	"github.com/stretchr/testify/require"
)

func TestSessionSpecExcludesAuctionFromContinuousBuckets(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	date := marketcalendar.MustParseCivilDate("2026-08-31")
	spec := SessionSpec{
		Location: location,
		Segments: []SessionSegment{
			{Open: 9*time.Hour + 15*time.Minute, Close: 9*time.Hour + 30*time.Minute, Kind: SessionAuction},
			{Open: 9*time.Hour + 30*time.Minute, Close: 11*time.Hour + 30*time.Minute, Kind: SessionRegular},
		},
	}

	buckets, err := spec.ExpectedMinuteBuckets(date)
	require.NoError(t, err)
	require.Len(t, buckets, 120)
	require.Equal(t, time.Date(2026, 8, 31, 9, 30, 0, 0, location), buckets[0])
	require.Equal(t, time.Date(2026, 8, 31, 11, 29, 0, 0, location), buckets[len(buckets)-1])
}

func TestSessionSpecRejectsOverlappingSegments(t *testing.T) {
	spec := SessionSpec{
		Location: time.UTC,
		Segments: []SessionSegment{
			{Open: time.Hour, Close: 3 * time.Hour},
			{Open: 2 * time.Hour, Close: 4 * time.Hour},
		},
	}
	require.Error(t, spec.Validate())
}
