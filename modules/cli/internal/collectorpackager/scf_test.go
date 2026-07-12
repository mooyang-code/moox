package collectorpackager

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSCFPackage_MissingBinaryPath_ShouldReturnError(t *testing.T) {
	_, err := BuildSCFPackage(BuildSCFPackageOptions{ConfigDir: "cfg", OutPath: "out.zip"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "binary path is required")
}

func TestBuildSCFPackage_ValidInputs_ShouldCreateZipWithExpectedEntries(t *testing.T) {
	tmp := t.TempDir()
	binaryPath := filepath.Join(tmp, "collector")
	require.NoError(t, os.WriteFile(binaryPath, []byte("binary"), 0o755))

	configDir := filepath.Join(tmp, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("storage: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "example_trpc_go.yaml"), []byte("server: {}\n"), 0o644))

	sourcesDir := filepath.Join(configDir, "sources", "binance")
	require.NoError(t, os.MkdirAll(sourcesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sourcesDir, "kline.yaml"), []byte("kind: kline\n"), 0o644))

	outPath := filepath.Join(tmp, "package.zip")
	result, err := BuildSCFPackage(BuildSCFPackageOptions{
		BinaryPath: binaryPath,
		ConfigDir:  configDir,
		OutPath:    outPath,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, outPath, result.Path)
	assert.Equal(t, []string{"config.yaml", "main", "sources/binance/kline.yaml", "trpc_go.yaml"}, result.Entries)

	reader, err := zip.OpenReader(outPath)
	require.NoError(t, err)
	defer reader.Close()

	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}
	assert.ElementsMatch(t, []string{"main", "config.yaml", "trpc_go.yaml", "sources/binance/kline.yaml"}, names)
}
