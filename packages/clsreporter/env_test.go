package clsreporter

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigFromEnv(t *testing.T) {
	values := map[string]string{"MOOX_CLS_ENABLED": "true", "MOOX_CLS_ENDPOINT": "ap-guangzhou.cls.tencentyun.com", "MOOX_CLS_TOPIC_ID": "topic", "MOOX_CLS_SECRET_ID": "id", "MOOX_CLS_SECRET_KEY": "key", "MOOX_CLS_TIMEOUT_MS": "800"}
	cfg, enabled, err := ConfigFromEnv(func(key string) string { return values[key] })
	require.NoError(t, err)
	require.True(t, enabled)
	require.Equal(t, 800*time.Millisecond, cfg.Timeout)
	delete(values, "MOOX_CLS_TOPIC_ID")
	_, _, err = ConfigFromEnv(func(key string) string { return values[key] })
	require.ErrorContains(t, err, "MOOX_CLS_TOPIC_ID")
}

func TestConfigFromEnvDisabled(t *testing.T) {
	_, enabled, err := ConfigFromEnv(func(string) string { return "" })
	require.NoError(t, err)
	require.False(t, enabled)
}
