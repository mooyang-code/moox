package collectorpackager

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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
		CLSTopicID: "topic-central",
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
			assert.Contains(t, string(content), "level: info")
			assert.Contains(t, string(content), "topic_id: topic-central")
		}
	}
	assert.ElementsMatch(t, []string{"main", "config.yaml", "trpc_go.yaml", "sources/binance/kline.yaml"}, names)
}

func TestBuildSCFPackage_AllowsPlatformConfiguredCLS(t *testing.T) {
	tmp := t.TempDir()
	binaryPath := filepath.Join(tmp, "collector")
	require.NoError(t, os.WriteFile(binaryPath, []byte("binary"), 0o755))

	_, err := BuildSCFPackage(BuildSCFPackageOptions{
		BinaryPath: binaryPath,
		ConfigDir:  tmp,
		OutPath:    filepath.Join(tmp, "package.zip"),
	})
	require.ErrorContains(t, err, "config.yaml")
}

func TestBuildSCFPackage_RendersStorageAuthKeyWithoutPackagingSecret(t *testing.T) {
	tmp := t.TempDir()
	binaryPath := filepath.Join(tmp, "collector")
	require.NoError(t, os.WriteFile(binaryPath, []byte("binary"), 0o755))
	configDir := filepath.Join(tmp, "config")
	require.NoError(t, os.MkdirAll(filepath.Join(configDir, "sources", "market"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("storage: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "trpc_go.yaml"), []byte("server: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "sources", "market", "binance.yaml"), []byte(`
storage:
  bindings:
    spot:
      auth_info:
        app_id: moox-collector
        app_key: stale-key
`), 0o644))

	outPath := filepath.Join(tmp, "package.zip")
	_, err := BuildSCFPackage(BuildSCFPackageOptions{
		BinaryPath: binaryPath, ConfigDir: configDir, OutPath: outPath, CLSTopicID: "topic-central",
		StoragePrimaryAuthSecret: "storage-secret",
	})
	require.NoError(t, err)
	reader, err := zip.OpenReader(outPath)
	require.NoError(t, err)
	defer reader.Close()
	for _, file := range reader.File {
		if file.Name != "sources/market/binance.yaml" {
			continue
		}
		stream, err := file.Open()
		require.NoError(t, err)
		content, err := io.ReadAll(stream)
		require.NoError(t, err)
		require.NoError(t, stream.Close())
		assert.NotContains(t, string(content), "stale-key")
		assert.NotContains(t, string(content), "storage-secret")
		assert.Contains(t, string(content), "app_key: 455dd0d9d5bf0130a27b70bc6805d5b0a2059d6ff68677f914576e0c5c092e32")
		return
	}
	t.Fatal("rendered Binance config not found")
}

func TestBuildSCFPackage_RejectsBinanceConfigWithoutStorageSecret(t *testing.T) {
	tmp := t.TempDir()
	binaryPath := filepath.Join(tmp, "collector")
	require.NoError(t, os.WriteFile(binaryPath, []byte("binary"), 0o755))
	configDir := filepath.Join(tmp, "config")
	require.NoError(t, os.MkdirAll(filepath.Join(configDir, "sources", "market"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("storage: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "trpc_go.yaml"), []byte("server: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "sources", "market", "binance.yaml"), []byte(`
storage:
  auth_info:
    app_id: moox-collector
    app_key: stale-key
`), 0o644))

	_, err := BuildSCFPackage(BuildSCFPackageOptions{
		BinaryPath: binaryPath, ConfigDir: configDir, CLSTopicID: "topic-central",
		OutPath: filepath.Join(tmp, "package.zip"),
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "MOOX_STORAGE_PRIMARY_AUTH_SECRET")
}

func TestRenderTRPCConfigForServerlessUsesCentralCLSWriters(t *testing.T) {
	config := []byte(`plugins:
  log:
    default:
      - writer: cls
        remote_config: {topic_id: topic-a}
      - writer: cls
        remote_config: {topic_id: topic-b}
`)
	rendered, err := renderTRPCConfigForServerless(config, "topic-central")
	require.NoError(t, err)
	assert.Contains(t, string(rendered), "writer: cls")
	assert.Contains(t, string(rendered), "topic_id: topic-central")
	assert.NotContains(t, string(rendered), "topic-a")
	assert.NotContains(t, string(rendered), "topic-b")
}

func TestSCFPackageUsesOnlyCentralInfoCLSWriter(t *testing.T) {
	source := []byte(`plugins:
  log:
    default:
      - writer: console
        level: warn
      - writer: cls
        level: error
        remote_config:
          topic_id: stale-topic
`)
	rendered, err := renderTRPCConfigForServerless(source, "topic-central")
	require.NoError(t, err)

	var document map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &document))
	plugins := document["plugins"].(map[string]any)
	logs := plugins["log"].(map[string]any)
	writers := logs["default"].([]any)
	require.Len(t, writers, 1)
	for _, writer := range writers {
		config := writer.(map[string]any)
		assert.Equal(t, "cls", config["writer"])
		assert.Equal(t, "info", config["level"])
		remote := config["remote_config"].(map[string]any)
		assert.Equal(t, "topic-central", remote["topic_id"])
	}
}
