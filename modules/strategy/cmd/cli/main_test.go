package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.yaml")
	if err := os.WriteFile(path, []byte(`api_version: moox.strategy/v2
kind: coin_selection
input:
  source_view_id: source
  data_frequency: 1h
  factors:
    - factor_id: bias
instrument_pool:
  markets: [spot]
long:
  side_weight: "1"
  scores:
    - factor_id: bias
      direction: ascending
      weight: "1"
  selection:
    mode: count
    value: "1"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut := mustTempFiles(t)
	if err := runCLI([]string{"validate", "--space-id", "space-1", path}, out, errOut); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(out.Name()); err != nil || !strings.Contains(string(raw), "kind=coin_selection") {
		t.Fatalf("validate output = %s, err=%v", raw, err)
	}
}

func mustTempFiles(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	errOut, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	return out, errOut
}
