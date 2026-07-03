package control

import "testing"

func TestStorageDeprecatedURLsDoNotBackfillTRPCTargets(t *testing.T) {
	cfg := Default()
	cfg.Storage.MetadataTarget = "127.0.0.1:20100"
	cfg.Storage.AccessTarget = "127.0.0.1:20102"
	cfg.Storage.MetadataURL = "http://127.0.0.1:20200"
	cfg.Storage.AccessURL = "http://127.0.0.1:20201"

	if err := cfg.validateStorageTargets(); err != nil {
		t.Fatalf("validateStorageTargets() error = %v", err)
	}
	if cfg.Storage.MetadataTarget != "127.0.0.1:20100" || cfg.Storage.AccessTarget != "127.0.0.1:20102" {
		t.Fatalf("deprecated URLs changed targets: metadata=%q access=%q", cfg.Storage.MetadataTarget, cfg.Storage.AccessTarget)
	}
}

func TestStorageTargetsRejectHTTP(t *testing.T) {
	cfg := Default()
	cfg.Storage.MetadataTarget = "http://127.0.0.1:20200"

	if err := cfg.validateStorageTargets(); err == nil {
		t.Fatalf("validateStorageTargets() expected error for HTTP target")
	}
}
