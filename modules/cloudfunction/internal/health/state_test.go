package health

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStateInitializesModuleInfo(t *testing.T) {
	state := New("cloudnode", "instance-1", "v1.0.0", "abc123")
	require.NotNil(t, state)
	assert.Equal(t, "cloudnode", state.Module)
	assert.Equal(t, "instance-1", state.InstanceID)
}
