package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPageLimitAndOffset(t *testing.T) {
	assert.Equal(t, 50, Page{}.Limit())
	assert.Equal(t, 500, Page{PageSize: 1000}.Limit())
	assert.Equal(t, 0, Page{Page: 1}.offset())
	assert.Equal(t, 100, Page{Page: 3, PageSize: 50}.offset())
}
