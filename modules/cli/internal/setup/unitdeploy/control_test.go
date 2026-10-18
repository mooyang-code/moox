package unitdeploy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type controlTransport struct {
	setupssh.Client
	run func([]string) (setupssh.Result, error)
}

func (c controlTransport) Run(_ context.Context, args []string, _ io.Reader) (setupssh.Result, error) {
	return c.run(args)
}

func TestControlConfigurationsPreserveBusinessSettingsAndBindActualEndpoints(t *testing.T) {
	directory := t.TempDir()
	for id, template := range map[string]string{
		"console-proxy": "public: {host: localhost, port: 9527, http3: true}\ntls: {mode: internal, storage_root: ../data/caddy/caddy}\nlifecycle: {drain_timeout: 91s}\n",
		"monitor":       "placement: {enabled: true, https: {}}\nmetrics: {enabled: true, no_data_intervals: 7}\n",
		"collector":     "database: {path: ./data/collector.db}\nstorage: {result_data_node_id: obsolete-node}\nscf_access: {key_id: original-scf-id}\nperiod_readiness: {grace: 7m}\n",
		"factor-mgr":    "engine: {lease_ttl: 73s}\n",
	} {
		parent := filepath.Join(directory, id, "config")
		require.NoError(t, os.MkdirAll(parent, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(parent, "app.yaml"), []byte(template), 0o600))
	}
	snapshot := &setupconfig.Snapshot{Manifest: setupconfig.Manifest{HostCatalog: map[string]setupconfig.HostDefinition{"control": {Address: "2001:db8::10", TLSMode: "internal"}, "storage1": {Address: "192.0.2.20"}}, Placements: map[string][]string{"control": {"admin", "eventbus", "console-proxy", "monitor", "collector", "factor-mgr"}, "storage1": {"storage-primary", "storage-node", "storage-view"}}, CollectorRetention: setupconfig.CollectorRetention{MaintenanceInterval: "2m", MaxRowsPerPass: 19}}}
	components := []string{"admin", "eventbus", "console-proxy", "monitor", "collector", "factor-mgr"}
	rendered, err := controlConfigurations(snapshot, directory, components)
	require.NoError(t, err)
	require.Len(t, rendered, 3, "factor-mgr 的模板原样使用，不再改写")
	require.NotContains(t, rendered, "factor-mgr/config/app.yaml")
	decode := func(name string) map[string]any {
		var value map[string]any
		require.NoError(t, yaml.Unmarshal(rendered[name+"/config/app.yaml"], &value))
		return value
	}
	proxy, monitor, collector := decode("console-proxy"), decode("monitor"), decode("collector")
	require.Equal(t, "2001:db8::10", proxy["public"].(map[string]any)["host"])
	require.Equal(t, "91s", proxy["lifecycle"].(map[string]any)["drain_timeout"])
	https := monitor["placement"].(map[string]any)["https"].(map[string]any)["console-proxy"].(map[string]any)
	require.Equal(t, "https://[2001:db8::10]:9527/", https["url"])
	require.Equal(t, "[2001:db8::10]:9527", https["connect_address"])
	require.Equal(t, "../../console-proxy/data/caddy/internal-ca.sha256", https["ca_baseline"])
	require.Equal(t, 7, monitor["metrics"].(map[string]any)["no_data_intervals"])
	require.Equal(t, "config/dataset-health-policy.yaml", monitor["metrics"].(map[string]any)["dataset_health_policy_path"])
	require.Equal(t, "storage-node-0", collector["storage"].(map[string]any)["result_data_node_id"])
	require.Equal(t, "original-scf-id", collector["scf_access"].(map[string]any)["key_id"])
	require.Equal(t, "7m", collector["period_readiness"].(map[string]any)["grace"])
	require.Equal(t, 19, collector["collector_retention"].(map[string]any)["max_rows_per_pass"])
	definition := snapshot.Manifest.HostCatalog["control"]
	definition.TLSMode = "public"
	snapshot.Manifest.HostCatalog["control"] = definition
	rendered, err = controlConfigurations(snapshot, directory, components)
	require.NoError(t, err)
	https = decode("monitor")["placement"].(map[string]any)["https"].(map[string]any)["console-proxy"].(map[string]any)
	require.Equal(t, "public", https["trust_mode"])
	require.NotContains(t, https, "ca_file")
	require.NotContains(t, https, "ca_baseline")
}

func TestControlEnvironmentDistributesOnlyRequiredStorageRoleSecrets(t *testing.T) {
	identity := runtimeIdentity{StoragePrimarySecret: strings.Repeat("p", 32), StorageViewSecret: strings.Repeat("v", 32), StorageNodeSecret: strings.Repeat("n", 32)}
	components := []string{"admin", "eventbus", "console-proxy", "monitor", "collector", "factor-mgr", "strategy"}
	// 部署 Monitor 前告警推送地址必须已配置，否则监控只展示不推送。
	_, err := unitEnvironment(setupconfig.Manifest{}, identity, "control", components)
	require.ErrorContains(t, err, "notification.webhook_url is required")
	manifest := setupconfig.Manifest{Notification: setupconfig.Notification{ChannelType: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"}}
	env, err := unitEnvironment(manifest, identity, "control", components)
	require.NoError(t, err)
	require.Equal(t, "wecom", env["monitor"]["MOOX_NOTIFICATION_CHANNEL_TYPE"])
	require.Equal(t, manifest.Notification.WebhookURL, env["monitor"]["MOOX_NOTIFICATION_WEBHOOK_URL"])
	require.NotContains(t, env["admin"], "MOOX_NOTIFICATION_WEBHOOK_URL")
	require.Equal(t, hostpolicy.Path(manifest.Paths.Resolved().DeployRoot), env["admin"]["MOOX_HOST_POLICY_FILE"])
	for _, id := range []string{"eventbus", "console-proxy"} {
		require.NotContains(t, env[id], "MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	}
	// Admin 的 Console 代浏览器调用 Storage，需要 Primary 与 View 两个角色密钥。
	require.Equal(t, identity.StoragePrimarySecret, env["admin"]["MOOX_STORAGE_PRIMARY_AUTH_SECRET"])
	require.Equal(t, identity.StorageViewSecret, env["admin"]["MOOX_STORAGE_VIEW_AUTH_SECRET"])
	require.NotContains(t, env["admin"], "MOOX_STORAGE_NODE_AUTH_SECRET")
	for _, id := range []string{"monitor", "collector", "factor-mgr", "strategy"} {
		require.Equal(t, identity.StoragePrimarySecret, env[id]["MOOX_STORAGE_PRIMARY_AUTH_SECRET"])
		require.NotContains(t, env[id], "MOOX_STORAGE_NODE_AUTH_SECRET")
	}
	require.Equal(t, "crypto", env["collector"]["MOOX_SPACE_ID"])
	require.Equal(t, "crypto", env["collector"]["MOOX_SPACE_IDS"])
	require.NotContains(t, env["monitor"], "MOOX_SPACE_ID")
	require.Equal(t, identity.StorageViewSecret, env["strategy"]["MOOX_STORAGE_VIEW_AUTH_SECRET"])
	require.NotContains(t, env["monitor"], "MOOX_STORAGE_VIEW_AUTH_SECRET")
}

func TestControlPreflightRejectsUnreadyAndUnrelatedRuntime(t *testing.T) {
	transport := controlTransport{}
	for _, output := range []string{`{"host_id":"unrelated","operation":"status","components":[]}`, `{"host_id":"control","operation":"status","components":[{"id":"monitor","state":"running","pid":42,"ready":false}]}`, `{"host_id":"control","operation":"status","components":[{"id":"unrelated","state":"running","pid":42,"ready":true}]}`} {
		transport.run = func([]string) (setupssh.Result, error) { return setupssh.Result{Stdout: output}, nil }
		_, err := activeControlStatus(t.Context(), transport, "/runtime", "/unit", "control", []string{"monitor"})
		require.Error(t, err)
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(directory, 0o700))
	root, err := privateRoot(directory)
	require.NoError(t, err)
	defer root.Close()
	require.NoError(t, bindControlRequest(root, "sha256:original"))
	require.NoError(t, bindControlRequest(root, "sha256:original"))
	require.Error(t, bindControlRequest(root, "sha256:changed"))
}
