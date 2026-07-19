package main

import (
	"context"
	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestOnceOptionsFromEnv(t *testing.T) {
	t.Setenv("MOOX_SERVICE_GATEWAY_TARGET", "http://127.0.0.1:11000")
	t.Setenv("MOOX_RUNTIME_NODE_ID", "e2e-scf-node")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "127.0.0.1:11003")

	opts := onceOptionsFromEnv()
	if opts.ServiceGatewayTarget != "http://127.0.0.1:11000" {
		t.Fatalf("ServiceGatewayTarget = %q, want http://127.0.0.1:11000", opts.ServiceGatewayTarget)
	}
	if opts.NodeID != "e2e-scf-node" {
		t.Fatalf("NodeID = %q, want e2e-scf-node", opts.NodeID)
	}
	if opts.StorageRPCGatewayTarget != "127.0.0.1:11003" {
		t.Fatalf("StorageRPCGatewayTarget = %q, want 127.0.0.1:11003", opts.StorageRPCGatewayTarget)
	}
}

func TestInitializeRuntimeOnceDoesNotRequireTRPCConfig(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := initializeRuntime(context.Background(), runtimeapp.DefaultConfig(), false); err != nil {
		t.Fatalf("initializeRuntime() error = %v", err)
	}
}

func TestInitializeServerlessRuntimeDoesNotRequireTRPCConfig(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := initializeServerlessRuntime(context.Background(), runtimeapp.DefaultConfig()); err != nil {
		t.Fatalf("initializeServerlessRuntime() error = %v", err)
	}
}

func TestOnceOptionsFromEnv_EmptyDefaults(t *testing.T) {
	t.Setenv("MOOX_SERVICE_GATEWAY_TARGET", "")
	t.Setenv("MOOX_RUNTIME_NODE_ID", "")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "")
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
