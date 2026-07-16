package metrics

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestBoundedPage(t *testing.T) {
	offset, limit := boundedPage(-1, 0)
	assert.Equal(t, 0, offset)
	assert.Equal(t, 50, limit)
	offset, limit = boundedPage(10, 1000)
	assert.Equal(t, 10, offset)
	assert.Equal(t, 500, limit)
}

func TestCanonicalJSON(t *testing.T) {
	assert.Equal(t, `{"a":1}`, canonicalJSON(`{"a":1}`))
	assert.Equal(t, "not-json", canonicalJSON("not-json"))
}

func TestMetricCatalogNoDataAfter(t *testing.T) {
	c := NewCatalog(nil)
	assert.Equal(t, 2*time.Minute, c.NoDataAfter())
	c.SetNoDataAfter(5 * time.Minute)
	assert.Equal(t, 5*time.Minute, c.NoDataAfter())
}

func TestMetricCatalogTimeScansRFC3339(t *testing.T) {
	var got metricCatalogTime
	require.NoError(t, got.Scan("2026-07-15T04:17:48.123456789Z"))
	assert.Equal(t, "2026-07-15T04:17:48.123456789Z", got.UTC().Format(time.RFC3339Nano))
}
