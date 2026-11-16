package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"net"
	"os"
	"path/filepath"
	"testing"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupCommandContractAndSecrecy(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	secrets := []string{"admin-test-password", "control-ssh-password", "other-ssh-password", "AKID-test-secret", "cloud-test-secret"}
	validateCalls := 0
	deps := setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		validate: func(context.Context, *setupconfig.Snapshot) (setupvalidate.Result, error) {
			validateCalls++
			return setupvalidate.Result{Checks: []setupvalidate.Check{{Name: "config", Status: "valid"}}}, nil
		},
		trustHost: func(context.Context, *setupconfig.Snapshot, string, string) error { return nil },
		status: func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error) {
			return setupclient.StatusResult{State: "completed", Users: 1, Secrets: 1}, nil
		},
		login: func(context.Context, *setupconfig.Snapshot) (setupclient.LoginResult, error) {
			return setupclient.LoginResult{LoginAPI: "valid"}, nil
		},
	}

	tests := []struct {
		name string
		args []string
		key  string
	}{
		{name: "validate", args: []string{"validate", "--file", "moox.toml"}, key: "checks"},
		{name: "trust", args: []string{"trust-host", "--file", "moox.toml", "--host", "control", "--fingerprint", "SHA256:test"}, key: "status"},
		{name: "status", args: []string{"status", "--file", "moox.toml"}, key: "state"},
	}
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
			require.NotContains(t, combined, "将使用默认配置")
		})
	}
	require.Equal(t, 1, validateCalls, "validate must validate the full manifest")
}

func TestSetupHelpListsWorkflowCommands(t *testing.T) {
	t.Parallel()
	cmd := newSetupCommand(setupDeps{})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	for _, name := range []string{"init", "hosts", "validate", "trust-host", "trust-browser", "build-linux", "status", "deploy-host", "deploy-unit", "bootstrap", "release", "watchdog", "metadata-import", "export-skill-config", "firewall"} {
		require.Contains(t, output.String(), name)
	}
	// 已删除的命令：初始秘密由 setup init 写入，因子任务由 setup start 与 factor task apply 导入，运行配置由部署渲染。
	for _, removed := range []string{"apply ", "factors ", "render-runtime-config"} {
		require.NotContains(t, output.String(), "  "+removed)
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
	snapshot := setupSnapshot(t)
	original, terminal := refreshSetupConsoleCA, setupHasTerminal
	t.Cleanup(func() { refreshSetupConsoleCA, setupHasTerminal = original, terminal })
	setupHasTerminal = func() bool { return true }
	refreshed := 0
	refreshSetupConsoleCA = func(context.Context, *topology.Config) error {
		refreshed++
		return nil
	}
	cmd := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }})
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
	snapshot := setupSnapshot(t)
	cmd := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }})
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
	for _, secret := range []string{"admin-test-password", "control-ssh-password", "other-ssh-password", "AKID-test-secret", "cloud-test-secret"} {
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
	manifest := setupconfig.Manifest{
		HostCatalog: map[string]setupconfig.HostDefinition{"control": {Address: "203.0.113.8"}, "storage": {Address: "203.0.113.10"}, "compute": {Address: "203.0.113.9"}},
		EventBus:    setupconfig.EventBus{PublicAddress: "203.0.113.8", Port: 4222},
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
}

func mustFirewallTargets(t *testing.T, manifest setupconfig.Manifest) []setupFirewallTarget {
	t.Helper()
	targets, err := setupFirewallTargets(manifest)
	require.NoError(t, err)
	return targets
}

// 健康端口只听本机、不验签：任何主机的防火墙都不放行健康端口（包括自动补入的主机网关与主机采集器）。
func TestSetupFirewallOpensNoHealthPort(t *testing.T) {
	manifest := setupconfig.Manifest{
		HostCatalog: map[string]setupconfig.HostDefinition{
			"control": {Address: "203.0.113.8", Region: "ap-shanghai"},
			"storage": {Address: "203.0.113.10", Region: "ap-guangzhou"},
			"edge":    {Address: "203.0.113.11", Region: "ap-hongkong"},
		},
		EventBus:    setupconfig.EventBus{Port: 4222},
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
	manifest := setupconfig.Manifest{
		HostCatalog: map[string]setupconfig.HostDefinition{"control": {Address: "203.0.113.8"}, "storage": {Address: "203.0.113.10"}, "factor": {Address: "192.168.0.103", Reachability: "outbound"}},
		EventBus:    setupconfig.EventBus{Port: 4222},
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

func TestSetupMetadataImportPassesExplicitHostAndSpaces(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	var host, seed string
	var spaces []string
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		importMetadata: func(_ context.Context, _ *setupconfig.Snapshot, selectedHost, selectedSeed string, selectedSpaces []string) (metadataImportSummary, error) {
			host, seed, spaces = selectedHost, selectedSeed, append([]string(nil), selectedSpaces...)
			return metadataImportSummary{Status: "ok", Planned: 12, Applied: 12}, nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"metadata-import", "--file", "moox.toml", "--seed", "seed.yaml", "--storage-host", "compute", "--spaces", "stockcn,crypto"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "compute", host)
	require.Equal(t, "seed.yaml", seed)
	require.Equal(t, []string{"stockcn", "crypto"}, spaces)
	var result metadataImportSummary
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.Equal(t, 12, result.Applied)
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
[hosts.control.ssh]
port = 22
username = "ubuntu"
password = "control-ssh-password"

[hosts.compute]
address = "203.0.113.9"
[hosts.compute.ssh]
port = 22
username = "ubuntu"
password = "other-ssh-password"

[deployments]
control = ["admin", "eventbus", "collector", "factor-mgr", "archive-mgr", "storage-primary"]
compute = []
`)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	snapshot, err := setupconfig.Load(path, dir)
	require.NoError(t, err)
	return snapshot
}
