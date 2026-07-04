package main

import (
	"context"
	"testing"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
)

func TestOnceOptionsFromEnv(t *testing.T) {
	t.Setenv("MOOX_SERVICE_GATEWAY_TARGET", "http://127.0.0.1:11000")
	t.Setenv("MOOX_RUNTIME_NODE_ID", "e2e-scf-node")
	t.Setenv("MOOX_STORAGE_METADATA_TARGET", "127.0.0.1:20100")
	t.Setenv("MOOX_STORAGE_ACCESS_TARGET", "127.0.0.1:20102")

	opts := onceOptionsFromEnv()
	if opts.ServiceGatewayTarget != "http://127.0.0.1:11000" {
		t.Fatalf("ServiceGatewayTarget = %q, want http://127.0.0.1:11000", opts.ServiceGatewayTarget)
	}
	if opts.NodeID != "e2e-scf-node" {
		t.Fatalf("NodeID = %q, want e2e-scf-node", opts.NodeID)
	}
	if opts.StorageMetadataTarget != "127.0.0.1:20100" {
		t.Fatalf("StorageMetadataTarget = %q, want 127.0.0.1:20100", opts.StorageMetadataTarget)
	}
	if opts.StorageAccessTarget != "127.0.0.1:20102" {
		t.Fatalf("StorageAccessTarget = %q, want 127.0.0.1:20102", opts.StorageAccessTarget)
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
