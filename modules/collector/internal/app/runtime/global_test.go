package runtime

import "testing"

func TestStorageTargetsUseEnvironmentBeforeLocalConfig(t *testing.T) {
	resetStorageTargetState(t)
	t.Setenv("MOOX_STORAGE_METADATA_TARGET", "ip://metadata.example.com:20100/")
	t.Setenv("MOOX_STORAGE_ACCESS_TARGET", "ip://access.example.com:20102/")

	if got := GetStorageMetadataTarget(); got != "ip://metadata.example.com:20100" {
		t.Fatalf("GetStorageMetadataTarget() = %q", got)
	}
	if got := GetStorageAccessTarget(); got != "ip://access.example.com:20102" {
		t.Fatalf("GetStorageAccessTarget() = %q", got)
	}
}

func TestStorageRuntimeTargetsOverrideEnvironment(t *testing.T) {
	resetStorageTargetState(t)
	t.Setenv("MOOX_STORAGE_METADATA_TARGET", "ip://metadata.example.com:20100")
	t.Setenv("MOOX_STORAGE_ACCESS_TARGET", "ip://access.example.com:20102")

	UpdateStorageTargets("ip://runtime-metadata.example.com:20100/", "ip://runtime-access.example.com:20102/")

	if got := GetStorageMetadataTarget(); got != "ip://runtime-metadata.example.com:20100" {
		t.Fatalf("GetStorageMetadataTarget() = %q", got)
	}
	if got := GetStorageAccessTarget(); got != "ip://runtime-access.example.com:20102" {
		t.Fatalf("GetStorageAccessTarget() = %q", got)
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
		StorageMetadataTarget: "127.0.0.1:20100",
		StorageAccessTarget:   "127.0.0.1:20102",
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
