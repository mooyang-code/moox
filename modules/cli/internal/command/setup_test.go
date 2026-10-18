package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/tlstrust"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
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
		apply: func(context.Context, *setupconfig.Snapshot) (setupclient.ApplyResult, error) {
			return setupclient.ApplyResult{Action: "created", Users: 1, Secrets: 1, Hosts: 2}, nil
		},
		status: func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error) {
			return setupclient.StatusResult{State: "completed", Users: 1, Secrets: 1, Hosts: 2}, nil
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
		{name: "apply", args: []string{"apply", "--file", "moox.toml"}, key: "login_api"},
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
	require.Equal(t, 2, validateCalls, "validate and apply must validate the full manifest")
}

func TestSetupHelpListsWorkflowCommands(t *testing.T) {
	t.Parallel()
	cmd := newSetupCommand(setupDeps{})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	for _, name := range []string{"init", "hosts", "validate", "trust-host", "trust-browser", "build-linux", "apply", "status", "deploy-host", "deploy-unit", "bootstrap", "release", "watchdog", "metadata-import", "export-skill-config", "firewall"} {
		require.Contains(t, output.String(), name)
	}
	require.Contains(t, output.String(), "render-runtime-config")
}

func TestSetupTrustBrowserSkipsPublicTLS(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	cmd := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"trust-browser", "--file", "moox.toml"})
	require.NoError(t, cmd.Execute())
	require.JSONEq(t, `{"host":"control","status":"not_required"}`, output.String())
}

func TestSetupTrustBrowserInstallsInternalCA(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "scripts", "deploy", "install-caddy-ca.sh")
	marker := filepath.Join(root, "installed")
	require.NoError(t, os.MkdirAll(filepath.Dir(script), 0o755))
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nset -eu\ncase \" $* \" in\n  *' --check '*) test -f \"$MARKER\";;\n  *) : >\"$MARKER\";;\nesac\n"), 0o700))
	t.Setenv("MARKER", marker)
	t.Chdir(root)
	snapshot := setupSnapshot(t)
	host := snapshot.Manifest.HostCatalog["control"]
	host.TLSMode = "internal"
	snapshot.Manifest.HostCatalog["control"] = host
	original := refreshSetupConsoleCA
	t.Cleanup(func() { refreshSetupConsoleCA = original })
	refreshed := 0
	refreshSetupConsoleCA = func(context.Context, setupconfig.Manifest) error {
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

func TestSetupRenderRuntimeConfigUsesOneSnapshot(t *testing.T) {
	snapshot := setupSnapshot(t)
	egressPath := filepath.Join(t.TempDir(), "egress-proxy", "app.yaml")
	collectorPath := filepath.Join(t.TempDir(), "collector", "app.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(egressPath), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(collectorPath), 0o755))
	require.NoError(t, os.WriteFile(egressPath, []byte("database:\n  path: ./egress.db\n"), 0o644))
	require.NoError(t, os.WriteFile(collectorPath, []byte("database:\n  path: ./collector.db\n"), 0o644))
	cmd := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"render-runtime-config", "--file", "moox.toml", "--egress-output", egressPath, "--collector-output", collectorPath})
	require.NoError(t, cmd.Execute())
	require.JSONEq(t, fmt.Sprintf(`{"status":"rendered","egress_output":%q,"collector_output":%q,"trade_console_host":"","trade_console_port":0}`, egressPath, collectorPath), output.String())
	egressRaw, err := os.ReadFile(egressPath)
	require.NoError(t, err)
	require.Contains(t, string(egressRaw), "domains:")
	collectorRaw, err := os.ReadFile(collectorPath)
	require.NoError(t, err)
	require.Contains(t, string(collectorRaw), "egress_proxy:")
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

func TestSetupHostsListsCompileHostRole(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	testfixture.SetHost(&snapshot.Manifest, setupconfig.Host{Name: "compile", Address: "203.0.113.10", Port: 2222, Username: "builder", Password: "compile-password"})
	snapshot.Manifest.CompileHostRef.Host = "compile"
	cmd := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"hosts", "--file", "moox.toml"})
	require.NoError(t, cmd.Execute())
	var result struct {
		Hosts []setupHostChoice `json:"hosts"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.Len(t, result.Hosts, 3)
	assert.Equal(t, setupHostChoice{Name: "compile", Address: "203.0.113.10", Port: 2222, Username: "builder", Role: "compile"}, result.Hosts[2])
	assert.NotContains(t, output.String(), "compile-password")
}

func TestFindSetupTrustHostIncludesCompileHost(t *testing.T) {
	snapshot := setupSnapshot(t)
	testfixture.SetHost(&snapshot.Manifest, setupconfig.Host{Name: "compile", Address: "203.0.113.10", Port: 22, Username: "builder"})
	snapshot.Manifest.CompileHostRef.Host = "compile"
	host, err := findSetupTrustHost(snapshot.Manifest, "compile")
	require.NoError(t, err)
	assert.Equal(t, "203.0.113.10", host.Address)
}

func TestNormalizeStorageInternalAuthRejectsShellContent(t *testing.T) {
	const valid = "MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary+/=\nMOOX_STORAGE_VIEW_AUTH_SECRET=view._-\n"
	got, err := normalizeStorageInternalAuth(valid)
	require.NoError(t, err)
	assert.Equal(t, valid, got)

	for _, raw := range []string{
		valid + "touch /tmp/pwned\n",
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET=$(id)\nMOOX_STORAGE_VIEW_AUTH_SECRET=view\n",
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET=one\nMOOX_STORAGE_PRIMARY_AUTH_SECRET=two\nMOOX_STORAGE_VIEW_AUTH_SECRET=view\n",
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary\n",
	} {
		_, err := normalizeStorageInternalAuth(raw)
		require.Error(t, err)
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

func TestSetupControlFirewallRulesIncludePublicTLSAndServicePorts(t *testing.T) {
	rules := setupControlFirewallRules()
	require.Len(t, rules, 3)
	assert.Equal(t, "80", rules[0].Ports)
	assert.Equal(t, "MooX ACME HTTP challenge", rules[0].Description)
	assert.Equal(t, "9527", rules[1].Ports)
	assert.Equal(t, "MooX browser HTTPS", rules[1].Description)
	assert.Equal(t, "11001", rules[2].Ports)
	assert.Equal(t, "MooX service HTTPS", rules[2].Description)
	for _, rule := range rules {
		assert.Equal(t, "TCP", rule.Protocol)
		assert.Equal(t, "0.0.0.0/0", rule.CidrBlock)
		assert.Equal(t, "ACCEPT", rule.Action)
	}
}

func TestSetupControlFirewallRulesSkipACMEForInternalTLS(t *testing.T) {
	rules := setupControlFirewallRulesForTLS(tlstrust.TLSModeInternal, "203.0.113.8")
	require.Len(t, rules, 2)
	require.Equal(t, "9527", rules[0].Ports)
	require.Equal(t, "11001", rules[1].Ports)
}

func TestIsPublicFirewallIPRejectsPrivateAndLoopbackAddresses(t *testing.T) {
	require.True(t, isPublicFirewallIP("203.0.113.8"))
	require.False(t, isPublicFirewallIP("10.0.0.8"))
	require.False(t, isPublicFirewallIP("127.0.0.1"))
}

func TestSetupRuntimeFirewallRulesIncludeEventBusAndServicePorts(t *testing.T) {
	rules := setupRuntimeFirewallRules(4333)
	require.Len(t, rules, 4)
	assert.Equal(t, "4333", rules[0].Ports)
	assert.Equal(t, "MooX EventBus TLS", rules[0].Description)
	assert.Equal(t, "11003", rules[1].Ports)
	assert.Equal(t, "MooX service gateway native", rules[1].Description)
	assert.Equal(t, "11012", rules[2].Ports)
	assert.Equal(t, "MooX SCF Gateway readiness", rules[2].Description)
	assert.Equal(t, "11409", rules[3].Ports)
	assert.Equal(t, "MooX SCF Monitor readiness", rules[3].Description)
	for _, rule := range rules {
		assert.Equal(t, "TCP", rule.Protocol)
		assert.Equal(t, "0.0.0.0/0", rule.CidrBlock)
		assert.Equal(t, "ACCEPT", rule.Action)
	}
}

func TestSetupFirewallTargetsCoverRuntimeHosts(t *testing.T) {
	manifest := setupconfig.Manifest{
		HostCatalog: map[string]setupconfig.HostDefinition{"control": {Address: "203.0.113.8"}, "storage": {Address: "203.0.113.10"}, "compute": {Address: "203.0.113.9"}},
		EventBus:    setupconfig.EventBus{PublicAddress: "203.0.113.8", Port: 4222},
		Placements:  map[string][]string{"control": {"admin", "console-proxy", "web-host", "eventbus"}, "storage": {"storage-primary"}, "compute": {"trade"}},
	}
	targets := setupFirewallTargets(manifest)
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
	assert.Equal(t, []string{"11003", "11200"}, ports("compute"))

	// 只能出站的主机不接受入站连接，不作为防火墙目标。
	compute := manifest.HostCatalog["compute"]
	compute.Reachability = "outbound"
	manifest.HostCatalog["compute"] = compute
	targets = setupFirewallTargets(manifest)
	require.Len(t, targets, 2)
	assert.Nil(t, ports("compute"))
	relay := setupRelayFirewallRules(4222)
	require.Len(t, relay, 2)
	assert.Equal(t, "11003", relay[0].Ports)
	assert.Equal(t, "4222", relay[1].Ports)
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

[placements]
control = ["admin", "console-proxy", "web-host", "eventbus"]
compute = []
`)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	snapshot, err := setupconfig.Load(path, dir)
	require.NoError(t, err)
	return snapshot
}
