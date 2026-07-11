package marketmanifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsUnknownFieldsBeforeRegistryMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stock_cn", "market.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, path, `schema_version: 1
market_id: stock_cn
space_id: stock_cn
register_metadata: true
runtime_enabled: false
execution:
  timeout_seconds: 60
  job_budget_ms: 30000
  report_reserve_ms: 10000
unknown_field: true
`)
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("expected strict unknown-field error, got %v", err)
	}
}

func TestLoadValidatesIdentityProvidersDatasetsAndExecutionBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stock_cn", "market.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	base := `schema_version: 1
market_id: stock_cn
space_id: stock_cn
register_metadata: true
runtime_enabled: false
execution:
  timeout_seconds: 60
  job_budget_ms: 30000
  report_reserve_ms: 10000
providers:
  - id: tdx
    capabilities: [kline]
    quotas:
      - scope: ip
        window_seconds: 60
        limit: 10
        weight: 1
datasets:
  - id: tdx_equity_kline
    role: provider_data
    provider_id: tdx
feeds:
  - id: equity_kline
    dataset_id: tdx_equity_kline
`
	writeManifest(t, path, base)
	if _, err := LoadFile(path); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	writeManifest(t, path, strings.Replace(base, "provider_id: tdx", "provider_id: missing", 1))
	if _, err := LoadFile(path); err == nil {
		t.Fatal("unknown dataset provider should be rejected")
	}
	writeManifest(t, path, strings.Replace(base, "limit: 10", "limit: -1", 1))
	if _, err := LoadFile(path); err == nil {
		t.Fatal("negative quota should be rejected")
	}
	writeManifest(t, path, strings.Replace(base, "job_budget_ms: 30000", "job_budget_ms: 30001", 1))
	if _, err := LoadFile(path); err == nil {
		t.Fatal("budget over 30 seconds should be rejected")
	}
	writeManifest(t, path, strings.Replace(base, "timeout_seconds: 60", "timeout_seconds: 39", 1))
	if _, err := LoadFile(path); err == nil {
		t.Fatal("timeout without reserve should be rejected")
	}
}

func TestLoadRejectsPathMismatchAndEmbeddedSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stock_us", "market.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, path, `schema_version: 1
market_id: stock_cn
space_id: stock_cn
execution: {timeout_seconds: 60, job_budget_ms: 30000, report_reserve_ms: 10000}
`)
	if _, err := LoadFile(path); err == nil {
		t.Fatal("directory/market mismatch should be rejected")
	}
	writeManifest(t, path, `schema_version: 1
market_id: stock_us
space_id: stock_us
execution: {timeout_seconds: 60, job_budget_ms: 30000, report_reserve_ms: 10000}
providers:
  - id: bad
    credential_env: MOOX_BAD_KEY
    token: leaked
`)
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("embedded secret should be rejected, got %v", err)
	}
}

func writeManifest(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
