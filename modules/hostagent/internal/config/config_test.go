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

// host_id、主机策略路径与网关客户端都必须由部署给出；已删除的 EventBus 凭据字段按未知字段拒绝。
func TestLoad_MissingDeploymentFields_ShouldReturnError(t *testing.T) {
	for name, content := range map[string]string{
		"host_id 为空":         "host_id: ''\nhost_policy_path: /p.json\ngateway_client: {caller: host-agent}\n",
		"host_id 不合法":        "host_id: Control_1\nhost_policy_path: /p.json\ngateway_client: {caller: host-agent}\n",
		"已删除的 EventBus 凭据":   deployedConfig + "eventbus_config: /a.yaml\n",
		"主机策略路径不是绝对路径":       "host_id: control\nhost_policy_path: p.json\ngateway_client: {caller: host-agent}\n",
		"网关调用方不是 host-agent": "host_id: control\nhost_policy_path: /p.json\ngateway_client: {caller: other}\n",
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

func TestLoad_InvalidYAML_ShouldReturnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hostagent.yaml")
	require.NoError(t, os.WriteFile(path, []byte("host_id: ["), 0o600))
	_, err := Load(path)
	assert.Error(t, err)
}
