package process

import (
	"os"
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

func TestSingleThreadBLASEnv_SkipsExplicitParentValues(t *testing.T) {
	for _, key := range singleThreadBLASKeys {
		t.Setenv(key, "")
		assert.NoError(t, os.Unsetenv(key))
	}
	assert.Equal(t, []string{"OPENBLAS_NUM_THREADS=1", "OMP_NUM_THREADS=1", "MKL_NUM_THREADS=1"}, SingleThreadBLASEnv())

	t.Setenv("OPENBLAS_NUM_THREADS", "2")
	t.Setenv("MKL_NUM_THREADS", "") // 显式设为空也算调用方指定，保留
	assert.Equal(t, []string{"OMP_NUM_THREADS=1"}, SingleThreadBLASEnv())
}
