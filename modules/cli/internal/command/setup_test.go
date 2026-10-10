package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupdeploy "github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/release"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var setupTestSecrets = []string{"admin-test-password", "control-ssh-password", "storage-ssh-password", "AKID-test-secret", "cloud-test-secret"}

// fakeSetupDeployer 记录部署命令的调用。
type fakeSetupDeployer struct {
	deploys          []fakeDeployCall
	scripts          []fakeScriptCall
	rollbacks        []string
	rollbackLockHeld []bool
	bootstrap        *setupdeploy.BootstrapOptions
	plan             release.Plan
}

type fakeDeployCall struct {
	host string
	opts setupdeploy.Options
}

type fakeScriptCall struct {
	host, script string
	lockHeld     bool
	args         []string
}

func (f *fakeSetupDeployer) Bootstrap(_ context.Context, opts setupdeploy.BootstrapOptions) ([]setupdeploy.Result, error) {
	f.bootstrap = &opts
	return []setupdeploy.Result{{Host: "control", Release: "r1", Components: []string{"admin"}, Output: "control-ssh-password"}}, nil
}

func (f *fakeSetupDeployer) Deploy(_ context.Context, hostID string, opts setupdeploy.Options) (setupdeploy.Result, error) {
	f.deploys = append(f.deploys, fakeDeployCall{host: hostID, opts: opts})
	return setupdeploy.Result{Host: hostID, Release: "r1", Components: opts.Components, Output: "installer output"}, nil
}

func (f *fakeSetupDeployer) Rollback(_ context.Context, hostID string, lockHeld bool) (string, error) {
	f.rollbacks = append(f.rollbacks, hostID)
	f.rollbackLockHeld = append(f.rollbackLockHeld, lockHeld)
	return "已切换到发布 r0\n", nil
}

func (f *fakeSetupDeployer) RunScript(_ context.Context, hostID, script string, lockHeld bool, args ...string) (string, error) {
	f.scripts = append(f.scripts, fakeScriptCall{host: hostID, script: script, lockHeld: lockHeld, args: args})
	return script + " ok\n", nil
}

func (f *fakeSetupDeployer) Plan(hostID string) (release.Plan, error) {
	f.plan.HostID = hostID
	return f.plan, nil
}

// setupTestDeps 返回注入了 moox.toml 快照和假部署器的依赖；syncs 记录每次打开部署器时是否同步部署记录。
func setupTestDeps(t *testing.T, snapshot *setupconfig.Snapshot, deployer *fakeSetupDeployer, syncs *[]bool) setupDeps {
	t.Helper()
	return setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		openDeployer: func(_ *setupconfig.Snapshot, _ string, _ io.Writer, sync bool) (setupDeployer, func(), error) {
			if syncs != nil {
				*syncs = append(*syncs, sync)
			}
			return deployer, func() {}, nil
		},
	}
}

func runSetup(t *testing.T, deps setupDeps, args ...string) (string, string, error) {
	t.Helper()
	cmd := newSetupCommand(deps)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestSetupCommandContractAndSecrecy(t *testing.T) {
	snapshot := setupSnapshot(t)
	validateCalls := 0
	deployer := &fakeSetupDeployer{}
	deps := setupTestDeps(t, snapshot, deployer, nil)
	deps.validate = func(context.Context, *setupconfig.Snapshot) (setupvalidate.Result, error) {
		validateCalls++
		return setupvalidate.Result{Checks: []setupvalidate.Check{{Name: "config", Status: "valid"}}}, nil
	}
	deps.trustHost = func(context.Context, *setupconfig.Snapshot, string, string) error { return nil }
	deps.apply = func(context.Context, *setupconfig.Snapshot) (setupclient.ApplyResult, error) {
		return setupclient.ApplyResult{Action: "created", Users: 1, Secrets: 1, Hosts: 2}, nil
	}
	deps.status = func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error) {
		return setupclient.StatusResult{State: "completed", Users: 1, Secrets: 1, Hosts: 2}, nil
	}
	deps.login = func(context.Context, *setupconfig.Snapshot) (setupclient.LoginResult, error) {
		return setupclient.LoginResult{LoginAPI: "valid"}, nil
	}
	for _, test := range []struct {
		name string
		args []string
		key  string
	}{
		{name: "validate", args: []string{"validate"}, key: "checks"},
		{name: "trust", args: []string{"trust-host", "--host", "control", "--fingerprint", "SHA256:test"}, key: "status"},
		{name: "bootstrap", args: []string{"bootstrap"}, key: "hosts"},
		{name: "deploy-host", args: []string{"deploy-host", "--host", "storage"}, key: "release"},
		{name: "apply", args: []string{"apply"}, key: "login_api"},
		{name: "status", args: []string{"status"}, key: "state"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, err := runSetup(t, deps, append(test.args, "--file", "moox.toml")...)
			require.NoError(t, err)
			var result map[string]any
			require.NoError(t, json.Unmarshal([]byte(stdout), &result))
			require.Contains(t, result, test.key)
			for _, secret := range setupTestSecrets {
				require.NotContains(t, stdout+stderr, secret)
			}
		})
	}
	require.Equal(t, 2, validateCalls, "validate 和 apply 都校验完整的 moox.toml")
}

func TestSetupHelpListsWorkflowCommands(t *testing.T) {
	stdout, _, err := runSetup(t, setupDeps{}, "--help")
	require.NoError(t, err)
	for _, name := range []string{
		"init", "hosts", "validate", "trust-host", "trust-browser", "bootstrap", "deploy-host", "deploy-service",
		"rollback", "pause", "resume", "service", "render", "export-state", "build-linux", "apply", "status",
		"export-skill-config", "firewall", "private-network", "scf-network-plan",
	} {
		require.Contains(t, stdout, "  "+name+" ", name)
	}
	for _, removed := range []string{"deploy-control", "deploy-storage", "rebuild-storage", "install-storage-watchdog", "metadata-import", "render-runtime-config", "purge-eventbus-data"} {
		require.NotContains(t, stdout, removed)
	}
}

func TestSetupTrustBrowserSkipsPublicTLS(t *testing.T) {
	snapshot := setupSnapshot(t)
	stdout, _, err := runSetup(t, setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }}, "trust-browser")
	require.NoError(t, err)
	require.JSONEq(t, `{"host":"control","status":"not_required"}`, stdout)
}

func TestSetupTrustBrowserInstallsInternalCA(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "scripts", "deploy", "install-caddy-ca.sh")
	marker := filepath.Join(root, "installed")
	require.NoError(t, os.MkdirAll(filepath.Dir(script), 0o755))
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nset -eu\ncase \" $* \" in\n  *' --check '*) test -f \"$MARKER\";;\n  *) : >\"$MARKER\";;\nesac\n"), 0o700))
	t.Setenv("MARKER", marker)
	t.Setenv("HOME", root)
	t.Chdir(root)
	snapshot := setupSnapshot(t)
	control := snapshot.Manifest.Hosts["control"]
	control.TLSMode = "internal"
	snapshot.Manifest.Hosts["control"] = control
	require.NoError(t, os.MkdirAll(filepath.Dir(setupdeploy.CAPath(control.Address)), 0o700))
	require.NoError(t, os.WriteFile(setupdeploy.CAPath(control.Address), []byte("ca"), 0o600))
	stdout, _, err := runSetup(t, setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }}, "trust-browser")
	require.NoError(t, err)
	require.FileExists(t, marker)
	require.Contains(t, stdout, `"status":"trusted"`)
}

func TestSetupHostsListsSanitizedManifestHosts(t *testing.T) {
	snapshot := setupSnapshot(t)
	snapshot.Manifest.CompileHost = setupconfig.CompileHost{
		Address: "203.0.113.10", SSH: setupconfig.SSH{Port: 2222, Username: "builder", Password: "compile-password"},
	}
	stdout, _, err := runSetup(t, setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }}, "hosts")
	require.NoError(t, err)
	var result struct {
		Hosts []setupHostChoice `json:"hosts"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &result))
	require.Len(t, result.Hosts, 3)
	assert.Equal(t, "control", result.Hosts[0].Name)
	assert.Equal(t, "host", result.Hosts[0].Role)
	assert.Equal(t, "/data/moox/control", result.Hosts[0].Root)
	assert.Equal(t, "storage", result.Hosts[1].Name)
	assert.Equal(t, []string{"storage-primary", "storage-node", "storage-view", "access"}, result.Hosts[1].Components)
	assert.Equal(t, setupHostChoice{Name: "compile", Address: "203.0.113.10", Port: 2222, Username: "builder", Role: "compile"}, result.Hosts[2])
	for _, secret := range append(setupTestSecrets, "compile-password") {
		require.NotContains(t, stdout, secret)
	}
}

func TestSetupTrustTargetIncludesCompileHost(t *testing.T) {
	snapshot := setupSnapshot(t)
	snapshot.Manifest.CompileHost = setupconfig.CompileHost{Address: "203.0.113.10", SSH: setupconfig.SSH{Port: 22, Username: "builder"}}
	target, err := setupTrustTarget(snapshot.Manifest, "compile")
	require.NoError(t, err)
	assert.Equal(t, "203.0.113.10", target.Address)
	target, err = setupTrustTarget(snapshot.Manifest, "storage")
	require.NoError(t, err)
	assert.Equal(t, "203.0.113.9", target.Address)
	_, err = setupTrustTarget(snapshot.Manifest, "missing")
	require.ErrorContains(t, err, "没有主机")
}

func TestSetupDeployHostPassesOptions(t *testing.T) {
	snapshot := setupSnapshot(t)
	deployer := &fakeSetupDeployer{}
	var syncs []bool
	deps := setupTestDeps(t, snapshot, deployer, &syncs)
	_, _, err := runSetup(t, deps, "deploy-host", "--host", "storage", "--components", "storage-view,access", "--skip-build", "--maintenance-lock-held")
	require.NoError(t, err)
	_, _, err = runSetup(t, deps, "deploy-host", "--host", "control", "--reuse-binaries", "--no-start", "--no-sync")
	require.NoError(t, err)
	require.Equal(t, []fakeDeployCall{
		{host: "storage", opts: setupdeploy.Options{Components: []string{"storage-view", "access"}, SkipBuild: true, MaintenanceLockHeld: true}},
		{host: "control", opts: setupdeploy.Options{ReuseBinaries: true, NoStart: true}},
	}, deployer.deploys)
	require.Equal(t, []bool{true, false}, syncs, "默认同步部署记录，--no-sync 时不同步")

	_, _, err = runSetup(t, deps, "deploy-host", "--host", "missing")
	require.ErrorContains(t, err, "没有主机")
	require.Len(t, deployer.deploys, 2)
}

func TestSetupDeployServiceDeploysEveryPlacedHost(t *testing.T) {
	snapshot := setupSnapshot(t)
	deployer := &fakeSetupDeployer{}
	deps := setupTestDeps(t, snapshot, deployer, nil)
	_, _, err := runSetup(t, deps, "deploy-service", "--component", "host-gateway", "--reuse-binaries")
	require.NoError(t, err)
	require.Equal(t, []string{"control", "storage"}, []string{deployer.deploys[0].host, deployer.deploys[1].host}, "主机组件部署在每台主机上")
	require.Equal(t, []string{"host-gateway"}, deployer.deploys[0].opts.Components)

	deployer.deploys = nil
	_, _, err = runSetup(t, deps, "deploy-service", "--component", "access", "--host", "storage")
	require.NoError(t, err)
	require.Equal(t, []fakeDeployCall{{host: "storage", opts: setupdeploy.Options{Components: []string{"access"}}}}, deployer.deploys)

	_, _, err = runSetup(t, deps, "deploy-service", "--component", "access", "--host", "control")
	require.ErrorContains(t, err, "没有部署组件 access")
	_, _, err = runSetup(t, deps, "deploy-service", "--component", "trade")
	require.ErrorContains(t, err, "部署表中没有组件")
}

func TestSetupPauseResumeAndServiceRunHostScripts(t *testing.T) {
	snapshot := setupSnapshot(t)
	deployer := &fakeSetupDeployer{}
	deps := setupTestDeps(t, snapshot, deployer, nil)
	stdout, _, err := runSetup(t, deps, "pause", "--host", "control", "--components", "collector,strategy", "--maintenance-lock-held")
	require.NoError(t, err)
	require.Equal(t, "pause ok\n", stdout)
	_, _, err = runSetup(t, deps, "resume", "--host", "control", "--components", "collector")
	require.NoError(t, err)
	_, _, err = runSetup(t, deps, "service", "restart", "--host", "storage", "--components", "storage-view", "--force")
	require.NoError(t, err)
	_, _, err = runSetup(t, deps, "service", "status")
	require.NoError(t, err)
	_, _, err = runSetup(t, deps, "rollback", "--host", "storage")
	require.NoError(t, err)
	_, _, err = runSetup(t, deps, "rollback", "--host", "control", "--maintenance-lock-held")
	require.NoError(t, err)
	require.Equal(t, []fakeScriptCall{
		{host: "control", script: "pause", lockHeld: true, args: []string{"collector", "strategy"}},
		{host: "control", script: "resume", args: []string{"collector"}},
		{host: "storage", script: "restart", args: []string{"--force", "storage-view"}},
		{host: "control", script: "status"},
	}, deployer.scripts)
	require.Equal(t, []string{"storage", "control"}, deployer.rollbacks)
	require.Equal(t, []bool{false, true}, deployer.rollbackLockHeld)

	_, _, err = runSetup(t, deps, "pause", "--host", "control")
	require.ErrorContains(t, err, "--components")
	_, _, err = runSetup(t, deps, "service", "stop", "--force")
	require.ErrorContains(t, err, "--force")
	_, _, err = runSetup(t, deps, "service", "reload")
	require.ErrorContains(t, err, "不支持的操作")
}

func TestSetupBootstrapUsesCLIKeyFileAndDefersPlacementSync(t *testing.T) {
	t.Setenv("MOOX_CLI_CALLER_KEY_FILE", "/tmp/moox-test-cli.key")
	snapshot := setupSnapshot(t)
	deployer := &fakeSetupDeployer{}
	var syncs []bool
	stdout, _, err := runSetup(t, setupTestDeps(t, snapshot, deployer, &syncs), "bootstrap", "--skip-build")
	require.NoError(t, err)
	require.NotNil(t, deployer.bootstrap)
	assert.True(t, deployer.bootstrap.SkipBuild)
	assert.Equal(t, "/tmp/moox-test-cli.key", deployer.bootstrap.CLIKeyFile)
	assert.NotNil(t, deployer.bootstrap.NewPlacements)
	assert.Equal(t, []bool{false}, syncs, "control 就绪前不能经 Admin 同步部署记录")
	assert.NotContains(t, stdout, "control-ssh-password", "安装器输出不进命令结果")
	assert.False(t, deployer.bootstrap.ControlOnly)
	assert.False(t, deployer.bootstrap.MaintenanceLockHeld)

	_, _, err = runSetup(t, setupTestDeps(t, snapshot, deployer, nil), "bootstrap", "--control-only", "--maintenance-lock-held")
	require.NoError(t, err)
	assert.True(t, deployer.bootstrap.ControlOnly)
	assert.True(t, deployer.bootstrap.MaintenanceLockHeld)
}

func TestSetupRenderWritesPlanFiles(t *testing.T) {
	snapshot := setupSnapshot(t)
	deployer := &fakeSetupDeployer{plan: release.Plan{Root: "/data/moox/storage", Files: []release.File{
		{Path: "runtime/host.env", Mode: 0o644, Data: []byte("HOST_ID=storage\n")},
		{Path: "storage-view/config/trpc_go.yaml", Mode: 0o644, Data: []byte("server: {}\n")},
	}}}
	out := filepath.Join(t.TempDir(), "rendered")
	stdout, _, err := runSetup(t, setupTestDeps(t, snapshot, deployer, nil), "render", "--host", "storage", "--out", out)
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(out, "runtime", "host.env"))
	require.NoError(t, err)
	require.Equal(t, "HOST_ID=storage\n", string(raw))
	require.FileExists(t, filepath.Join(out, "storage-view", "config", "trpc_go.yaml"))
	require.Contains(t, stdout, `"files":2`)

	_, _, err = runSetup(t, setupTestDeps(t, snapshot, deployer, nil), "render", "--host", "storage", "--out", out)
	require.ErrorContains(t, err, "不为空")
}

func TestSetupFirewallRulesFollowPlacements(t *testing.T) {
	snapshot := setupSnapshot(t)
	addresses := map[string]string{"control": "203.0.113.8", "storage": "203.0.113.9"}
	rules := setupFirewallRules(snapshot.Manifest, addresses)
	has := func(host, port, source string) bool {
		return containsFirewallEntry(rules[host], setupFirewallEntry{Port: port, Source: source})
	}
	assert.True(t, has("control", "9527", "0.0.0.0/0"), "控制台对公网开放")
	assert.True(t, has("control", "80", "0.0.0.0/0"), "公网证书需要 80 端口完成验证")
	assert.True(t, has("control", "4333", "0.0.0.0/0"), "消息总线端口保持对公网开放（SCF 发布事件）")
	assert.True(t, has("control", "11003", "203.0.113.9"))
	assert.False(t, has("control", "11003", "0.0.0.0/0"), "跨主机入口不对公网开放")
	assert.False(t, has("control", "11012", "203.0.113.8"), "control 自己的健康端口不用开放")
	assert.True(t, has("storage", "11003", "203.0.113.8"))
	assert.True(t, has("storage", "11004", "0.0.0.0/0"), "外部接入对公网开放")
	assert.True(t, has("storage", "11012", "203.0.113.8"), "主机网关的健康端口只对 control 开放")
	assert.True(t, has("storage", "20210", "203.0.113.8"), "存储主服务的健康端口只对 control 开放")
	assert.False(t, has("storage", "9527", "0.0.0.0/0"))

	control := snapshot.Manifest.Hosts["control"]
	control.TLSMode = "internal"
	snapshot.Manifest.Hosts["control"] = control
	rules = setupFirewallRules(snapshot.Manifest, addresses)
	assert.False(t, has("control", "80", "0.0.0.0/0"), "内置 CA 不需要 80 端口")
}

func TestDiffFirewallEntriesPrunesOnlyManagedPorts(t *testing.T) {
	snapshot := setupSnapshot(t)
	managed := setupFirewallManagedPorts(snapshot.Manifest)
	desired := []setupFirewallEntry{{Port: "11003", Source: "203.0.113.8"}, {Port: "11004", Source: "0.0.0.0/0"}}
	existing := []setupFirewallEntry{
		{Port: "11003", Source: "0.0.0.0/0"},
		{Port: "11003", Source: "203.0.113.8/32"},
		{Port: "12004", Source: "0.0.0.0/0"},
		{Port: "11001", Source: "0.0.0.0/0"},
		{Port: "22", Source: "0.0.0.0/0"},
		{Port: "8080-9000", Source: "0.0.0.0/0"},
	}
	missing, obsolete := diffFirewallEntries(desired, existing, managed)
	assert.Equal(t, []setupFirewallEntry{{Port: "11004", Source: "0.0.0.0/0"}}, missing, "203.0.113.8/32 与 203.0.113.8 是同一条规则")
	assert.Equal(t, []setupFirewallEntry{
		{Port: "11003", Source: "0.0.0.0/0"}, {Port: "12004", Source: "0.0.0.0/0"}, {Port: "11001", Source: "0.0.0.0/0"},
	}, obsolete, "SSH 和端口范围不受管")
}

func TestIsPublicFirewallIPRejectsPrivateAndLoopbackAddresses(t *testing.T) {
	assert.True(t, isPublicFirewallIP("106.53.107.122"))
	for _, address := range []string{"10.0.0.1", "127.0.0.1", "192.168.1.2", "::1", "not-an-ip"} {
		assert.False(t, isPublicFirewallIP(address), address)
	}
}

func setupSnapshot(t *testing.T) *setupconfig.Snapshot {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "moox.toml")
	raw := []byte(`[admin]
username = "admin"
password = "admin-test-password"
[tencent_cloud]
secret_id = "AKID-test-secret"
secret_key = "cloud-test-secret"
[eventbus]
port = 4333
tls_enabled = true
[notification]
channel_type = "wecom"
webhook_url = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"
[hosts.control]
address = "203.0.113.8"
provider = "tencent"
ssh = { username = "ubuntu", password = "control-ssh-password" }
[hosts.storage]
address = "203.0.113.9"
region = "ap-nanjing"
provider = "tencent"
ssh = { username = "ubuntu", password = "storage-ssh-password" }
[placements]
control = ["console-proxy", "web-host", "admin", "eventbus", "monitor", "collector"]
storage = ["storage-primary", "storage-node", "storage-view", "access"]
`)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	snapshot, err := setupconfig.Load(path, dir)
	require.NoError(t, err)
	return snapshot
}

func TestSetupSnapshotFixtureIsValid(t *testing.T) {
	snapshot := setupSnapshot(t)
	require.Equal(t, []string{"control", "storage"}, snapshot.Manifest.HostIDs())
	require.True(t, strings.HasPrefix(snapshot.Manifest.EventBusURL(), "tls://203.0.113.8:"))
}
