package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizePage(t *testing.T) {
	page, size := normalizePage(Page{})
	assert.Equal(t, 1, page)
	assert.Equal(t, defaultPageSize, size)
	page, size = normalizePage(Page{Page: 2, PageSize: 5000})
	assert.Equal(t, 2, page)
	assert.Equal(t, maxPageSize, size)
}
