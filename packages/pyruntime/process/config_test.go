package process

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
	"github.com/stretchr/testify/assert"
)

func TestConfigDefaults_FillsMissingValues(t *testing.T) {
	cfg := &Config{}
	cfg.defaults()
	assert.Equal(t, "python3", cfg.PythonBin)
	assert.Equal(t, 30*time.Second, cfg.TaskTimeout)
	assert.Equal(t, 64<<10, cfg.MaxLogBytes)
	assert.Equal(t, protocol.DefaultLimits(), cfg.Limits)
}

func TestDefaultLimits_MatchesProtocol(t *testing.T) {
	assert.Equal(t, protocol.DefaultLimits(), DefaultLimits())
}
