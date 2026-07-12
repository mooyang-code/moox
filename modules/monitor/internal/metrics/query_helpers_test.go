package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTime(t *testing.T) {
	ts, err := ParseTime("")
	require.NoError(t, err)
	assert.True(t, ts.IsZero())
	raw := "2026-01-02T03:04:05.123456789Z"
	ts, err = ParseTime(raw)
	require.NoError(t, err)
	assert.Equal(t, raw, ts.UTC().Format(time.RFC3339Nano))
	_, err = ParseTime("not-a-time")
	require.Error(t, err)
}
