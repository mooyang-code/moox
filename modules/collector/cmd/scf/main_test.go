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

	if err := initializeRuntime(context.Background(), runtimeapp.DefaultConfig()); err != nil {
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

func TestConfigureResidentRuntimeFromExplicitOptions(t *testing.T) {
	configureResidentRuntime(onceOptions{
		ServiceGatewayTarget:    "http://127.0.0.1:11002",
		NodeID:                  "e2e-scf-node",
		StorageRPCGatewayTarget: "127.0.0.1:11003",
	})

	nodeID, _ := runtimeapp.GetNodeInfo()
	assert.Equal(t, "e2e-scf-node", nodeID)
	assert.Equal(t, "http://127.0.0.1:11002", runtimeapp.GetServiceGatewayTarget())
	assert.Equal(t, "127.0.0.1:11003", runtimeapp.GetStorageRPCGatewayTarget())
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, runtimeapp.WaitForReadiness(waitCtx), context.DeadlineExceeded)
}

func TestStartProductionRuntimeRequiresSpaceID(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "")

	err := startProductionRuntime(context.Background(), runtimeapp.DefaultConfig())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "MOOX_SPACE_ID")
}

func TestStartProductionRuntimeStartsFunctionAndOneRunnerWithoutTRPCServer(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Chdir(t.TempDir())
	oldRegisterFunction := registerCloudFunction
	oldRun := runTaskRunner
	t.Cleanup(func() {
		registerCloudFunction = oldRegisterFunction
		runTaskRunner = oldRun
	})

	functionStarts := make(chan struct{}, 1)
	releaseFunction := make(chan struct{})
	runnerStarts := make(chan struct{}, 2)
	registerCloudFunction = func() {
		functionStarts <- struct{}{}
		<-releaseFunction
	}
	runTaskRunner = func(ctx context.Context) error {
		runnerStarts <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	started := make(chan error, 1)
	go func() {
		started <- startProductionRuntime(ctx, runtimeapp.DefaultConfig())
	}()
	select {
	case <-functionStarts:
	case <-time.After(time.Second):
		t.Fatal("cloud function registration did not start")
	}
	select {
	case <-runnerStarts:
	case <-time.After(time.Second):
		t.Fatal("resident taskrunner did not start before blocking function registration")
	}
	select {
	case <-runnerStarts:
		t.Fatal("resident taskrunner started more than once")
	case <-time.After(10 * time.Millisecond):
	}
	close(releaseFunction)
	require.NoError(t, <-started)
}

func TestResidentTaskRunnerRestartsAfterTransportFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan int, 2)
	count := 0
	run := func(context.Context) error {
		count++
		calls <- count
		if count == 1 {
			return errors.New("temporary NATS failure")
		}
		cancel()
		return context.Canceled
	}

	runResidentTaskRunner(ctx, run, time.Millisecond)

	assert.Equal(t, 1, <-calls)
	assert.Equal(t, 2, <-calls)
}

func TestDurationEnv(t *testing.T) {
	t.Setenv("MOOX_RUNTIME_ONCE_TIMEOUT", "bad")
	assert.Equal(t, defaultOnceTimeout, durationEnv("MOOX_RUNTIME_ONCE_TIMEOUT", defaultOnceTimeout))
	t.Setenv("MOOX_RUNTIME_ONCE_TIMEOUT", "30s")
	assert.Equal(t, 30*time.Second, durationEnv("MOOX_RUNTIME_ONCE_TIMEOUT", defaultOnceTimeout))
}
