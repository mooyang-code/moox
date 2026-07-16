package collectorpackager

import (
	"archive/zip"
	"io"
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
		CLSTopicID: "topic-unified",
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
		if file.Name == "trpc_go.yaml" {
			stream, openErr := file.Open()
			require.NoError(t, openErr)
			content, readErr := io.ReadAll(stream)
			require.NoError(t, readErr)
			require.NoError(t, stream.Close())
			assert.Contains(t, string(content), "writer: cls")
			assert.Contains(t, string(content), "topic_id: topic-unified")
			assert.Contains(t, string(content), "secret_id: ${MOOX_CLS_SECRET_ID}")
		}
	}
	assert.ElementsMatch(t, []string{"main", "config.yaml", "trpc_go.yaml", "sources/binance/kline.yaml"}, names)
}

func TestBuildSCFPackage_MissingCLSTopicID_ShouldReturnError(t *testing.T) {
	tmp := t.TempDir()
	binaryPath := filepath.Join(tmp, "collector")
	require.NoError(t, os.WriteFile(binaryPath, []byte("binary"), 0o755))

	_, err := BuildSCFPackage(BuildSCFPackageOptions{
		BinaryPath: binaryPath,
		ConfigDir:  tmp,
		OutPath:    filepath.Join(tmp, "package.zip"),
	})
	require.ErrorContains(t, err, "CLS topic ID is required")
}
