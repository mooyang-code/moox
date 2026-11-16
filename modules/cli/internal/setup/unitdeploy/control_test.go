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
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
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
		"monitor":    "deployment: {enabled: true}\nmetrics: {enabled: true, no_data_intervals: 7}\n",
		"collector":  "database: {path: ./data/collector.db}\nscf_access: {key_id: original-scf-id}\nperiod_readiness: {grace: 7m}\n",
		"factor-mgr": "engine: {lease_ttl: 73s}\n",
	} {
		parent := filepath.Join(directory, id, "config")
		require.NoError(t, os.MkdirAll(parent, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(parent, "app.yaml"), []byte(template), 0o600))
	}
	snapshot := &setupconfig.Snapshot{Manifest: setupconfig.Manifest{HostCatalog: map[string]setupconfig.HostDefinition{"control": {Address: "2001:db8::10"}, "storage1": {Address: "192.0.2.20"}}, Deployments: map[string][]string{"control": {"admin", "eventbus", "monitor", "collector", "factor-mgr"}, "storage1": {"storage-primary", "storage-view"}}, CollectorRetention: setupconfig.CollectorRetention{MaintenanceInterval: "2m", MaxRowsPerPass: 19}}}
	components := []string{"admin", "eventbus", "monitor", "collector", "factor-mgr"}
	rendered, err := controlConfigurations(snapshot, directory, components)
	require.NoError(t, err)
	require.Len(t, rendered, 2, "admin、factor-mgr 的模板原样使用，不再改写")
	require.NotContains(t, rendered, "factor-mgr/config/app.yaml")
	decode := func(name string) map[string]any {
		var value map[string]any
		require.NoError(t, yaml.Unmarshal(rendered[name+"/config/app.yaml"], &value))
		return value
	}
	require.NotContains(t, rendered, "admin/config/app.yaml")
	monitor, collector := decode("monitor"), decode("collector")
	require.Equal(t, map[string]any{"enabled": true}, monitor["deployment"], "Monitor 不再探测组件，部署段不写探测地址")
	require.Equal(t, 7, monitor["metrics"].(map[string]any)["no_data_intervals"])
	require.Equal(t, "config/dataset-health-policy.yaml", monitor["metrics"].(map[string]any)["dataset_health_policy_path"])
	require.Equal(t, "", collector["scf_access"].(map[string]any)["key_id"], "scf-collector 的 key_id 在安装时由主机材料填入，不从旧配置继承")
	require.Equal(t, "7m", collector["period_readiness"].(map[string]any)["grace"])
	require.Equal(t, 19, collector["collector_retention"].(map[string]any)["max_rows_per_pass"])
}

func TestControlEnvironmentCarriesNotificationAndHostPolicy(t *testing.T) {
	components := []string{"admin", "eventbus", "monitor", "collector", "factor-mgr", "strategy"}
	// 没配置告警推送地址也能部署 Monitor，渠道由用户稍后在管理台添加。
	empty, err := unitEnvironment(setupconfig.Manifest{}, "control", components)
	require.NoError(t, err)
	require.Empty(t, empty["monitor"]["MOOX_NOTIFICATION_WEBHOOK_URL"])
	manifest := setupconfig.Manifest{Notification: setupconfig.Notification{ChannelType: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"}}
	env, err := unitEnvironment(manifest, "control", components)
	require.NoError(t, err)
	require.Equal(t, "wecom", env["monitor"]["MOOX_NOTIFICATION_CHANNEL_TYPE"])
	require.Equal(t, manifest.Notification.WebhookURL, env["monitor"]["MOOX_NOTIFICATION_WEBHOOK_URL"])
	require.NotContains(t, env["admin"], "MOOX_NOTIFICATION_WEBHOOK_URL")
	require.Equal(t, hostpolicy.Path(manifest.Paths.Resolved().DeployRoot), env["admin"]["MOOX_HOST_POLICY_FILE"])
	// Collector 的空间名单取自本地库的管理集合，部署不再注入空间环境变量。
	require.NotContains(t, env["collector"], "MOOX_SPACE_ID")
	require.NotContains(t, env["collector"], "MOOX_SPACE_IDS")
	require.NotContains(t, env["monitor"], "MOOX_SPACE_ID")
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

// 发布包从 version 2 起按组件出包，旧部署路径一律拒绝，提示改用新命令。
func TestLegacyPathRefusesComponentBundle(t *testing.T) {
	digest := strings.Repeat("a", 64)
	manifest := &bundle.Manifest{Version: bundle.ManifestVersion, Release: "v9.9.9", Commit: strings.Repeat("a", 40), Components: map[string]bundle.Artifact{
		"host-gateway": {File: "moox-host-gateway_v9.9.9_linux_amd64.tar.gz", SHA256: digest, Size: 7, Binaries: map[string]string{"moox-host-gateway": digest}},
	}}
	_, err := bundleSoftware(manifest, "host", "amd64")
	require.ErrorIs(t, err, errLegacyBundle)
	_, err = bundleSoftware(manifest, "host", "arm64")
	require.ErrorContains(t, err, "target_arch_unsupported")
	state, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(state, 0o700))
	_, err = FetchRelease(state, "r1", manifest)
	require.ErrorIs(t, err, errLegacyBundle)
	require.Equal(t, "/data/moox/staging/abc/moox-unit-host_v9.9.9_linux_amd64.tar.gz", artifactPath("/data/moox", unitpackage.Result{SHA256: "sha256:abc", RemoteFile: "moox-unit-host_v9.9.9_linux_amd64.tar.gz"}))
}

func TestExternalUnpackCreatesPrivateStage(t *testing.T) {
	require.Contains(t, externalUnpackScript, "umask 077", "stage 目录要 0700，后面上传密钥时会检查")
	require.Contains(t, externalUnpackScript, `mkdir -p "$1"`, "软件包在 staging，没有上传步骤替它建目录")
}
