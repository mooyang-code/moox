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
		{raw: "1w", want: 7 * 24 * time.Hour},
		{raw: "1M", want: 31 * 24 * time.Hour},
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
