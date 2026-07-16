package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_ValidConfig_ShouldApplyDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hostagent.yaml")
	content := "version: 0\ninterval: 0s\nidentity_path: ~/.local/state/moox/hostagent/identity.yaml\neventbus_config: ~/.config/moox/hostagent/eventbus.yaml\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.Version)
	assert.Equal(t, 15*time.Second, cfg.Interval)
	assert.NotContains(t, cfg.IdentityPath, "~")
	assert.NotContains(t, cfg.EventBusConfig, "~")
}

func TestLoad_HostNameOverride_ShouldTrimValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hostagent.yaml")
	content := "host_name: '  腾讯云-香港  '\nidentity_path: ~/.local/state/moox/hostagent/identity.yaml\neventbus_config: ~/.config/moox/hostagent/eventbus.yaml\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "腾讯云-香港", cfg.HostName)
}

func TestLoad_MissingRequiredPaths_ShouldReturnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hostagent.yaml")
	require.NoError(t, os.WriteFile(path, []byte("identity_path: \"\"\neventbus_config: \"\"\n"), 0o600))
	_, err := Load(path)
	assert.Error(t, err)
}

func TestLoad_MissingFile_ShouldReturnError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	assert.Error(t, err)
}

func TestLoadEventBus_Valid_ShouldSucceed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eventbus.yaml")
	content := "urls:\n  - nats://127.0.0.1:4222\nusername: hostagent\neventbus_token: token\nca_file: ~/ca.pem\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	cfg, err := LoadEventBus(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"nats://127.0.0.1:4222"}, cfg.URLs)
	assert.Equal(t, "hostagent", cfg.Username)
	assert.NotContains(t, cfg.CAFile, "~")
}

func TestLoadEventBus_BadPermission_ShouldReturnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eventbus.yaml")
	require.NoError(t, os.WriteFile(path, []byte("urls: [a]\nusername: u\neventbus_token: t\n"), 0o644))
	_, err := LoadEventBus(path)
	assert.Error(t, err)
}

func TestLoadEventBus_MissingFields_ShouldReturnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eventbus.yaml")
	require.NoError(t, os.WriteFile(path, []byte("urls: []\nusername: \"\"\neventbus_token: \"\"\n"), 0o600))
	_, err := LoadEventBus(path)
	assert.Error(t, err)
}

func TestLoad_InvalidYAML_ShouldReturnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hostagent.yaml")
	require.NoError(t, os.WriteFile(path, []byte("interval: ["), 0o600))
	_, err := Load(path)
	assert.Error(t, err)
}

func TestLoadEventBus_MissingFile_ShouldReturnError(t *testing.T) {
	_, err := LoadEventBus(filepath.Join(t.TempDir(), "missing.yaml"))
	assert.Error(t, err)
}

func TestExpand_HomeAndEnv(t *testing.T) {
	t.Setenv("MOOX_TEST_PATH", "expanded")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "x"), Expand("~/x"))
	assert.Equal(t, "expanded", Expand("$MOOX_TEST_PATH"))
}
