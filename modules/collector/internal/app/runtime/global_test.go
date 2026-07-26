package runtime

import "testing"

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
