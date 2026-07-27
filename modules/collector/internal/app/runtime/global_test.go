package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRuntimeReadinessRequiresNodeAndServiceGateway(t *testing.T) {
	resetStorageTargetState(t)
	resetRuntimeReadinessForTest(t)

	UpdateNodeInfo("node-1", "v1")
	if SignalReadinessIfConfigured() {
		t.Fatal("SignalReadinessIfConfigured() = true without service gateway")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := WaitForReadiness(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForReadiness() error = %v, want deadline exceeded", err)
	}

	UpdateServiceGatewayTarget("http://gateway.example.com:11000")
	if !SignalReadinessIfConfigured() {
		t.Fatal("SignalReadinessIfConfigured() = false with node and service gateway")
	}
	if err := WaitForReadiness(context.Background()); err != nil {
		t.Fatalf("WaitForReadiness() error = %v", err)
	}
}

func TestRuntimeReadinessWaitHonorsCancellation(t *testing.T) {
	resetRuntimeReadinessForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := WaitForReadiness(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitForReadiness() error = %v, want canceled", err)
	}
}

func TestStorageTargetsUseEnvironmentBeforeLocalConfig(t *testing.T) {
	resetStorageTargetState(t)
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://gateway.example.com:11003/")

	if got := GetStorageRPCGatewayTarget(); got != "ip://gateway.example.com:11003" {
		t.Fatalf("GetStorageRPCGatewayTarget() = %q", got)
	}
	if got := GetStorageRPCGatewayTarget(); got != "ip://gateway.example.com:11003" {
		t.Fatalf("GetStorageRPCGatewayTarget() = %q", got)
	}
}

func TestStorageRuntimeTargetsOverrideEnvironment(t *testing.T) {
	resetStorageTargetState(t)
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://gateway.example.com:11003")

	UpdateStorageRPCGatewayTarget("ip://runtime-gateway.example.com:11003/")

	if got := GetStorageRPCGatewayTarget(); got != "ip://runtime-gateway.example.com:11003" {
		t.Fatalf("GetStorageRPCGatewayTarget() = %q", got)
	}
	if got := GetStorageRPCGatewayTarget(); got != "ip://runtime-gateway.example.com:11003" {
		t.Fatalf("GetStorageRPCGatewayTarget() = %q", got)
	}
}

func TestServiceGatewayTargetUsesCanonicalRuntimeTarget(t *testing.T) {
	resetStorageTargetState(t)

	UpdateServiceGatewayTarget("http://gateway.example.com:11000/")

	if got := GetServiceGatewayTarget(); got != "http://gateway.example.com:11000" {
		t.Fatalf("GetServiceGatewayTarget() = %q", got)
	}
}

func resetStorageTargetState(t *testing.T) {
	t.Helper()

	configMu.Lock()
	oldGlobal := GlobalConfig
	GlobalConfig = Config{}
	configMu.Unlock()

	localAppConfigMu.Lock()
	oldLocal := LocalAppConfig
	LocalAppConfig = &AppConfig{System: &SystemConfig{
		StorageRPC: StorageRPCConfig{GatewayTarget: "ip://127.0.0.1:11003"},
	}}
	localAppConfigMu.Unlock()

	t.Cleanup(func() {
		configMu.Lock()
		GlobalConfig = oldGlobal
		configMu.Unlock()

		localAppConfigMu.Lock()
		LocalAppConfig = oldLocal
		localAppConfigMu.Unlock()
	})
}

func resetRuntimeReadinessForTest(t *testing.T) {
	t.Helper()
	old := processReadiness
	processReadiness = newReadiness()
	t.Cleanup(func() {
		processReadiness = old
	})
}
