package report

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecentDatasetTimesUsesStorageCalendarBoundaries(t *testing.T) {
	now := time.Date(2026, time.July, 29, 15, 47, 12, 0, time.UTC)
	tests := []struct {
		frequency string
		want      []time.Time
	}{
		{"1h", []time.Time{
			time.Date(2026, time.July, 29, 15, 0, 0, 0, time.UTC),
			time.Date(2026, time.July, 29, 14, 0, 0, 0, time.UTC),
		}},
		{"1w", []time.Time{
			time.Date(2026, time.July, 27, 0, 0, 0, 0, time.UTC),
			time.Date(2026, time.July, 20, 0, 0, 0, 0, time.UTC),
		}},
		{"1mo", []time.Time{
			time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
		}},
		{"4h", []time.Time{
			time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC),
			time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.frequency, func(t *testing.T) {
			got, err := RecentDatasetTimes(tt.frequency, now, 2)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
