package binance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBinanceSourceConfigPathFindsDeployedCollectorConfigs(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "collector", "configs", "sources", "market", "binance.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("api: {}\nstorage:\n  bindings: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Chdir(root)

	got, err := resolveBinanceSourceConfigPath()
	if err != nil {
		t.Fatalf("resolveBinanceSourceConfigPath() error = %v", err)
	}
	if got != configPath {
		t.Fatalf("resolveBinanceSourceConfigPath() = %q, want %q", got, configPath)
	}
}
