package collectorpackager

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSCFPackageRequiresBinaryAndConfig(t *testing.T) {
	_, err := BuildSCFPackage(BuildSCFPackageOptions{ConfigDir: "cfg", OutPath: "out.zip"})
	require.ErrorContains(t, err, "binary path is required")
}

func TestBuildSCFPackageExcludesTRPCAndCLSCredentials(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "main")
	require.NoError(t, os.WriteFile(binary, []byte("binary"), 0o755))
	config := filepath.Join(tmp, "config")
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources", "example"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(config, "config.yaml"), []byte("system: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(config, "trpc_go.yaml"), []byte("secret: should-not-package\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(config, "sources", "example", "source.yaml"), []byte("kind: example\n"), 0o644))
	out := filepath.Join(tmp, "package.zip")
	result, err := BuildSCFPackage(BuildSCFPackageOptions{BinaryPath: binary, ConfigDir: config, OutPath: out})
	require.NoError(t, err)
	assert.Equal(t, []string{"config.yaml", "main", "sources/example/source.yaml"}, result.Entries)
	reader, err := zip.OpenReader(out)
	require.NoError(t, err)
	defer reader.Close()
	for _, file := range reader.File {
		assert.NotContains(t, file.Name, "trpc_go.yaml")
		assert.NotContains(t, file.Name, "secret")
	}
}

func TestBuildSCFPackageIncludesStockCNCalendar(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "main")
	require.NoError(t, os.WriteFile(binary, []byte("binary"), 0o755))
	config := filepath.Join(tmp, "modules", "collector", "configs", "scf", "stock_cn")
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(config, "config.yaml"), []byte("system: {}\n"), 0o644))
	calendar := filepath.Join(tmp, "modules", "collector", "config", "markets", "stock_cn", "calendar.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(calendar), 0o755))
	require.NoError(t, os.WriteFile(calendar, []byte("timezone: Asia/Shanghai\n"), 0o644))

	out := filepath.Join(tmp, "package.zip")
	result, err := BuildSCFPackage(BuildSCFPackageOptions{BinaryPath: binary, ConfigDir: config, OutPath: out})
	require.NoError(t, err)
	assert.Contains(t, result.Entries, "markets/stock_cn/calendar.yaml")

	reader, err := zip.OpenReader(out)
	require.NoError(t, err)
	defer reader.Close()
	var found bool
	for _, file := range reader.File {
		if file.Name == "markets/stock_cn/calendar.yaml" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestBuildSCFPackageRequiresStockCNCalendar(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "main")
	require.NoError(t, os.WriteFile(binary, []byte("binary"), 0o755))
	config := filepath.Join(tmp, "modules", "collector", "configs", "scf", "stock_cn")
	require.NoError(t, os.MkdirAll(config, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(config, "config.yaml"), []byte("system: {}\n"), 0o644))

	_, err := BuildSCFPackage(BuildSCFPackageOptions{BinaryPath: binary, ConfigDir: config, OutPath: filepath.Join(tmp, "package.zip")})
	require.ErrorContains(t, err, "stock_cn calendar")
}

func TestBuildSCFPackageSetsOutputMode0600(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "main")
	config := filepath.Join(tmp, "config")
	out := filepath.Join(tmp, "package.zip")
	require.NoError(t, os.WriteFile(binary, []byte("binary"), 0o755))
	require.NoError(t, os.MkdirAll(config, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(config, "config.yaml"), []byte("system: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(out, []byte("old"), 0o644))

	_, err := BuildSCFPackage(BuildSCFPackageOptions{BinaryPath: binary, ConfigDir: config, OutPath: out})
	require.NoError(t, err)
	info, err := os.Stat(out)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
