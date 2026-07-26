package main

import (
	"context"
	"errors"
	"testing"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestStartProductionRuntimeRequiresSpaceID(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "")

	err := startProductionRuntime(context.Background(), runtimeapp.DefaultConfig())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "MOOX_SPACE_ID")
}

func TestStartProductionRuntimeStartsServicesFunctionAndOneRunner(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Chdir(t.TempDir())
	oldRegisterTRPC := registerTRPCServices
	oldRegisterFunction := registerCloudFunction
	oldRun := runTaskRunner
	t.Cleanup(func() {
		registerTRPCServices = oldRegisterTRPC
		registerCloudFunction = oldRegisterFunction
		runTaskRunner = oldRun
	})

	trpcStarts := 0
	functionStarts := 0
	runnerStarts := make(chan struct{}, 2)
	registerTRPCServices = func() error {
		trpcStarts++
		return nil
	}
	registerCloudFunction = func() {
		functionStarts++
	}
	runTaskRunner = func(context.Context) error {
		runnerStarts <- struct{}{}
		return nil
	}

	require.NoError(t, startProductionRuntime(context.Background(), runtimeapp.DefaultConfig()))
	select {
	case <-runnerStarts:
	case <-time.After(time.Second):
		t.Fatal("resident taskrunner did not start")
	}
	select {
	case <-runnerStarts:
		t.Fatal("resident taskrunner started more than once")
	case <-time.After(10 * time.Millisecond):
	}
	assert.Equal(t, 1, trpcStarts)
	assert.Equal(t, 1, functionStarts)
}

func TestStartProductionRuntimeReturnsTRPCStartFailure(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Chdir(t.TempDir())
	oldRegisterTRPC := registerTRPCServices
	t.Cleanup(func() { registerTRPCServices = oldRegisterTRPC })
	registerTRPCServices = func() error { return errors.New("bad timer config") }

	err := startProductionRuntime(context.Background(), runtimeapp.DefaultConfig())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad timer config")
}

func TestDurationEnv(t *testing.T) {
	t.Setenv("MOOX_RUNTIME_ONCE_TIMEOUT", "bad")
	assert.Equal(t, defaultOnceTimeout, durationEnv("MOOX_RUNTIME_ONCE_TIMEOUT", defaultOnceTimeout))
	t.Setenv("MOOX_RUNTIME_ONCE_TIMEOUT", "30s")
	assert.Equal(t, 30*time.Second, durationEnv("MOOX_RUNTIME_ONCE_TIMEOUT", defaultOnceTimeout))
}
