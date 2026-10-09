package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/release"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/stretchr/testify/require"
)

// fakeConfigHost 把主机上的文件放在内存里。
type fakeConfigHost struct {
	files map[string][]byte
}

func (h *fakeConfigHost) Run(_ context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	if !strings.Contains(argv[2], "exit 3") {
		return setupssh.Result{ExitCode: 1, Stderr: "unexpected script"}, errors.New("unexpected script")
	}
	raw, ok := h.files[argv[4]]
	if !ok {
		return setupssh.Result{ExitCode: 3}, errors.New("ssh_command_failed: Process exited with status 3")
	}
	return setupssh.Result{Stdout: string(raw)}, nil
}

func (h *fakeConfigHost) Close() error { return nil }

const configTestManifest = `[admin]
username = "admin"
password = "admin-test-password"
[tencent_cloud]
secret_id = "AKID-test-secret"
secret_key = "cloud-test-secret"
[notification]
channel_type = "wecom"
webhook_url = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"
[eventbus]
tls_enabled = true
[hosts.control]
address = "203.0.113.8"
ssh = { username = "ubuntu", password = "control-ssh-password" }
[hosts.storage]
address = "203.0.113.20"
region = "ap-nanjing"
ssh = { username = "ubuntu", password = "storage-ssh-password" }
[placements]
control = ["console-proxy", "web-host", "admin", "eventbus", "monitor", "collector"]
storage = ["storage-primary", "storage-node", "storage-view", "access"]
`

const collectorTestConfig = "server:\n  name: collector\n"

// configTestEnv 是 config 命令测试的假主机、假渲染和假部署：部署把渲染结果写进主机当前发布。
type configTestEnv struct {
	hosts      map[string]*fakeConfigHost
	deploys    []string
	failDeploy bool
}

func fakeRenderedPlan(manifest setupconfig.Manifest, hostID string) (release.Plan, error) {
	plan := release.Plan{HostID: hostID}
	if manifest.HasComponent(hostID, "storage-primary") {
		policy, err := manifest.StoragePolicy().Encode()
		if err != nil {
			return plan, err
		}
		for _, component := range []string{"storage-primary", "storage-view"} {
			plan.Files = append(plan.Files, release.File{Path: component + "/config/storage-policy.json", Data: policy})
		}
	}
	if manifest.HasComponent(hostID, "collector") {
		rendered, err := setupconfig.RenderCollectorRuntimeConfig(&setupconfig.Snapshot{Manifest: manifest}, []byte(collectorTestConfig))
		if err != nil {
			return plan, err
		}
		plan.Files = append(plan.Files, release.File{Path: "collector/config/app.yaml", Data: rendered})
	}
	return plan, nil
}

// deployedPolicy 是主机上当前的存储策略：保留期与默认值相同，只是视图根数不同。
func deployedPolicy(t *testing.T) []byte {
	t.Helper()
	policy := storagepolicy.Default()
	policy.View.Bars = 4000
	policy.View.TrimBars = 4800
	raw, err := policy.Encode()
	require.NoError(t, err)
	return raw
}

func newConfigTestDeps(t *testing.T) (configDeps, *configTestEnv, string) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "moox.toml")
	require.NoError(t, os.WriteFile(file, []byte(configTestManifest), 0o600))
	env := &configTestEnv{hosts: map[string]*fakeConfigHost{
		"control": {files: map[string][]byte{"/data/moox/control/current/collector/config/app.yaml": []byte(collectorTestConfig)}},
		"storage": {files: map[string][]byte{
			"/data/moox/storage/current/storage-primary/config/storage-policy.json": deployedPolicy(t),
			"/data/moox/storage/current/storage-view/config/storage-policy.json":    deployedPolicy(t),
		}},
	}}
	return configDeps{
		load:   func(path string) (*setupconfig.Snapshot, error) { return setupconfig.Load(path, dir) },
		dial:   func(_ context.Context, host setupconfig.Host) (configHost, error) { return env.hosts[host.ID], nil },
		render: fakeRenderedPlan,
		deploy: func(_ context.Context, snapshot *setupconfig.Snapshot, _ string, hostID string, components []string, _ io.Writer) error {
			if env.failDeploy {
				return errors.New("启动失败，已切回原发布")
			}
			env.deploys = append(env.deploys, hostID+":"+strings.Join(components, ","))
			plan, err := fakeRenderedPlan(snapshot.Manifest, hostID)
			if err != nil {
				return err
			}
			host, _ := snapshot.Manifest.Host(hostID)
			for _, file := range plan.Files {
				env.hosts[hostID].files[host.Root+"/current/"+file.Path] = file.Data
			}
			return nil
		},
	}, env, file
}

func configCommand(t *testing.T, deps configDeps, file string, args ...string) (map[string]any, error) {
	t.Helper()
	cmd := newConfigCommand(deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append(args, "--file", file))
	err := cmd.ExecuteContext(context.Background())
	var result map[string]any
	if out.Len() > 0 {
		require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	}
	return result, err
}

func targetPlan(t *testing.T, result map[string]any, id string) map[string]any {
	t.Helper()
	for _, item := range result["targets"].([]any) {
		target := item.(map[string]any)
		if target["id"] == id {
			return target
		}
	}
	t.Fatalf("target %s missing in %v", id, result)
	return nil
}

func TestConfigPlanComparesCurrentReleaseWithoutChangingHosts(t *testing.T) {
	deps, env, file := newConfigTestDeps(t)
	result, err := configCommand(t, deps, file, "plan")
	require.NoError(t, err)
	policy := targetPlan(t, result, "storage-policy")
	require.Equal(t, configStatusChanged, policy["status"])
	require.Equal(t, "storage", policy["host"])
	require.Equal(t, []any{"storage-primary", "storage-view"}, policy["redeploy"])
	collector := targetPlan(t, result, "collector-runtime")
	require.Equal(t, configStatusChanged, collector["status"])
	require.Contains(t, collector["changed_keys"], "collector_retention.max_rows_per_pass")
	require.Empty(t, env.deploys, "plan 不部署")
	require.Equal(t, collectorTestConfig, string(env.hosts["control"].files["/data/moox/control/current/collector/config/app.yaml"]))
	require.NotContains(t, configJSON(t, result), "password")
}

func TestConfigPublishRedeploysChangedTargetsAndIsIdempotent(t *testing.T) {
	deps, env, file := newConfigTestDeps(t)
	_, err := configCommand(t, deps, file, "publish")
	require.ErrorContains(t, err, "--yes")

	result, err := configCommand(t, deps, file, "publish", "--yes")
	require.NoError(t, err)
	require.Equal(t, []any{"storage-policy@storage", "collector-runtime@control"}, result["published"])
	require.Equal(t, []string{"storage:storage-primary,storage-view", "control:collector"}, env.deploys)
	written, err := storagepolicy.Parse(env.hosts["storage"].files["/data/moox/storage/current/storage-primary/config/storage-policy.json"])
	require.NoError(t, err)
	require.Equal(t, storagepolicy.Default().Retention, written.Retention)

	again, err := configCommand(t, deps, file, "publish", "--yes")
	require.NoError(t, err)
	require.EqualValues(t, 0, again["changed"])
	require.Empty(t, again["published"])
	require.Len(t, env.deploys, 2, "没有变化时不重新部署")
}

func TestConfigPublishRequiresConsentToShortenRetention(t *testing.T) {
	deps, env, file := newConfigTestDeps(t)
	_, err := configCommand(t, deps, file, "publish", "--yes", "--only", "storage-policy")
	require.NoError(t, err)

	shorter := strings.Replace(configTestManifest, "[hosts.control]", "[storage_retention.spaces.crypto]\n\"1m\" = \"5d\"\n\n[hosts.control]", 1)
	require.NoError(t, os.WriteFile(file, []byte(shorter), 0o600))
	plan, err := configCommand(t, deps, file, "plan", "--only", "storage-policy")
	require.NoError(t, err)
	require.Contains(t, targetPlan(t, plan, "storage-policy")["retention_shrinks"], "crypto 1m: 168h -> 120h")

	_, err = configCommand(t, deps, file, "publish", "--yes", "--only", "storage-policy")
	require.ErrorContains(t, err, "--allow-retention-shrink")
	require.Len(t, env.deploys, 1, "拒绝缩短保留期时不部署")

	_, err = configCommand(t, deps, file, "publish", "--yes", "--only", "storage-policy", "--allow-retention-shrink")
	require.NoError(t, err)
	current, err := storagepolicy.Parse(env.hosts["storage"].files["/data/moox/storage/current/storage-primary/config/storage-policy.json"])
	require.NoError(t, err)
	require.Equal(t, "120h", current.Retention.Spaces["crypto"]["1m"])
}

func TestConfigPublishStopsAtTheFirstFailedTarget(t *testing.T) {
	deps, env, file := newConfigTestDeps(t)
	env.failDeploy = true
	result, err := configCommand(t, deps, file, "publish", "--yes")
	require.ErrorContains(t, err, "storage-policy: 重新部署主机 storage")
	require.Empty(t, result["published"])
	require.Equal(t, collectorTestConfig, string(env.hosts["control"].files["/data/moox/control/current/collector/config/app.yaml"]), "后面的目标不发布")
}

func TestConfigSkipsComponentsNotDeployedOnTheHost(t *testing.T) {
	deps, env, file := newConfigTestDeps(t)
	delete(env.hosts["control"].files, "/data/moox/control/current/collector/config/app.yaml")
	result, err := configCommand(t, deps, file, "publish", "--yes")
	require.NoError(t, err)
	require.Equal(t, configStatusNotDeployed, targetPlan(t, result, "collector-runtime")["status"])
	require.Equal(t, []any{"storage-policy@storage"}, result["published"])
	require.Equal(t, []string{"storage:storage-primary,storage-view"}, env.deploys)
}

func TestConfigRejectsUnknownTarget(t *testing.T) {
	deps, _, file := newConfigTestDeps(t)
	_, err := configCommand(t, deps, file, "plan", "--only", "trade-dns")
	require.ErrorContains(t, err, "未知的配置目标")
}

func TestRetentionShrinksComparesEffectiveRetention(t *testing.T) {
	base := storagepolicy.Default()
	current, err := base.Encode()
	require.NoError(t, err)
	longer := storagepolicy.Default()
	longer.Retention.Spaces = map[string]map[string]string{"crypto": {"1m": "30d"}}
	for spaceID, overrides := range storagepolicy.Default().Retention.Spaces {
		longer.Retention.Spaces[spaceID] = overrides
	}
	rendered, err := longer.Encode()
	require.NoError(t, err)
	require.Empty(t, retentionShrinks(current, rendered), "a longer retention is not a shrink")

	finite := storagepolicy.Default()
	finite.Retention.Defaults = map[string]string{}
	for freq, period := range storagepolicy.Default().Retention.Defaults {
		finite.Retention.Defaults[freq] = period
	}
	finite.Retention.Defaults["1d"] = "3650d"
	rendered, err = finite.Encode()
	require.NoError(t, err)
	require.Contains(t, retentionShrinks(current, rendered), "defaults 1d: forever -> 87600h")
}

func configJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}
