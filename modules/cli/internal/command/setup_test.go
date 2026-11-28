package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupCommandContractAndSecrecy(t *testing.T) {
	t.Parallel()
	snapshot := testSetupSnapshot(t)
	secrets := []string{"admin-test-password", "AKID-test-secret", "cloud-test-secret", "webhook/send?key=test"}
	validateCalls := 0
	deps := setupDeps{
		load: func(string) (*setupSnapshot, error) { return snapshot, nil },
		validate: func(context.Context, *setupSnapshot) (setupvalidate.Result, error) {
			validateCalls++
			return setupvalidate.Result{Checks: []setupvalidate.Check{{Name: "config", Status: "valid"}}}, nil
		},
		trustHost: func(context.Context, *setupSnapshot, string, string) error { return nil },
		scanDeployment: func(context.Context, *setupSnapshot, deployscan.Options) ([]deployscan.HostReport, error) {
			return []deployscan.HostReport{
				{Host: "control", Installed: true, Version: json.RawMessage(`{"version":"v0.0.2","components":{"admin":{}}}`), Units: []string{"moox-admin.service"}, Active: []string{"moox-admin.service"}, Linger: true, Journald: true, DiskFreeKB: 50 << 20},
				{Host: "compute", Linger: false, Journald: false, DiskFreeKB: 1 << 20},
			}, nil
		},
	}

	tests := []struct {
		name string
		args []string
		key  string
	}{
		{name: "validate", args: []string{"validate", "--file", "moox.toml"}, key: "checks"},
		{name: "trust", args: []string{"trust-host", "--file", "moox.toml", "--host", "control", "--fingerprint", "SHA256:test"}, key: "status"},
		{name: "status", args: []string{"status", "--file", "moox.toml"}, key: "hosts"},
	}
	outputs := map[string]string{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := newSetupCommand(deps)
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs(test.args)
			require.NoError(t, cmd.Execute())
			var result map[string]any
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
			require.Contains(t, result, test.key)
			combined := stdout.String() + stderr.String()
			for _, secret := range secrets {
				require.NotContains(t, combined, secret)
			}
			outputs[test.name] = stdout.String()
		})
	}
	require.Equal(t, 1, validateCalls, "validate must validate the full manifest")

	// validate：有 version.json 但控制机无完成标记时是 incomplete；没开 linger 的主机给出要用户执行的命令，journald 没持久化、磁盘不够只提醒。
	var validated setupValidateOutput
	require.NoError(t, json.Unmarshal([]byte(outputs["validate"]), &validated))
	require.Equal(t, deployscan.RouteIncomplete, validated.Deployment.Route)
	require.Len(t, validated.Deployment.ManualSteps, 1)
	require.Contains(t, validated.Deployment.ManualSteps[0], "sudo loginctl enable-linger ubuntu")
	require.Len(t, validated.Deployment.Warnings, 2)
	require.NotEmpty(t, validated.Deployment.NextSteps)
	// 装过时让用户在保留数据与全部覆盖之间选（外加单独卸载）；按组件报告是否已部署，没装过的主机单独提醒。
	require.Equal(t, []string{setupModeKeep, setupModeWipe, setupModeUninstall}, validated.Deployment.Modes)
	require.NotNil(t, validated.Deployment.DataFormat)
	require.True(t, validated.Deployment.DataFormat.CanKeep)
	require.Len(t, validated.Deployment.Components, 2)
	require.Contains(t, validated.Deployment.Components[0].Components, deploy.ComponentStatus{ID: "admin", State: "active", Installed: true})
	require.Contains(t, validated.Deployment.Components[0].Components, deploy.ComponentStatus{ID: "collector", State: "missing"})
	require.Len(t, validated.Deployment.Notes, 2)
	require.Contains(t, validated.Deployment.Notes[0], "主机 control 装过，但缺这些组件：", "装过的主机缺组件时提醒：可能是上次安装没做完")
	require.Contains(t, validated.Deployment.Notes[1], "主机 compute 还没装过")
	require.Contains(t, strings.Join(validated.Deployment.NextSteps, "\n"), "setup install")
	require.Contains(t, strings.Join(validated.Deployment.NextSteps, "\n"), "setup wipe")
	require.NotContains(t, strings.Join(validated.Deployment.NextSteps, "\n"), "setup update")

	// 控制机有完成标记时是 installed。
	installedDeps := setupDeps{load: deps.load, validate: deps.validate, scanDeployment: func(context.Context, *setupSnapshot, deployscan.Options) ([]deployscan.HostReport, error) {
		return []deployscan.HostReport{
			{Host: "control", Installed: true, InstallComplete: true, Version: json.RawMessage(`{"version":"v0.0.2","components":{"admin":{}}}`), Units: []string{"moox-admin.service"}, Active: []string{"moox-admin.service"}, Linger: true, Journald: true, DiskFreeKB: 50 << 20},
			{Host: "compute", Installed: true, Version: json.RawMessage(`{"version":"v0.0.2"}`), Linger: true, Journald: true, DiskFreeKB: 50 << 20},
		}, nil
	}}
	installedCmd := newSetupCommand(installedDeps)
	var installedOut bytes.Buffer
	installedCmd.SetOut(&installedOut)
	installedCmd.SetErr(&bytes.Buffer{})
	installedCmd.SetArgs([]string{"validate", "--file", "moox.toml"})
	require.NoError(t, installedCmd.Execute())
	var installedValidated setupValidateOutput
	require.NoError(t, json.Unmarshal(installedOut.Bytes(), &installedValidated))
	require.Equal(t, deployscan.RouteInstalled, installedValidated.Deployment.Route)
	require.Equal(t, []string{setupModeKeep, setupModeWipe, setupModeUninstall}, installedValidated.Deployment.Modes)

	// 没装过时只有新安装；没有 version.json 的残留单独提醒。
	fresh := setupDeps{load: deps.load, validate: deps.validate, scanDeployment: func(context.Context, *setupSnapshot, deployscan.Options) ([]deployscan.HostReport, error) {
		return []deployscan.HostReport{{Host: "control", Entries: []string{"bin"}, Linger: true, Journald: true}, {Host: "compute", Linger: true, Journald: true}}, nil
	}}
	cmd := newSetupCommand(fresh)
	var freshOut bytes.Buffer
	cmd.SetOut(&freshOut)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"validate", "--file", "moox.toml"})
	require.NoError(t, cmd.Execute())
	var freshValidated setupValidateOutput
	require.NoError(t, json.Unmarshal(freshOut.Bytes(), &freshValidated))
	require.Equal(t, []string{setupModeInstall}, freshValidated.Deployment.Modes)
	require.Len(t, freshValidated.Deployment.Notes, 1)
	require.Contains(t, freshValidated.Deployment.Notes[0], "主机 control 没有 version.json")

	// status：读 version.json 与各组件的用户服务状态。
	var status setupStatusOutput
	require.NoError(t, json.Unmarshal([]byte(outputs["status"]), &status))
	require.Equal(t, deployscan.RouteIncomplete, status.Route)
	require.Equal(t, "v0.0.2", status.Hosts[0].Version)
	require.Contains(t, status.Hosts[0].Components, deploy.ComponentStatus{ID: "admin", State: "active", Installed: true})
	require.Contains(t, status.Hosts[0].Components, deploy.ComponentStatus{ID: "collector", State: "missing"})
	require.False(t, status.Hosts[1].Installed)
}

func TestSetupHelpListsWorkflowCommands(t *testing.T) {
	t.Parallel()
	cmd := newSetupCommand(setupDeps{})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	for _, name := range []string{"install", "backup", "operator", "deploy", "init", "start", "verify", "validate", "status", "logs", "restart", "stop", "start-component", "wipe", "hosts", "trust-host", "trust-browser", "firewall"} {
		require.Contains(t, output.String(), "  "+name+" ")
	}
	// 已删除或移出的命令：setup update 已合并进 setup install；setup build-linux 已删除；
	// 云运维命令 inspect-scf/attach-scf-vpc/invoke-scf 迁至 collector function，reboot-host/host-diagnostics 迁至 host；
	// 初始秘密由 setup init 写入，因子任务由 setup start 与 factor task apply 导入，运行配置由部署渲染；
	// 旧部署路径的命令（bootstrap、deploy-host、deploy-unit、release、watchdog、package）及 export-skill-config 已从注册表摘掉。
	for _, removed := range []string{"update ", "apply ", "factors ", "render-runtime-config", "bootstrap", "deploy-host", "deploy-unit", "release ", "watchdog", "package ", "metadata-import", "export-skill-config", "build-linux", "inspect-scf", "attach-scf-vpc", "invoke-scf", "reboot-host", "host-diagnostics"} {
		require.NotContains(t, output.String(), "  "+removed)
	}
}

func TestRelocatedCloudOpsCommandsAreRegistered(t *testing.T) {
	mountCommands(rootCmd)
	for _, path := range [][]string{
		{"collector", "function", "inspect"},
		{"collector", "function", "attach-vpc"},
		{"collector", "function", "invoke"},
		{"host", "reboot"},
		{"host", "diagnostics"},
	} {
		cmd, rest, err := rootCmd.Find(path)
		require.NoError(t, err, path)
		require.Empty(t, rest, path)
		require.Equal(t, path[len(path)-1], cmd.Name())
	}
}

func TestSetupTrustBrowserInstallsInternalCA(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "scripts", "deploy", "install-moox-ca.sh")
	marker := filepath.Join(root, "installed")
	require.NoError(t, os.MkdirAll(filepath.Dir(script), 0o755))
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nset -eu\ncase \" $* \" in\n  *' --check '*) test -f \"$MARKER\";;\n  *) : >\"$MARKER\";;\nesac\n"), 0o700))
	t.Setenv("MARKER", marker)
	t.Chdir(root)
	snapshot := testSetupSnapshot(t)
	original, terminal := refreshSetupConsoleCA, setupHasTerminal
	t.Cleanup(func() { refreshSetupConsoleCA, setupHasTerminal = original, terminal })
	setupHasTerminal = func() bool { return true }
	refreshed := 0
	refreshSetupConsoleCA = func(context.Context, *topology.Config) error {
		refreshed++
		return nil
	}
	cmd := newSetupCommand(setupDeps{load: func(string) (*setupSnapshot, error) { return snapshot, nil }})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"trust-browser", "--file", "moox.toml"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, 1, refreshed)
	require.FileExists(t, marker)
	require.Contains(t, output.String(), `"status":"trusted"`)
}

func TestSetupHostsListsSanitizedManifestHosts(t *testing.T) {
	t.Parallel()
	snapshot := testSetupSnapshot(t)
	cmd := newSetupCommand(setupDeps{load: func(string) (*setupSnapshot, error) { return snapshot, nil }})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"hosts", "--file", "moox.toml"})
	require.NoError(t, cmd.Execute())
	var result struct {
		Hosts []setupHostChoice `json:"hosts"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.Equal(t, []string{"control", "compute"}, []string{result.Hosts[0].Name, result.Hosts[1].Name})
	require.Equal(t, "control", result.Hosts[0].Role)
	for _, secret := range []string{"admin-test-password", "AKID-test-secret", "cloud-test-secret"} {
		require.NotContains(t, output.String(), secret)
	}
}

func TestEventBusFirewallIPResolvesDNSWithoutChangingAdvertisedAddress(t *testing.T) {
	lookup := func(_ context.Context, network, host string) ([]net.IP, error) {
		require.Equal(t, "ip4", network)
		require.Equal(t, "eventbus.example.test", host)
		return []net.IP{net.ParseIP("203.0.113.10")}, nil
	}
	ip, err := eventBusFirewallIP(context.Background(), "eventbus.example.test", lookup)
	require.NoError(t, err)
	require.Equal(t, "203.0.113.10", ip)
}

func TestEventBusFirewallIPRejectsAmbiguousDNS(t *testing.T) {
	lookup := func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("203.0.113.11")}, nil
	}
	_, err := eventBusFirewallIP(context.Background(), "eventbus.example.test", lookup)
	require.ErrorContains(t, err, "exactly one IPv4")
}

func TestSetupControlFirewallRulesOnlyOpenBrowserPort(t *testing.T) {
	rules := setupControlFirewallRules()
	require.Len(t, rules, 1, "只用内部证书，不开 80 端口；没有进程监听 11001")
	assert.Equal(t, "9527", rules[0].Ports)
	assert.Equal(t, "MooX browser HTTPS", rules[0].Description)
	for _, rule := range rules {
		assert.Equal(t, "TCP", rule.Protocol)
		assert.Equal(t, "0.0.0.0/0", rule.CidrBlock)
		assert.Equal(t, "ACCEPT", rule.Action)
	}
}

func TestIsPublicFirewallIPRejectsPrivateAndLoopbackAddresses(t *testing.T) {
	require.True(t, isPublicFirewallIP("203.0.113.8"))
	require.False(t, isPublicFirewallIP("10.0.0.8"))
	require.False(t, isPublicFirewallIP("127.0.0.1"))
}

func TestSetupRuntimeFirewallRulesAreServicePortsAndEventBusRuleIsSeparate(t *testing.T) {
	rules := setupRuntimeFirewallRules()
	require.Len(t, rules, 1, "EventBus 端口不属于控制机的运行时规则，健康端口只听本机不放行")
	assert.Equal(t, "11003", rules[0].Ports)
	assert.Equal(t, "MooX service gateway native", rules[0].Description)
	busRule := setupEventBusFirewallRule(4333)
	assert.Equal(t, "4333", busRule.Ports)
	assert.Equal(t, "MooX EventBus TLS", busRule.Description)
	for _, rule := range append(rules, busRule) {
		assert.Equal(t, "TCP", rule.Protocol)
		assert.Equal(t, "0.0.0.0/0", rule.CidrBlock)
		assert.Equal(t, "ACCEPT", rule.Action)
	}
}

func TestSetupFirewallTargetsCoverRuntimeHosts(t *testing.T) {
	manifest := &topology.Config{
		HostCatalog: map[string]topology.HostDefinition{"control": {Address: "203.0.113.8"}, "storage": {Address: "203.0.113.10"}, "compute": {Address: "203.0.113.9"}},
		EventBus:    topology.EventBus{Port: 4222},
		Deployments: map[string][]string{"control": {"admin", "eventbus"}, "storage": {"storage-primary"}, "compute": {"trade"}},
	}
	targets := mustFirewallTargets(t, manifest)
	require.Len(t, targets, 3)
	ports := func(name string) []string {
		for _, target := range targets {
			if target.Host.Name != name {
				continue
			}
			got := make([]string, 0, len(target.Rules))
			for _, rule := range target.Rules {
				got = append(got, rule.Ports)
			}
			return got
		}
		return nil
	}
	assert.Contains(t, ports("control"), "4222")
	assert.Contains(t, ports("control"), "11003", "出站主机经 control 的 11003 反向连接")
	assert.Equal(t, []string{"11003"}, ports("storage"))
	assert.Equal(t, []string{"11003"}, ports("compute"), "Trade 的 11200 只听本机，不放行")

	// 只能出站的主机不接受入站连接，不作为防火墙目标。
	compute := manifest.HostCatalog["compute"]
	compute.Reachability = "outbound"
	manifest.HostCatalog["compute"] = compute
	targets = mustFirewallTargets(t, manifest)
	require.Len(t, targets, 2)
	assert.Nil(t, ports("compute"))
	relay := setupRelayFirewallTargets(manifest)
	require.Len(t, relay, 1, "EventBus 与控制机同机时合并成一个目标")
	require.Len(t, relay[0].Rules, 2)
	assert.Equal(t, "11003", relay[0].Rules[0].Ports)
	assert.Equal(t, "4222", relay[0].Rules[1].Ports)

	// 停用的主机只写登记，不碰它的防火墙。
	disabled := false
	storage := manifest.HostCatalog["storage"]
	storage.Enabled = &disabled
	manifest.HostCatalog["storage"] = storage
	targets = mustFirewallTargets(t, manifest)
	require.Len(t, targets, 1)
	assert.Nil(t, ports("storage"))
}

// 配置了公网地域的 SCF 时，自动为对应的外部入口主机放行 11004；仅 VPC 地域不放行 11004。
func TestSetupFirewallTargetsOpenExternalEntryForPublicSCFRegions(t *testing.T) {
	manifest := &topology.Config{
		HostCatalog: map[string]topology.HostDefinition{
			"control":  {Address: "203.0.113.8", Region: "ap-guangzhou"},
			"storage":  {Address: "203.0.113.10", Region: "ap-nanjing", ExternalEntry: true},
			"overseas": {Address: "203.0.113.12", Region: "ap-hongkong", ExternalEntry: true},
		},
		EventBus:    topology.EventBus{Port: 4222},
		Deployments: map[string][]string{"control": {"admin"}, "storage": {"storage-primary", "eventbus"}, "overseas": {"trade"}},
		SCFPlan: topology.SCFPlan{
			Enabled: true,
			Regions: map[string]topology.SCFPlanRegion{
				"ap-nanjing":  {Network: topology.SCFNetworkVPC, VPCID: "vpc-1", SubnetID: "subnet-1", EntryHost: "storage"},
				"ap-hongkong": {Network: topology.SCFNetworkVPC, VPCID: "vpc-2", SubnetID: "subnet-2", EntryHost: "overseas"},
				"ap-seoul":    {Network: topology.SCFNetworkPublic, EntryHost: "storage"},
				"ap-tokyo":    {Network: topology.SCFNetworkPublic, EntryHost: "storage"},
			},
		},
	}
	targets := mustFirewallTargets(t, manifest)
	ports := func(name string) []string {
		for _, target := range targets {
			if target.Host.Name != name {
				continue
			}
			got := make([]string, 0, len(target.Rules))
			for _, rule := range target.Rules {
				got = append(got, rule.Ports)
			}
			return got
		}
		return nil
	}
	assert.Contains(t, ports("storage"), "11004", "公网 SCF 地域的入口主机应自动放行 11004")
	assert.NotContains(t, ports("overseas"), "11004", "仅被 VPC 地域引用的入口主机不对公网放行 11004")
}

func mustFirewallTargets(t *testing.T, manifest *topology.Config) []setupFirewallTarget {
	t.Helper()
	targets, err := setupFirewallTargets(manifest)
	require.NoError(t, err)
	return targets
}

// 健康端口只听本机、不验签：任何主机的防火墙都不放行健康端口（包括自动补入的主机网关与主机采集器）。
func TestSetupFirewallOpensNoHealthPort(t *testing.T) {
	manifest := &topology.Config{
		HostCatalog: map[string]topology.HostDefinition{
			"control": {Address: "203.0.113.8", Region: "ap-shanghai"},
			"storage": {Address: "203.0.113.10", Region: "ap-guangzhou"},
			"edge":    {Address: "203.0.113.11", Region: "ap-hongkong"},
		},
		EventBus:    topology.EventBus{Port: 4222},
		Deployments: map[string][]string{"control": {"admin", "monitor"}, "storage": {"storage-primary", "eventbus"}, "edge": {"trade"}},
	}
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	health := map[string]string{}
	for _, component := range catalog.Components {
		health[fmt.Sprint(component.Health.Port)] = component.ID
	}
	targets := mustFirewallTargets(t, manifest)
	require.NotEmpty(t, targets)
	for _, target := range targets {
		for _, rule := range target.Rules {
			component, isHealth := health[rule.Ports]
			assert.False(t, isHealth, "%s 放行了 %s 的健康端口 %s", target.Host.Name, component, rule.Ports)
		}
	}
}

// EventBus 部署在存储机：4222 只在存储机上放行，控制机不再放行；出站主机接入需要的规则也跟着分到两台主机上。
func TestSetupFirewallTargetsFollowEventBusPlacement(t *testing.T) {
	manifest := &topology.Config{
		HostCatalog: map[string]topology.HostDefinition{"control": {Address: "203.0.113.8"}, "storage": {Address: "203.0.113.10"}, "factor": {Address: "192.168.0.103", Reachability: "outbound"}},
		EventBus:    topology.EventBus{Port: 4222},
		Deployments: map[string][]string{"control": {"admin"}, "storage": {"storage-primary", "eventbus"}, "factor": {"factor-engine"}},
	}
	portsOn := func(targets []setupFirewallTarget, name string) []string {
		for _, target := range targets {
			if target.Host.Name == name {
				var ports []string
				for _, rule := range target.Rules {
					ports = append(ports, rule.Ports)
				}
				return ports
			}
		}
		return nil
	}
	targets := mustFirewallTargets(t, manifest)
	assert.NotContains(t, portsOn(targets, "control"), "4222")
	assert.Contains(t, portsOn(targets, "control"), "11003")
	assert.Contains(t, portsOn(targets, "storage"), "4222")

	relay := setupRelayFirewallTargets(manifest)
	require.Len(t, relay, 2)
	assert.Equal(t, []string{"11003"}, portsOn(relay, "control"))
	assert.Equal(t, []string{"4222"}, portsOn(relay, "storage"))
}

// testSetupSnapshot 是测试用的两份配置：控制机带 Storage 与 EventBus，另有一台空主机；秘密里的值都是可辨认的假值。
func testSetupSnapshot(t *testing.T) *setupSnapshot {
	t.Helper()
	cfg, err := topology.Parse([]byte(`[eventbus]
port = 4333

[hosts.control]
address = "203.0.113.8"
ssh = { port = 22, username = "ubuntu" }

[hosts.compute]
address = "203.0.113.9"
ssh = { port = 22, username = "ubuntu" }

[deployments]
control = ["admin", "eventbus", "collector", "factor-mgr", "archive-mgr", "storage-primary"]
compute = []
`))
	require.NoError(t, err)
	secrets, err := topology.ParseSecrets([]byte(`[admin]
username = "admin"
password = "admin-test-password"

[tencent_cloud]
secret_id = "AKID-test-secret"
secret_key = "cloud-test-secret"
`))
	require.NoError(t, err)
	return &setupSnapshot{Manifest: &cfg, Secrets: &secrets}
}
