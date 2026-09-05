package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDSL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.yaml")
	if err := os.WriteFile(path, []byte(`name: momentum
triggers: {event: {name: source.ready}}
data: {bar: 1h, calendar: crypto_24x7}
rules:
  main: {pool: [BTC-USDT-SPOT], score: close, select: {top: 1}, weight: 1}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut := mustTempFiles(t)
	if err := runCLI([]string{"validate", "--space-id", "space-1", path}, out, errOut); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(out.Name()); err != nil || !strings.Contains(string(raw), "name=momentum") || strings.Contains(string(raw), "hash=") {
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
