package unitdeploy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/bundle"
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
		"console-proxy": "public: {host: localhost, port: 9527, http3: true}\ntls: {storage_root: ../data/caddy/caddy}\nlifecycle: {drain_timeout: 91s}\n",
		"monitor":       "deployment: {enabled: true, https: {}}\nmetrics: {enabled: true, no_data_intervals: 7}\n",
		"collector":     "database: {path: ./data/collector.db}\nscf_access: {key_id: original-scf-id}\nperiod_readiness: {grace: 7m}\n",
		"factor-mgr":    "engine: {lease_ttl: 73s}\n",
	} {
		parent := filepath.Join(directory, id, "config")
		require.NoError(t, os.MkdirAll(parent, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(parent, "app.yaml"), []byte(template), 0o600))
	}
	snapshot := &setupconfig.Snapshot{Manifest: setupconfig.Manifest{HostCatalog: map[string]setupconfig.HostDefinition{"control": {Address: "2001:db8::10"}, "storage1": {Address: "192.0.2.20"}}, Deployments: map[string][]string{"control": {"admin", "eventbus", "console-proxy", "monitor", "collector", "factor-mgr"}, "storage1": {"storage-primary", "storage-view"}}, CollectorRetention: setupconfig.CollectorRetention{MaintenanceInterval: "2m", MaxRowsPerPass: 19}}}
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
	require.NotContains(t, proxy["tls"], "mode", "Console Proxy 严格解析配置，不能再带 mode")
	require.Equal(t, "91s", proxy["lifecycle"].(map[string]any)["drain_timeout"])
	https := monitor["deployment"].(map[string]any)["https"].(map[string]any)["console-proxy"].(map[string]any)
	require.Equal(t, "https://[2001:db8::10]:9527/", https["url"])
	require.Equal(t, "[2001:db8::10]:9527", https["connect_address"])
	require.Equal(t, "../../console-proxy/data/caddy/internal-ca.sha256", https["ca_baseline"])
	require.Equal(t, 7, monitor["metrics"].(map[string]any)["no_data_intervals"])
	require.Equal(t, "config/dataset-health-policy.yaml", monitor["metrics"].(map[string]any)["dataset_health_policy_path"])
	require.Equal(t, "", collector["scf_access"].(map[string]any)["key_id"], "scf-collector 的 key_id 在安装时由主机材料填入，不从旧配置继承")
	require.Equal(t, "7m", collector["period_readiness"].(map[string]any)["grace"])
	require.Equal(t, 19, collector["collector_retention"].(map[string]any)["max_rows_per_pass"])
}

func TestControlEnvironmentDistributesOnlyRequiredStorageRoleSecrets(t *testing.T) {
	identity := runtimeIdentity{StoragePrimarySecret: strings.Repeat("p", 32), StorageViewSecret: strings.Repeat("v", 32)}
	components := []string{"admin", "eventbus", "console-proxy", "monitor", "collector", "factor-mgr", "strategy"}
	// 没配置告警推送地址也能部署 Monitor，渠道由用户稍后在管理台添加。
	empty, err := unitEnvironment(setupconfig.Manifest{}, identity, "control", components)
	require.NoError(t, err)
	require.Empty(t, empty["monitor"]["MOOX_NOTIFICATION_WEBHOOK_URL"])
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
	for _, id := range []string{"monitor", "collector", "factor-mgr", "strategy"} {
		require.Equal(t, identity.StoragePrimarySecret, env[id]["MOOX_STORAGE_PRIMARY_AUTH_SECRET"])
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
	require.NoError(t, bindControlRequest(root, "", "sha256:original"))
	require.NoError(t, bindControlRequest(root, "", "sha256:original"))
	require.NoError(t, bindControlRequest(root, "", "sha256:changed"), "control 没成功时可以改配置重试")
	require.NoError(t, saveJSON(root, "control-ready.json", map[string]string{"stage": "control-ready"}, true))
	require.Error(t, bindControlRequest(root, "", "sha256:other"), "control 成功后不能换请求")
	require.NoError(t, bindControlRequest(root, "", "sha256:changed"))
}

func TestControlDependenciesReadReadyRecords(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(directory, 0o700))
	root, err := privateRoot(directory)
	require.NoError(t, err)
	defer root.Close()
	snapshot := &setupconfig.Snapshot{Manifest: setupconfig.Manifest{
		HostCatalog: map[string]setupconfig.HostDefinition{"control": {Address: "192.0.2.1"}, "storage": {Address: "192.0.2.2"}},
		Deployments: map[string][]string{"control": {"admin", "monitor"}, "storage": {"storage-primary"}},
	}}
	require.ErrorContains(t, checkControlDependencies(snapshot, root, []string{"monitor"}), "control_dependency_not_ready")
	state := filepath.Join(directory, "units", "storage", "storage")
	require.NoError(t, os.MkdirAll(state, 0o700))
	write := func(stage string) {
		require.NoError(t, os.WriteFile(filepath.Join(state, unitReadyFile), []byte(`{"stage":"`+stage+`"}`+"\n"), 0o600))
	}
	write("storage-paused")
	require.ErrorContains(t, checkControlDependencies(snapshot, root, []string{"monitor"}), "control_dependency_not_ready", "暂停的 Storage 不能当就绪")
	write("storage-ready")
	require.NoError(t, checkControlDependencies(snapshot, root, []string{"monitor"}))
}

func TestLatestUnitConfigurationReadsNewestRecord(t *testing.T) {
	state := t.TempDir()
	base := filepath.Join(state, "units", "storage", "storage")
	require.NoError(t, os.MkdirAll(filepath.Join(base, "releases", "r1"), 0o700))
	_, ok := latestUnitConfiguration(state, "storage", "storage")
	require.False(t, ok)
	first, second := filepath.Join(base, "operation.json"), filepath.Join(base, "releases", "r1", "operation.json")
	require.NoError(t, os.WriteFile(first, []byte(`{"configuration_sha256":"sha256:old"}`), 0o600))
	require.NoError(t, os.WriteFile(second, []byte(`{"configuration_sha256":"sha256:new"}`), 0o600))
	require.NoError(t, os.Chtimes(first, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)))
	digest, ok := latestUnitConfiguration(state, "storage", "storage")
	require.True(t, ok)
	require.Equal(t, "sha256:new", digest)
}

func TestBundleSoftwareRecordsRemoteArtifact(t *testing.T) {
	manifest := &bundle.Manifest{Release: "v9.9.9", Units: map[string]bundle.Artifact{
		"host": {File: "moox-unit-host_v9.9.9_linux_amd64.tar.gz", SHA256: "abc", Size: 7, Manifest: []byte(`{"profile":"host","files":[{"path":"bin/moox-runtime","sha256":"sha256:def"}]}`)},
	}}
	item, err := bundleSoftware(manifest, "host", "amd64")
	require.NoError(t, err)
	require.Empty(t, item.Archive, "软件不在这台电脑上")
	require.Equal(t, "/data/moox/staging/abc/moox-unit-host_v9.9.9_linux_amd64.tar.gz", artifactPath("/data/moox", item))
	digest, err := packageHelperSHA(item)
	require.NoError(t, err)
	require.Equal(t, "def", digest)
	_, err = bundleSoftware(manifest, "host", "arm64")
	require.ErrorContains(t, err, "target_arch_unsupported")
	_, err = bundleSoftware(manifest, "trade", "amd64")
	require.ErrorContains(t, err, "bundle_incomplete")
}

func TestFetchedReleaseCanBeRead(t *testing.T) {
	state, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(state, 0o700))
	unit := func(profile, extra string) bundle.Artifact {
		return bundle.Artifact{File: profile + ".tar.gz", SHA256: "abc", Size: 1, Manifest: []byte(`{"profile":"` + profile + `"` + extra + `}`)}
	}
	manifest := &bundle.Manifest{Release: "v9.9.9", Commit: strings.Repeat("a", 40), Units: map[string]bundle.Artifact{
		"host": unit("host", `,"files":[{"path":"bin/moox-runtime","sha256":"sha256:def"}]`), "control": unit("control", ""),
	}}
	_, err = FetchRelease(state, "r1", manifest)
	require.NoError(t, err)
	root, err := privateRoot(state)
	require.NoError(t, err)
	defer root.Close()
	release, exists, err := readRelease(t.Context(), root, "r1", "amd64")
	require.NoError(t, err, "登记的版本要读得出来，否则 --release 全部不可用")
	require.True(t, exists)
	require.Equal(t, "def", release.HelperSHA256)
}

func TestExternalUnpackCreatesPrivateStage(t *testing.T) {
	require.Contains(t, externalUnpackScript, "umask 077", "stage 目录要 0700，后面上传密钥时会检查")
	require.Contains(t, externalUnpackScript, `mkdir -p "$1"`, "软件包在 staging，没有上传步骤替它建目录")
}
