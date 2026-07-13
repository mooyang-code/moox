package metrics

import (
	"github.com/stretchr/testify/assert"
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
