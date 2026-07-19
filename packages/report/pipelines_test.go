package report

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPipelineAllowlistValidatesAndHashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipelines.yaml")
	raw := "version: 1\npipelines:\n  - id: strategy-targets\n    module: strategy\n    space_id: crypto\n    input_dataset: factors\n    output_dataset: targets\n    lag_tolerance: 5m\n    enabled: true\n    crosses_storage_deferred: true\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPipelineAllowlist(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Pipelines) != 1 || len(cfg.IDsForModule("strategy")) != 1 || cfg.Checksum == "" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestPipelineAllowlistRejectsInvalidDefinitions(t *testing.T) {
	for _, raw := range []string{
		"version: 1\npipelines:\n  - id: duplicate\n    module: factor\n    space_id: s\n    input_dataset: i\n    output_dataset: o\n    lag_tolerance: 1s\n  - id: duplicate\n    module: factor\n    space_id: s\n    input_dataset: i\n    output_dataset: o\n    lag_tolerance: 1s\n",
		"version: 1\npipelines:\n  - id: invalid\n    module: factor\n    space_id: ''\n    input_dataset: i\n    output_dataset: o\n    lag_tolerance: 0s\n",
	} {
		path := filepath.Join(t.TempDir(), "pipelines.yaml")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPipelineAllowlist(path); err == nil {
			t.Fatalf("invalid config accepted: %s", raw)
		}
	}
}
