package readiness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockDetectsManifestOrEvidenceDrift(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "crypto_binance")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "market.yaml"), []byte("schema_version: 1\nmarket_id: crypto_binance\nspace_id: crypto_binance\nregister_metadata: true\nruntime_enabled: false\nexecution: {timeout_seconds: 60, job_budget_ms: 30000, report_reserve_ms: 10000}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "provider-validation.yaml"), []byte("schema_version: 1\nprovider_id: binance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Validate(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "provider-validation.yaml"), []byte("schema_version: 1\nprovider_id: changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lock.Validate(root); err == nil {
		t.Fatal("evidence drift accepted")
	}
}
