package metricspublish

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfigEnvOverrides(t *testing.T) {
	t.Setenv("MOOX_INSTANCE_ID", "mon-inst")
	t.Setenv("MOOX_NODE_ID", "mon-node")
	t.Setenv("MOOX_METRICS_EVENTBUS_URL", "nats://monitor:4222")
	t.Setenv("MOOX_METRICS_MAX_FAMILIES", "99")
	t.Setenv("MOOX_METRICS_INCLUDE_REGEX", "^moox_.*$")

	cfg := DefaultConfig("monitor")
	assert.Equal(t, "mon-inst", cfg.InstanceID)
	assert.Equal(t, "mon-node", cfg.NodeID)
	assert.Equal(t, "nats://monitor:4222", cfg.EventBusURL)
	assert.Equal(t, 99, cfg.MaxMetricFamilies)
	assert.Equal(t, "^moox_.*$", cfg.IncludeRegex)

	with := Config{ServiceName: "monitor"}.withDefaults()
	assert.Equal(t, DefaultTopic, with.Topic)
	assert.Equal(t, 30*time.Second, with.Interval)

	assert.Equal(t, "", firstEnv())
	assert.Equal(t, 3, envInt("MOOX_MISSING_ENV_INT", 3))
	t.Setenv("MOOX_BAD_INT", "x")
	assert.Equal(t, 5, envInt("MOOX_BAD_INT", 5))

	_, err := NewHandler(Config{})
	require.Error(t, err)
}
