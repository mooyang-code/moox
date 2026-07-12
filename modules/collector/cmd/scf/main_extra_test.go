package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOnceOptionsFromEnv_EmptyDefaults(t *testing.T) {
	t.Setenv("MOOX_SERVICE_GATEWAY_TARGET", "")
	t.Setenv("MOOX_RUNTIME_NODE_ID", "")
	t.Setenv("MOOX_STORAGE_METADATA_TARGET", "")
	t.Setenv("MOOX_STORAGE_ACCESS_TARGET", "")
	opts := onceOptionsFromEnv()
	assert.Equal(t, "", opts.ServiceGatewayTarget)
	assert.Equal(t, "", opts.NodeID)
}

func TestRunOnce_RequiresGatewayAndNodeID(t *testing.T) {
	err := runOnce(context.Background(), onceOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "service-gateway-target")

	err = runOnce(context.Background(), onceOptions{ServiceGatewayTarget: "http://127.0.0.1:11000"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node-id")
}

func TestIntEnvAndDurationEnv(t *testing.T) {
	t.Setenv("MOOX_RUNTIME_SERVER_PORT", "not-a-number")
	assert.Equal(t, 0, intEnv("MOOX_RUNTIME_SERVER_PORT"))
	t.Setenv("MOOX_RUNTIME_SERVER_PORT", "8080")
	assert.Equal(t, 8080, intEnv("MOOX_RUNTIME_SERVER_PORT"))
	t.Setenv("MOOX_RUNTIME_ONCE_TIMEOUT", "bad")
	assert.Equal(t, 90*time.Second, durationEnv("MOOX_RUNTIME_ONCE_TIMEOUT", 90*time.Second))
	t.Setenv("MOOX_RUNTIME_ONCE_TIMEOUT", "30s")
	assert.Equal(t, 30*time.Second, durationEnv("MOOX_RUNTIME_ONCE_TIMEOUT", 90*time.Second))
}
