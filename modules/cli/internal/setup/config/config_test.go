package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSCFFetcherRejectsUnusableFleetAndUnsafeConcurrency(t *testing.T) {
	base := func() SCFFetcher {
		return SCFFetcher{
			Enabled: true,
			Spaces: []SCFFetcherSpace{{
				SpaceID: "crypto", MemorySize: 64, TimeoutSeconds: 15,
				StorageRPCGatewayTarget: "ip://106.53.107.122:11003",
				MaxInflightRequests:     32, RequestTimeoutMS: 1500,
				HTTPMaxAttempts: 4, StorageMaxAttempts: 3,
				Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1}},
			}},
		}
	}

	t.Run("requires an enabled region", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].Regions[0].Enabled = false
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least one enabled region")
	})

	t.Run("caps invocation concurrency at the executor bound", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].MaxInflightRequests = 65
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "between 1 and 64")
	})

	t.Run("caps aggregate Storage retries at three attempts", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].StorageMaxAttempts = 4
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "request/storage attempts")
	})

	t.Run("allows a 30-item realtime batch for fleet fanout", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].RealtimeBatchSize = 30
		cfg.Spaces[0].Regions[0].CloudAccountID = "tencent-scf-guangzhou"
		require.NoError(t, validateSCFFetcher(&cfg))
	})

	t.Run("accepts the standard 10-item timer and invoke budget", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].RealtimeBatchSize = 10
		cfg.Spaces[0].MaxInflightRequests = 10
		cfg.Spaces[0].RequestTimeoutMS = 2000
		cfg.Spaces[0].Regions[0].CloudAccountID = "tencent-scf-guangzhou"
		require.NoError(t, validateSCFFetcher(&cfg))
	})

	t.Run("rejects a realtime batch above the SCF request bound", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].RealtimeBatchSize = 31
		cfg.Spaces[0].Regions[0].CloudAccountID = "tencent-scf-guangzhou"
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "between 1 and 30")
	})

	t.Run("rejects a batch that cannot finish before the SCF deadline", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].RealtimeBatchSize = 30
		cfg.Spaces[0].MaxInflightRequests = 1
		cfg.Spaces[0].RequestTimeoutMS = 7000
		cfg.Spaces[0].Regions[0].CloudAccountID = "tencent-scf-guangzhou"
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "request waves")
	})

	t.Run("reserves the CLS flush window", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].RealtimeBatchSize = 30
		cfg.Spaces[0].MaxInflightRequests = 32
		cfg.Spaces[0].RequestTimeoutMS = 5000
		cfg.Spaces[0].Regions[0].CloudAccountID = "tencent-scf-guangzhou"
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reserves")
	})
}

const validManifest = `[admin]
username = "admin"
password = "admin-password"

[tencent_cloud]
secret_id = "secret-id"
secret_key = "secret-key"

[eventbus]
public_address = "eventbus.example.test"
port = 4222
tls_enabled = true

[control_host]
name = "control"
address = "192.0.2.10"
port = 22
username = "ubuntu"
password = "control-password"
`

func writeManifest(t *testing.T, root, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(root, "custom.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), mode))
	require.NoError(t, os.Chmod(path, mode))
	return path
}

func TestLoadValidManifest(t *testing.T) {
	root := t.TempDir()
	path := writeManifest(t, root, validManifest, 0o600)

	snapshot, err := Load(path, root)
	require.NoError(t, err)
	assert.Equal(t, "admin", snapshot.Manifest.Admin.Username)
	assert.Equal(t, "eventbus.example.test", snapshot.Manifest.EventBus.PublicAddress)
	assert.Equal(t, 4222, snapshot.Manifest.EventBus.Port)
	assert.True(t, snapshot.Manifest.EventBus.TLSEnabled)
	assert.Equal(t, "ap-guangzhou", snapshot.Manifest.TencentCloud.Region)
	assert.Equal(t, 22, snapshot.Manifest.ControlHost.Port)
	assert.Empty(t, snapshot.Manifest.Monitoring.WeComWebhook)
	assert.Empty(t, snapshot.Manifest.OtherHosts)
	require.NoError(t, snapshot.VerifyUnchanged())
}

func TestLoadMonitoringWeComWebhook(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, "[control_host]", `[monitoring]
wecom_webhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"

[control_host]`, 1)

	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test", snapshot.Manifest.Monitoring.WeComWebhook)
}

func TestLoadDefaultsEventBusPort(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, "port = 4222\n", "", 1)

	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, 4222, snapshot.Manifest.EventBus.Port)
}

func TestLoadTencentCloudRegion(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, `secret_key = "secret-key"`, "secret_key = \"secret-key\"\nregion = \"ap-shanghai\"", 1)

	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, "ap-shanghai", snapshot.Manifest.TencentCloud.Region)
}

func TestLoadDefaultsHostPorts(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, "port = 22\n", "", 1) + `
[[other_hosts]]
name = "compute"
address = "192.0.2.11"
username = "ubuntu"
password = "compute-password"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, 22, snapshot.Manifest.ControlHost.Port)
	assert.Equal(t, 22, snapshot.Manifest.OtherHosts[0].Port)
}

func TestLoadOptionalCompileHost(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[compile_host]
name = "compile"
address = "192.0.2.20"
username = "builder"
`

	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.True(t, snapshot.Manifest.HasCompileHost())
	assert.Equal(t, "compile", snapshot.Manifest.CompileHost.Name)
	assert.Equal(t, 22, snapshot.Manifest.CompileHost.Port)
	assert.Len(t, snapshot.Manifest.Hosts(), 1)
}

func TestLoadRejectsInvalidManifest(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing admin password", body: strings.Replace(validManifest, `password = "admin-password"`, `password = ""`, 1), want: "admin.password"},
		{name: "bcrypt password too long", body: strings.Replace(validManifest, "admin-password", strings.Repeat("x", 73), 1), want: "72 bytes"},
		{name: "missing secret id", body: strings.Replace(validManifest, `secret_id = "secret-id"`, `secret_id = ""`, 1), want: "tencent_cloud.secret_id"},
		{name: "missing secret key", body: strings.Replace(validManifest, `secret_key = "secret-key"`, `secret_key = ""`, 1), want: "tencent_cloud.secret_key"},
		{name: "empty region", body: strings.Replace(validManifest, `secret_key = "secret-key"`, "secret_key = \"secret-key\"\nregion = \" \"", 1), want: "tencent_cloud.region"},
		{name: "missing eventbus address", body: strings.Replace(validManifest, `public_address = "eventbus.example.test"`, `public_address = ""`, 1), want: "eventbus.public_address"},
		{name: "eventbus address with surrounding whitespace", body: strings.Replace(validManifest, `eventbus.example.test`, ` eventbus.example.test `, 1), want: "eventbus.public_address"},
		{name: "eventbus address with scheme", body: strings.Replace(validManifest, `eventbus.example.test`, `tls://eventbus.example.test`, 1), want: "eventbus.public_address"},
		{name: "eventbus address with path", body: strings.Replace(validManifest, `eventbus.example.test`, `eventbus.example.test/nats`, 1), want: "eventbus.public_address"},
		{name: "eventbus address with port", body: strings.Replace(validManifest, `eventbus.example.test`, `eventbus.example.test:4222`, 1), want: "eventbus.public_address"},
		{name: "eventbus ipv6 address", body: strings.Replace(validManifest, `eventbus.example.test`, `2001:db8::1`, 1), want: "eventbus.public_address"},
		{name: "eventbus explicit zero port", body: strings.Replace(validManifest, "port = 4222", "port = 0", 1), want: "eventbus.port"},
		{name: "eventbus invalid port", body: strings.Replace(validManifest, "port = 4222", "port = 70000", 1), want: "eventbus.port"},
		{name: "eventbus tls disabled", body: strings.Replace(validManifest, "tls_enabled = true", "tls_enabled = false", 1), want: "eventbus.tls_enabled"},
		{name: "monitoring webhook must use HTTPS", body: strings.Replace(validManifest, "[control_host]", "[monitoring]\nwecom_webhook = \"http://example.test/hook\"\n\n[control_host]", 1), want: "monitoring.wecom_webhook"},
		{name: "monitoring webhook must be a URL", body: strings.Replace(validManifest, "[control_host]", "[monitoring]\nwecom_webhook = \"not-a-url\"\n\n[control_host]", 1), want: "monitoring.wecom_webhook"},
		{name: "missing host address", body: strings.Replace(validManifest, `address = "192.0.2.10"`, `address = ""`, 1), want: "control_host.address"},
		{name: "invalid host name", body: strings.Replace(validManifest, `name = "control"`, `name = "Storage A"`, 1), want: "control_host.name"},
		{name: "missing compile host address", body: validManifest + `
[compile_host]
name = "compile"
address = ""
username = "builder"
password = "password"
`, want: "compile_host.address"},
		{name: "unknown field", body: validManifest + "unexpected = true\n", want: "unknown field"},
		{name: "invalid port", body: strings.Replace(validManifest, "port = 22", "port = 70000", 1), want: "control_host.port"},
		{
			name: "duplicate host name",
			body: validManifest + `
[[other_hosts]]
name = "control"
address = "192.0.2.11"
username = "ubuntu"
password = "password"
`,
			want: "duplicate host name",
		},
		{
			name: "duplicate host address",
			body: validManifest + `
[[other_hosts]]
name = "compute"
address = "192.0.2.10"
username = "ubuntu"
password = "password"
`,
			want: "duplicate host address",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, tt.body, 0o600), root)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), "admin-password")
			assert.NotContains(t, err.Error(), "secret-key")
		})
	}
}

func TestLoadRejectsInsecureFile(t *testing.T) {
	t.Run("wrong mode", func(t *testing.T) {
		root := t.TempDir()
		_, err := Load(writeManifest(t, root, validManifest, 0o644), root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "0600")
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		require.NoError(t, os.WriteFile(target, []byte(validManifest), 0o600))
		path := filepath.Join(root, "custom.toml")
		require.NoError(t, os.Symlink(target, path))
		_, err := Load(path, root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "regular file")
	})

	t.Run("wrong basename", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "other.toml")
		require.NoError(t, os.WriteFile(path, []byte(validManifest), 0o600))
		_, err := Load(path, root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "custom.toml")
	})

	t.Run("outside repository root", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		_, err := Load(writeManifest(t, outside, validManifest, 0o600), root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "repository root")
	})
}

func TestSnapshotDetectsMutation(t *testing.T) {
	root := t.TempDir()
	path := writeManifest(t, root, validManifest, 0o600)
	snapshot, err := Load(path, root)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(path, []byte(validManifest+"\n"), 0o600))
	err = snapshot.VerifyUnchanged()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config_changed")
}

func TestSnapshotDetectsReplacement(t *testing.T) {
	root := t.TempDir()
	path := writeManifest(t, root, validManifest, 0o600)
	snapshot, err := Load(path, root)
	require.NoError(t, err)

	replacement := filepath.Join(root, "replacement")
	require.NoError(t, os.WriteFile(replacement, []byte(validManifest), 0o600))
	require.NoError(t, os.Rename(replacement, path))
	err = snapshot.VerifyUnchanged()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config_changed")
}
