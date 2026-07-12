package snapshot

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHandleAndMappedNilGuards(t *testing.T) {
	var h *Handle
	assert.NoError(t, h.Release())

	var m *Mapped
	assert.Nil(t, m.Reader())
	assert.Nil(t, m.Schema())
	assert.NoError(t, m.Close())
}
