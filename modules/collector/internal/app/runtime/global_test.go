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

func resetStorageTargetState(t *testing.T) {
	t.Helper()

	localAppConfigMu.Lock()
	oldLocal := LocalAppConfig
	LocalAppConfig = &AppConfig{System: &SystemConfig{
		StorageRPC: StorageRPCConfig{GatewayTarget: "ip://127.0.0.1:11003"},
	}}
	localAppConfigMu.Unlock()

	t.Cleanup(func() {
		localAppConfigMu.Lock()
		LocalAppConfig = oldLocal
		localAppConfigMu.Unlock()
	})
}
