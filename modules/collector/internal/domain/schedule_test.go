package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseScheduleIntervalAcceptsWholeMinuteDurations(t *testing.T) {
	tests := []struct {
		raw  string
		want time.Duration
	}{
		{raw: "1m", want: time.Minute},
		{raw: "90m", want: 90 * time.Minute},
		{raw: "24h", want: 24 * time.Hour},
		{raw: "1d", want: 24 * time.Hour},
		{raw: "2d", want: 48 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := ParseScheduleInterval(tt.raw)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseScheduleIntervalRejectsNonWholeMinuteDurations(t *testing.T) {
	tests := []struct {
		raw     string
		wantErr string
	}{
		{raw: "", wantErr: "positive"},
		{raw: "0m", wantErr: "positive"},
		{raw: "-1m", wantErr: "positive"},
		{raw: "30s", wantErr: "whole minutes"},
		{raw: "90s", wantErr: "whole minutes"},
		{raw: "1.5m", wantErr: "whole minutes"},
		{raw: "0d", wantErr: "positive"},
		{raw: "-1d", wantErr: "positive"},
		{raw: "1.5d", wantErr: "positive"},
		{raw: "day", wantErr: "positive"},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			_, err := ParseScheduleInterval(tt.raw)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestScheduleDecisionUsesNextUTCMinuteAsCandidate(t *testing.T) {
	now := time.Date(2026, time.July, 27, 10, 14, 42, 123, time.FixedZone("CST", 8*60*60))

	candidate, due, err := ScheduleDecision(now, "5m")

	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, time.July, 27, 2, 15, 0, 0, time.UTC), candidate)
	assert.True(t, due)
}

func TestScheduleDecisionIsNotDueWhenCandidateIsNotPeriodAligned(t *testing.T) {
	now := time.Date(2026, time.July, 27, 2, 15, 59, 0, time.UTC)

	candidate, due, err := ScheduleDecision(now, "5m")

	require.NoError(t, err)
	assert.True(t, candidate.IsZero())
	assert.False(t, due)
}

func TestScheduleDecisionAlignsMinuteHourFourHourAndDayPeriods(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		raw  string
		want time.Time
	}{
		{
			name: "minute",
			now:  time.Date(2026, time.July, 27, 2, 15, 42, 0, time.UTC),
			raw:  "1m",
			want: time.Date(2026, time.July, 27, 2, 16, 0, 0, time.UTC),
		},
		{
			name: "hour",
			now:  time.Date(2026, time.July, 27, 2, 59, 42, 0, time.UTC),
			raw:  "1h",
			want: time.Date(2026, time.July, 27, 3, 0, 0, 0, time.UTC),
		},
		{
			name: "four hours",
			now:  time.Date(2026, time.July, 27, 3, 59, 42, 0, time.UTC),
			raw:  "4h",
			want: time.Date(2026, time.July, 27, 4, 0, 0, 0, time.UTC),
		},
		{
			name: "day",
			now:  time.Date(2026, time.July, 27, 23, 59, 42, 0, time.UTC),
			raw:  "1d",
			want: time.Date(2026, time.July, 28, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate, due, err := ScheduleDecision(tt.now, tt.raw)
			require.NoError(t, err)
			assert.True(t, due)
			assert.Equal(t, tt.want, candidate)
		})
	}
}

func TestScheduleDecisionRejectsInvalidInterval(t *testing.T) {
	candidate, due, err := ScheduleDecision(time.Now(), "30s")

	require.Error(t, err)
	assert.True(t, candidate.IsZero())
	assert.False(t, due)
}
