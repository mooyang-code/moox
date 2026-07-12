package metricspublish

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfigAndWithDefaults(t *testing.T) {
	t.Setenv("MOOX_INSTANCE_ID", "inst-1")
	t.Setenv("MOOX_NODE_ID", "node-1")
	t.Setenv("MOOX_METRICS_EVENTBUS_URL", "nats://example:4222")
	t.Setenv("MOOX_METRICS_MAX_SAMPLES", "123")
	t.Setenv("MOOX_METRICS_GZIP_LEVEL", "bad")

	cfg := DefaultConfig("eventbus")
	assert.Equal(t, "eventbus", cfg.ServiceName)
	assert.Equal(t, "inst-1", cfg.InstanceID)
	assert.Equal(t, "node-1", cfg.NodeID)
	assert.Equal(t, "nats://example:4222", cfg.EventBusURL)
	assert.Equal(t, 123, cfg.MaxSamples)
	assert.Equal(t, 1, cfg.GzipLevel) // invalid env falls back

	empty := Config{}.withDefaults()
	assert.Equal(t, DefaultTopic, empty.Topic)
	assert.Equal(t, DefaultSpace, empty.SpaceID)
	assert.Equal(t, 30*time.Second, empty.Interval)
	assert.NotEmpty(t, empty.IncludeRegex)
}

func TestFirstEnvAndEnvInt(t *testing.T) {
	t.Setenv("MOOX_TEST_A", "alpha")
	assert.Equal(t, "alpha", firstEnv("MOOX_TEST_A", "MOOX_TEST_B"))
	assert.Equal(t, "", firstEnv("MOOX_MISSING_X", "MOOX_MISSING_Y"))

	t.Setenv("MOOX_TEST_INT", "42")
	assert.Equal(t, 42, envInt("MOOX_TEST_INT", 7))
	assert.Equal(t, 7, envInt("MOOX_MISSING_INT", 7))
	t.Setenv("MOOX_TEST_INT_BAD", "nope")
	assert.Equal(t, 9, envInt("MOOX_TEST_INT_BAD", 9))
}

func TestNewHandlerValidation(t *testing.T) {
	_, err := NewHandler(Config{})
	require.Error(t, err)

	_, err = NewHandler(Config{ServiceName: "svc", IncludeRegex: "["})
	require.Error(t, err)

	h, err := NewHandler(DefaultConfig("svc"))
	require.NoError(t, err)
	require.NotNil(t, h)
}
