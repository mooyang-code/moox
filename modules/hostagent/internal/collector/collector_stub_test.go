//go:build !linux

package collector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectorStub_NewAndCollect_ShouldReturnLinuxOnlyError(t *testing.T) {
	c := New()
	require.NotNil(t, c)

	snapshot, statuses, err := c.Collect(context.Background())
	assert.Nil(t, snapshot)
	assert.Nil(t, statuses)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "linux only")
}
