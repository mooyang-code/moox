package unitinstall

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// 渲染身份字段不能丢掉模板里 gateway_client 下的其他设置（例如 Access 的连接池参数）。
func TestSetGatewayIdentityKeepsTemplateSettings(t *testing.T) {
	document, err := yamlDocument([]byte("gateway_client:\n  caller: access\n  key_id: \"\"\n  key_file: placeholder\n  pool:\n    min_idle: 16\nverification_file: v.json\n"))
	require.NoError(t, err)
	require.NoError(t, setGatewayIdentity(member(document, "gateway_client"), "access", "key-7", "storage", "/data/moox/host/current/certs/moox-ca.crt"))
	encoded, err := yaml.Marshal(document)
	require.NoError(t, err)
	var rendered struct {
		Client struct {
			Caller  string `yaml:"caller"`
			KeyID   string `yaml:"key_id"`
			KeyFile string `yaml:"key_file"`
			HostID  string `yaml:"host_id"`
			Local   string `yaml:"local_address"`
			CAFile  string `yaml:"ca_file"`
			Pool    struct {
				MinIdle int `yaml:"min_idle"`
			} `yaml:"pool"`
		} `yaml:"gateway_client"`
		Verification string `yaml:"verification_file"`
	}
	require.NoError(t, yaml.Unmarshal(encoded, &rendered))
	require.Equal(t, "access", rendered.Client.Caller)
	require.Equal(t, "key-7", rendered.Client.KeyID)
	require.Equal(t, "../../secrets/caller-access.key", rendered.Client.KeyFile)
	require.Equal(t, "storage", rendered.Client.HostID)
	require.Equal(t, "127.0.0.1:11002", rendered.Client.Local)
	require.Equal(t, "/data/moox/host/current/certs/moox-ca.crt", rendered.Client.CAFile)
	require.Equal(t, 16, rendered.Client.Pool.MinIdle)
	require.Equal(t, "v.json", rendered.Verification)
}
