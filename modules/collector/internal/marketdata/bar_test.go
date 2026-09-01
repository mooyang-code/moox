package marketdata

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBarDefinitionNormalizesStartAndEndLabels(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	start := time.Date(2026, 8, 31, 9, 30, 0, 0, location)

	definition := BarDefinition{Frequency: "1d", Location: location, TimestampMode: TimestampStartLabel}
	gotStart, gotEnd, err := definition.NormalizeLabel(start)
	require.NoError(t, err)
	require.Equal(t, start.UTC(), gotStart)
	require.Equal(t, time.Date(2026, 9, 1, 9, 30, 0, 0, location).UTC(), gotEnd)

	endLabel := time.Date(2026, 9, 1, 9, 30, 0, 0, location)
	definition.TimestampMode = TimestampEndLabel
	gotStart, gotEnd, err = definition.NormalizeLabel(endLabel)
	require.NoError(t, err)
	require.Equal(t, start.UTC(), gotStart)
	require.Equal(t, endLabel.UTC(), gotEnd)
}

func TestBarDefinitionUsesCalendarMonthBoundaries(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	definition := BarDefinition{Frequency: "1M", Location: location, TimestampMode: TimestampStartLabel}
	start := time.Date(2026, 1, 31, 0, 0, 0, 0, location)

	_, end, err := definition.NormalizeLabel(start)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 2, 28, 0, 0, 0, 0, location).UTC(), end)
}

func TestBarDefinitionRejectsUnknownSemantics(t *testing.T) {
	definition := BarDefinition{Frequency: "unknown", Location: time.UTC, TimestampMode: TimestampStartLabel}
	require.Error(t, definition.Validate())

	definition = BarDefinition{Frequency: "1m", Location: time.UTC, TimestampMode: "provider_guess"}
	require.Error(t, definition.Validate())
}
