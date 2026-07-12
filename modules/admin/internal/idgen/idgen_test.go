package idgen

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateID_DefaultLength_ShouldReturn11Chars(t *testing.T) {
	id := GenerateID(0)
	require.Len(t, id, 11)
	assert.Regexp(t, regexp.MustCompile(`^[a-z0-9]+$`), id)
}

func TestGenerateID_CustomLength_ShouldReturnRequestedLength(t *testing.T) {
	id := GenerateID(16)
	require.Len(t, id, 16)
	assert.Regexp(t, regexp.MustCompile(`^[a-z0-9]+$`), id)
}

func TestGenerateID_NegativeLength_ShouldUseDefaultLength(t *testing.T) {
	id := GenerateID(-1)
	require.Len(t, id, 11)
}

func TestGenerateID_MultipleCalls_ShouldReturnUniqueIDs(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id := GenerateID(11)
		_, exists := seen[id]
		assert.False(t, exists, "duplicate id %q", id)
		seen[id] = struct{}{}
	}
}
