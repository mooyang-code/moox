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
