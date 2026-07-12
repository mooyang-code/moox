package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalLabelsJSON(t *testing.T) {
	raw, err := CanonicalLabelsJSON(map[string]string{"b": "2", "a": "1"})
	require.NoError(t, err)
	assert.Equal(t, `{"a":"1","b":"2"}`, raw)
	raw, err = CanonicalLabelsJSON(nil)
	require.NoError(t, err)
	assert.Equal(t, "{}", raw)
}

func TestSeriesIDIsStableAndOrderIndependent(t *testing.T) {
	a := SeriesID("svc", "inst", "cpu", map[string]string{"env": "prod", "zone": "a"})
	b := SeriesID("svc", "inst", "cpu", map[string]string{"zone": "a", "env": "prod"})
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, SeriesID("svc", "inst", "cpu", map[string]string{"env": "dev", "zone": "a"}))
	assert.Len(t, a, 64)
}

func TestFmtHex(t *testing.T) {
	assert.Equal(t, "00ff", fmtHex([]byte{0x00, 0xff}))
}
