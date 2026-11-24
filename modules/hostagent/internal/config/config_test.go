package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deployedConfig 是部署渲染后的配置形态。
const deployedConfig = "version: 0\nhost_id: '  control  '\nhost_name: '  腾讯云-香港  '\n" +
	"eventbus_config: /data/moox/secrets/eventbus/hostagent-publisher.yaml\n" +
	"host_policy_path: /data/moox/data/host-agent/host-policy.json\n" +
	"gateway_client: {caller: host-agent, key_file: /data/moox/secrets/host.key, host_id: control, local_address: '127.0.0.1:11002', ca_file: /data/moox/secrets/moox-ca.crt}\n"

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hostagent.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoad_DeployedConfig_ShouldTrimAndDefault(t *testing.T) {
	cfg, err := Load(writeConfig(t, deployedConfig))
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.Version)
	assert.Equal(t, "control", cfg.HostID)
	assert.Equal(t, "腾讯云-香港", cfg.HostName)
	assert.Equal(t, "/data/moox/data/host-agent/host-policy.json", cfg.HostPolicyPath)
}

// 去掉独立运行模式后，host_id、EventBus 凭据、主机策略路径与网关客户端都必须由部署给出。
func TestLoad_MissingDeploymentFields_ShouldReturnError(t *testing.T) {
	for name, content := range map[string]string{
		"host_id 为空":         "host_id: ''\neventbus_config: /a.yaml\nhost_policy_path: /p.json\ngateway_client: {caller: host-agent}\n",
		"host_id 不合法":        "host_id: Control_1\neventbus_config: /a.yaml\nhost_policy_path: /p.json\ngateway_client: {caller: host-agent}\n",
		"缺 EventBus 凭据":      "host_id: control\nhost_policy_path: /p.json\ngateway_client: {caller: host-agent}\n",
		"主机策略路径不是绝对路径":       "host_id: control\neventbus_config: /a.yaml\nhost_policy_path: p.json\ngateway_client: {caller: host-agent}\n",
		"网关调用方不是 host-agent": "host_id: control\neventbus_config: /a.yaml\nhost_policy_path: /p.json\ngateway_client: {caller: other}\n",
		"身份文件已删除":            deployedConfig + "identity_path: /data/moox/data/host-agent/identity.yaml\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, content))
			assert.Error(t, err)
		})
	}
}

func TestCheckedInConfigHasNoSamplingFrequency(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "app.yaml"))
	require.NoError(t, err)
	legacyKey := "inter" + "val:"
	assert.NotContains(t, string(data), legacyKey)
}

func TestLoadRejectsApplicationOwnedSamplingFrequency(t *testing.T) {
	legacySchedule := "inter" + "val: 15s\n"
	_, err := Load(writeConfig(t, legacySchedule+deployedConfig))
	assert.Error(t, err)
}

func TestLoad_MissingFile_ShouldReturnError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	assert.Error(t, err)
}

func TestLoadEventBus_Valid_ShouldSucceed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eventbus.yaml")
	content := "urls:\n  - nats://127.0.0.1:4222\nusername: hostagent\neventbus_token: token\nca_file: /etc/moox/ca.pem\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	cfg, err := LoadEventBus(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"nats://127.0.0.1:4222"}, cfg.URLs)
	assert.Equal(t, "hostagent", cfg.Username)
	assert.Equal(t, "/etc/moox/ca.pem", cfg.CAFile)
}

func TestLoadEventBus_RelativeCA_ShouldResolveFromCredentialDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eventbus.yaml")
	content := "urls: [nats://127.0.0.1:4222]\nusername: hostagent\neventbus_token: token\nca_file: ca.pem\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	cfg, err := LoadEventBus(path)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "ca.pem"), cfg.CAFile)
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
	require.NoError(t, os.WriteFile(path, []byte("host_id: ["), 0o600))
	_, err := Load(path)
	assert.Error(t, err)
}

func TestLoadEventBus_MissingFile_ShouldReturnError(t *testing.T) {
	_, err := LoadEventBus(filepath.Join(t.TempDir(), "missing.yaml"))
	assert.Error(t, err)
}
