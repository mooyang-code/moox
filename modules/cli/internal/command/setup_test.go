package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupdeploy "github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupCommandContractAndSecrecy(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	secrets := []string{"admin-test-password", "control-ssh-password", "other-ssh-password", "AKID-test-secret", "cloud-test-secret"}
	validateCalls := 0
	deploymentValidateCalls := 0
	deps := setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		validate: func(context.Context, *setupconfig.Snapshot) (setupvalidate.Result, error) {
			validateCalls++
			return setupvalidate.Result{Checks: []setupvalidate.Check{{Name: "config", Status: "valid"}}}, nil
		},
		validateDeployment: func(context.Context, *setupconfig.Snapshot, []setupconfig.Host) (setupvalidate.Result, error) {
			deploymentValidateCalls++
			return setupvalidate.Result{Checks: []setupvalidate.Check{{Name: "config", Status: "valid"}}}, nil
		},
		trustHost:     func(context.Context, *setupconfig.Snapshot, string, string) error { return nil },
		deployControl: func(context.Context, *setupconfig.Snapshot, bool) error { return nil },
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
		{name: "validate", args: []string{"validate", "--file", "custom.toml"}, key: "checks"},
		{name: "trust", args: []string{"trust-host", "--file", "custom.toml", "--host", "control", "--fingerprint", "SHA256:test"}, key: "status"},
		{name: "deploy", args: []string{"deploy-control", "--file", "custom.toml"}, key: "status"},
		{name: "apply", args: []string{"apply", "--file", "custom.toml"}, key: "login_api"},
		{name: "status", args: []string{"status", "--file", "custom.toml"}, key: "state"},
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
	require.Equal(t, 1, deploymentValidateCalls, "deploy-control must only validate config and SSH")
}

func TestSetupHelpListsWorkflowCommands(t *testing.T) {
	t.Parallel()
	cmd := newSetupCommand(setupDeps{})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	for _, name := range []string{"init", "hosts", "validate", "trust-host", "trust-browser", "deploy-control", "deploy-service", "apply", "status", "deploy-storage", "install-storage-watchdog", "metadata-import", "verify-storage", "e2e-storage", "browser-e2e-storage", "e2e-eventbus"} {
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
	cmd.SetArgs([]string{"trust-browser", "--file", "custom.toml"})
	require.NoError(t, cmd.Execute())
	require.JSONEq(t, `{"host":"control","status":"not_required"}`, output.String())
}

func TestSetupTrustBrowserInstallsInternalCA(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "scripts", "install-caddy-ca.sh")
	marker := filepath.Join(root, "installed")
	require.NoError(t, os.MkdirAll(filepath.Dir(script), 0o755))
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nset -eu\ncase \" $* \" in\n  *' --check '*) test -f \"$MARKER\";;\n  *) : >\"$MARKER\";;\nesac\n"), 0o700))
	t.Setenv("MARKER", marker)
	t.Chdir(root)
	snapshot := setupSnapshot(t)
	snapshot.Manifest.ControlHost.TLSMode = "internal"
	cmd := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"trust-browser", "--file", "custom.toml"})
	require.NoError(t, cmd.Execute())
	require.FileExists(t, marker)
	require.Contains(t, output.String(), `"status":"trusted"`)
}

func TestBrowserServiceNames(t *testing.T) {
	for _, service := range []string{"admin", "admin_gateway", "moox-admin", "web-host", "web_host", "moox-web-host"} {
		assert.True(t, isBrowserService(service), service)
	}
	for _, service := range []string{"storage-view", "collector", "factor"} {
		assert.False(t, isBrowserService(service), service)
	}
}

func TestSetupRenderRuntimeConfigUsesOneSnapshot(t *testing.T) {
	snapshot := setupSnapshot(t)
	tradePath := filepath.Join(t.TempDir(), "trade", "app.yaml")
	collectorPath := filepath.Join(t.TempDir(), "collector", "app.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(tradePath), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(collectorPath), 0o755))
	require.NoError(t, os.WriteFile(tradePath, []byte("database:\n  path: ./trade.db\n"), 0o644))
	require.NoError(t, os.WriteFile(collectorPath, []byte("database:\n  path: ./collector.db\n"), 0o644))
	cmd := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"render-runtime-config", "--file", "custom.toml", "--trade-output", tradePath, "--collector-output", collectorPath})
	require.NoError(t, cmd.Execute())
	require.JSONEq(t, fmt.Sprintf(`{"status":"rendered","trade_output":%q,"collector_output":%q,"dns_resolver_enabled":false,"dns_resolver_node_id":"","dns_resolver_target":"ip://127.0.0.1:11003"}`, tradePath, collectorPath), output.String())
	tradeRaw, err := os.ReadFile(tradePath)
	require.NoError(t, err)
	require.Contains(t, string(tradeRaw), "dns_resolver:")
	collectorRaw, err := os.ReadFile(collectorPath)
	require.NoError(t, err)
	require.Contains(t, string(collectorRaw), "dns_resolver:")
}

func TestSetupInstallStorageWatchdogCommand(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	var selectedHost string
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		installStorageWatchdog: func(_ context.Context, _ *setupconfig.Snapshot, host string) error {
			selectedHost = host
			return nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"install-storage-watchdog", "--file", "custom.toml", "--host", "compute"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "compute", selectedHost)
	require.JSONEq(t, `{"host":"compute","status":"ready"}`, output.String())
}

func TestSetupE2EEventBusWritesOnlyBooleanProof(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		e2eEventBus: func(context.Context, *setupconfig.Snapshot) (eventBusE2EResult, error) {
			return eventBusE2EResult{
				PublicTLS: true, WorkerBindFetchAck: true, WorkerCreateDenied: true, WorkerPublishDenied: true,
			}, nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"e2e-eventbus", "--file", "custom.toml"})
	require.NoError(t, cmd.Execute())
	require.JSONEq(t, `{"public_tls":true,"worker_bind_fetch_ack":true,"worker_create_denied":true,"worker_publish_denied":true}`, output.String())
	for _, secret := range []string{"admin-test-password", "control-ssh-password", "AKID-test-secret", "cloud-test-secret"} {
		require.NotContains(t, output.String(), secret)
	}
}

func TestEventBusE2ERequiresMatchingPermissionViolation(t *testing.T) {
	errorsCh := make(chan error, 2)
	errorsCh <- errors.New(`nats: permissions violation for publish to "$JS.API.CONSUMER.CREATE.MOOX"`)
	assert.True(t, hasPermissionViolation(errorsCh, "$JS.API.CONSUMER.CREATE.MOOX"))

	errorsCh <- errors.New("context deadline exceeded")
	assert.False(t, hasPermissionViolation(errorsCh, "moox.cloudnode.job.execution.requested"))
}

func TestSetupDeployServicePassesPackageAndService(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	var selectedHost, selectedPackage, selectedService, selectedDir string
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		deployService: func(_ context.Context, _ *setupconfig.Snapshot, host, packagePath, service, deployDir string) (setupdeploy.ServiceResult, error) {
			selectedHost, selectedPackage, selectedService, selectedDir = host, packagePath, service, deployDir
			return setupdeploy.ServiceResult{ServiceName: service, DeployDir: deployDir, LocalSHA256: "local", RemoteSHA256: "local"}, nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"deploy-service", "--file", "custom.toml", "--host", "compute", "--service", "admin", "--package", "./release/moox-admin.zip", "--deploy-dir", "/home/ubuntu/moox/prod"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "compute", selectedHost)
	require.Equal(t, "./release/moox-admin.zip", selectedPackage)
	require.Equal(t, "admin", selectedService)
	require.Equal(t, "/home/ubuntu/moox/prod", selectedDir)
	require.JSONEq(t, `{"service_name":"admin","deploy_dir":"/home/ubuntu/moox/prod","remote_archive":"","local_sha256":"local","remote_sha256":"local"}`, output.String())
}

func TestSetupHostsListsSanitizedManifestHosts(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	cmd := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"hosts", "--file", "custom.toml"})
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
	snapshot.Manifest.CompileHost = setupconfig.Host{
		Name: "compile", Address: "203.0.113.10", Port: 2222, Username: "builder", Password: "compile-password",
	}
	cmd := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"hosts", "--file", "custom.toml"})
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
	snapshot.Manifest.CompileHost = setupconfig.Host{Name: "compile", Address: "203.0.113.10", Port: 22, Username: "builder"}
	host, err := findSetupTrustHost(snapshot.Manifest, "compile")
	require.NoError(t, err)
	assert.Equal(t, "203.0.113.10", host.Address)
}

func TestSetupDeployStorageRequiresAndPassesSelectedHost(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	selected := ""
	var validatedHosts []string
	reset := true
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		validate: func(context.Context, *setupconfig.Snapshot) (setupvalidate.Result, error) {
			return setupvalidate.Result{}, fmt.Errorf("full validation must not run")
		},
		validateDeployment: func(_ context.Context, _ *setupconfig.Snapshot, hosts []setupconfig.Host) (setupvalidate.Result, error) {
			for _, host := range hosts {
				validatedHosts = append(validatedHosts, host.Name)
			}
			return setupvalidate.Result{}, nil
		},
		status: func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error) {
			return setupclient.StatusResult{State: "completed"}, nil
		},
		deployStorage: func(_ context.Context, _ *setupconfig.Snapshot, host string, selectedReset, _ bool) error {
			selected = host
			reset = selectedReset
			return nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"deploy-storage", "--file", "custom.toml", "--host", "compute"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "compute", selected)
	require.Equal(t, []string{"control", "compute"}, validatedHosts)
	require.False(t, reset, "reset must default to false")
	require.JSONEq(t, `{"host":"compute","status":"ready","reset_storage_data":false,"reset_view_data":false}`, output.String())
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

func TestNormalizeHealthAuthRejectsShellContent(t *testing.T) {
	const valid = "MOOX_HEALTH_AUTH_VERSION=moox-health-v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=monitor\nMOOX_HEALTH_AUTH_SECRET_KEY=health+/=\n"
	got, err := normalizeHealthAuth(valid)
	require.NoError(t, err)
	assert.Equal(t, valid, got)

	for _, raw := range []string{
		valid + "touch /tmp/pwned\n",
		"MOOX_HEALTH_AUTH_VERSION=moox-health-v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=monitor\nMOOX_HEALTH_AUTH_SECRET_KEY=$(id)\n",
		"MOOX_HEALTH_AUTH_VERSION=moox-health-v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=monitor\n",
	} {
		_, err := normalizeHealthAuth(raw)
		require.Error(t, err)
	}
}

func TestSetupDeployControlWritesSanitizedValidationResultOnFailure(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		validateDeployment: func(context.Context, *setupconfig.Snapshot, []setupconfig.Host) (setupvalidate.Result, error) {
			return setupvalidate.Result{Checks: []setupvalidate.Check{{Name: "host:control", Status: "invalid", Code: "host_key_unknown", Fingerprint: "SHA256:verified"}}}, setupvalidate.ErrValidationFailed
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"deploy-control", "--file", "custom.toml"})
	require.ErrorIs(t, cmd.Execute(), setupvalidate.ErrValidationFailed)
	require.JSONEq(t, `{"checks":[{"name":"host:control","status":"invalid","code":"host_key_unknown","fingerprint":"SHA256:verified"}]}`, output.String())
}

func TestSetupDeployControlAcceptsExplicitResetFlag(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	reset := false
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		validateDeployment: func(context.Context, *setupconfig.Snapshot, []setupconfig.Host) (setupvalidate.Result, error) {
			return setupvalidate.Result{}, nil
		},
		deployControl: func(_ context.Context, _ *setupconfig.Snapshot, selectedReset bool) error {
			reset = selectedReset
			return nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"deploy-control", "--file", "custom.toml", "--reset-data"})
	require.NoError(t, cmd.Execute())
	require.True(t, reset)
	require.JSONEq(t, `{"host":"control","status":"ready","reset_data":true,"certificate":{"mode":"public","issuer":"letsencrypt","automatic_renewal":true,"renewal":"caddy_acme_ari"}}`, output.String())
}

func TestSetupDeployControlValidatesConfiguredResolverHost(t *testing.T) {
	snapshot := setupSnapshot(t)
	snapshot.Manifest.OtherHosts[0].Name = "compute-1"
	snapshot.Manifest.DNSResolver = setupconfig.DNSResolver{
		Enabled: true, TradeNode: "compute-1", Domains: []string{"fapi.binance.com"},
	}
	var hosts []setupconfig.Host
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		validateDeployment: func(_ context.Context, _ *setupconfig.Snapshot, values []setupconfig.Host) (setupvalidate.Result, error) {
			hosts = append([]setupconfig.Host(nil), values...)
			return setupvalidate.Result{}, nil
		},
		deployControl: func(context.Context, *setupconfig.Snapshot, bool) error { return nil },
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"deploy-control", "--file", "custom.toml"})
	require.NoError(t, cmd.Execute())
	require.Len(t, hosts, 2)
	require.Equal(t, "control", hosts[0].Name)
	require.Equal(t, "compute-1", hosts[1].Name)
}

func TestSetupCertificateSummarySelectsTrustModel(t *testing.T) {
	t.Parallel()
	assert.Equal(t, map[string]any{
		"mode": "public", "issuer": "letsencrypt", "automatic_renewal": true, "renewal": "caddy_acme_ari",
	}, setupCertificateSummary("203.0.113.8"))
	assert.Equal(t, map[string]any{
		"mode": "internal", "issuer": "caddy_internal_ca", "automatic_renewal": true, "renewal": "caddy_internal",
	}, setupCertificateSummary("127.0.0.1"))
}

func TestControlDeployOptionsUseManifestEventBusEndpoint(t *testing.T) {
	snapshot := setupSnapshot(t)
	opts := controlDeployOptions(snapshot, "/repo")
	require.Equal(t, "/repo", opts.RepositoryRoot)
	require.Equal(t, "203.0.113.8", opts.PublicHost)
	require.Equal(t, "eventbus.example.test", opts.EventBusPublicAddress)
	require.Equal(t, 4333, opts.EventBusPort)
	require.True(t, opts.EventBusTLSEnabled)
	require.Equal(t, "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test", opts.MonitoringWeComWebhook)
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

func TestSetupControlDeploymentOpensACMEBeforeDeploy(t *testing.T) {
	events := []string{}
	step := func(name string, fail bool) func() error {
		return func() error {
			events = append(events, name)
			if fail {
				return errors.New(name)
			}
			return nil
		}
	}
	require.NoError(t, runSetupControlDeploySteps(step("control-firewall", false), step("deploy", false), step("eventbus-firewall", false)))
	require.Equal(t, []string{"control-firewall", "deploy", "eventbus-firewall"}, events)

	events = nil
	require.EqualError(t, runSetupControlDeploySteps(step("control-firewall", true), step("deploy", false), step("eventbus-firewall", false)), "control-firewall")
	require.Equal(t, []string{"control-firewall"}, events)
}

func TestSetupDeployStoragePassesExplicitResetFlag(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	var reset bool
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		validateDeployment: func(context.Context, *setupconfig.Snapshot, []setupconfig.Host) (setupvalidate.Result, error) {
			return setupvalidate.Result{}, nil
		},
		status: func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error) {
			return setupclient.StatusResult{State: "completed"}, nil
		},
		deployStorage: func(_ context.Context, _ *setupconfig.Snapshot, _ string, selectedReset, _ bool) error {
			reset = selectedReset
			return nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"deploy-storage", "--file", "custom.toml", "--host", "compute", "--reset-storage-data"})
	require.NoError(t, cmd.Execute())
	require.True(t, reset)
	require.JSONEq(t, `{"host":"compute","status":"ready","reset_storage_data":true,"reset_view_data":false}`, output.String())
}

func TestSetupDeployStoragePassesViewResetFlagAndRejectsCombinedReset(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	var resetView bool
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		validateDeployment: func(context.Context, *setupconfig.Snapshot, []setupconfig.Host) (setupvalidate.Result, error) {
			return setupvalidate.Result{}, nil
		},
		status: func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error) {
			return setupclient.StatusResult{State: "completed"}, nil
		},
		deployStorage: func(_ context.Context, _ *setupconfig.Snapshot, _ string, _, selectedResetView bool) error {
			resetView = selectedResetView
			return nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"deploy-storage", "--file", "custom.toml", "--host", "compute", "--reset-view-data"})
	require.NoError(t, cmd.Execute())
	require.True(t, resetView)
	require.JSONEq(t, `{"host":"compute","status":"ready","reset_storage_data":false,"reset_view_data":true}`, output.String())

	cmd = newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		validateDeployment: func(context.Context, *setupconfig.Snapshot, []setupconfig.Host) (setupvalidate.Result, error) {
			return setupvalidate.Result{}, nil
		},
		status: func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error) {
			return setupclient.StatusResult{State: "completed"}, nil
		},
		deployStorage: func(context.Context, *setupconfig.Snapshot, string, bool, bool) error { return nil },
	})
	cmd.SetArgs([]string{"deploy-storage", "--file", "custom.toml", "--host", "compute", "--reset-view-data", "--reset-storage-data"})
	require.EqualError(t, cmd.Execute(), "--reset-storage-data and --reset-view-data are mutually exclusive")
}

func TestSetupDeployStorageRequiresCompletedControlSetup(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	deployed := false
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		validateDeployment: func(context.Context, *setupconfig.Snapshot, []setupconfig.Host) (setupvalidate.Result, error) {
			return setupvalidate.Result{}, nil
		},
		status: func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error) {
			return setupclient.StatusResult{State: "incomplete"}, nil
		},
		deployStorage: func(context.Context, *setupconfig.Snapshot, string, bool, bool) error {
			deployed = true
			return nil
		},
	})
	cmd.SetArgs([]string{"deploy-storage", "--file", "custom.toml", "--host", "compute"})
	require.EqualError(t, cmd.Execute(), "setup_incomplete")
	require.False(t, deployed)
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
	cmd.SetArgs([]string{"metadata-import", "--file", "custom.toml", "--seed", "seed.yaml", "--storage-host", "compute", "--spaces", "stock_cn,crypto"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "compute", host)
	require.Equal(t, "seed.yaml", seed)
	require.Equal(t, []string{"stock_cn", "crypto"}, spaces)
	var result metadataImportSummary
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.Equal(t, 12, result.Applied)
}

func setupSnapshot(t *testing.T) *setupconfig.Snapshot {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.toml")
	raw := []byte(`[admin]
username = "admin"
password = "admin-test-password"
[tencent_cloud]
secret_id = "AKID-test-secret"
secret_key = "cloud-test-secret"
[eventbus]
public_address = "eventbus.example.test"
port = 4333
tls_enabled = true
[monitoring]
wecom_webhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"
[control_host]
name = "control"
address = "203.0.113.8"
port = 22
username = "ubuntu"
password = "control-ssh-password"
[[other_hosts]]
name = "compute"
address = "203.0.113.9"
port = 22
username = "ubuntu"
password = "other-ssh-password"
`)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	snapshot, err := setupconfig.Load(path, dir)
	require.NoError(t, err)
	return snapshot
}
