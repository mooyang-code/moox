package packager

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSCFPackageIncludesExpectedLayoutAndDoesNotMutateConfig(t *testing.T) {
	tmp := t.TempDir()
	binaryPath := filepath.Join(tmp, "main")
	configDir := filepath.Join(tmp, "configs")
	outPath := filepath.Join(tmp, "collector.zip")

	if err := os.WriteFile(binaryPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(configDir, "sources", "binance"), 0o755); err != nil {
		t.Fatal(err)
	}
	originalConfig := "system:\n  version: \"old\"\n  service_auth:\n    version: \"moox-auth-v1\"\ncollectors:\n  enabled: false\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(originalConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "trpc_go.yaml"), []byte("server: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "sources", "binance", "kline.yaml"), []byte("symbol: BTCUSDT\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := BuildSCFPackage(BuildSCFPackageOptions{
		BinaryPath: binaryPath,
		ConfigDir:  configDir,
		OutPath:    outPath,
		Version:    "v1.2.3",
		Overrides: map[string]string{
			"collectors.enabled": "true",
		},
	})
	if err != nil {
		t.Fatalf("BuildSCFPackage returned error: %v", err)
	}
	if result.Path != outPath {
		t.Fatalf("result path = %q, want %q", result.Path, outPath)
	}

	gotConfigBytes, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotConfigBytes) != originalConfig {
		t.Fatalf("source config mutated:\n%s", gotConfigBytes)
	}

	entries := readZipEntries(t, outPath)
	for _, name := range []string{"main", "config.yaml", "trpc_go.yaml", "sources/binance/kline.yaml"} {
		if _, ok := entries[name]; !ok {
			t.Fatalf("zip missing %s; entries=%v", name, mapKeys(entries))
		}
	}
	if got := string(entries["config.yaml"]); !strings.Contains(got, `version: v1.2.3`) || !strings.Contains(got, `enabled: true`) {
		t.Fatalf("patched config =\n%s", got)
	}
	if got := string(entries["config.yaml"]); !strings.Contains(got, `version: moox-auth-v1`) {
		t.Fatalf("service auth version should not be changed:\n%s", got)
	}
}

func readZipEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	entries := make(map[string][]byte)
	for _, file := range reader.File {
		rc, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		if err != nil {
			_ = rc.Close()
			t.Fatal(err)
		}
		if err := rc.Close(); err != nil {
			t.Fatal(err)
		}
		entries[file.Name] = data
	}
	return entries
}

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
