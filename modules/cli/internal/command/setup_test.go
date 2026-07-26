package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
		deployControl: func(context.Context, *setupconfig.Snapshot) error { return nil },
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
	for _, name := range []string{"hosts", "validate", "trust-host", "deploy-control", "deploy-service", "apply", "status", "deploy-storage", "metadata-import", "verify-storage", "e2e-storage", "browser-e2e-storage"} {
		require.Contains(t, output.String(), name)
	}
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
		deployStorage: func(_ context.Context, _ *setupconfig.Snapshot, host string, selectedReset bool) error {
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
	require.JSONEq(t, `{"host":"compute","status":"ready","reset_storage_data":false}`, output.String())
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

func TestControlDeployOptionsUseManifestEventBusEndpoint(t *testing.T) {
	snapshot := setupSnapshot(t)
	opts := controlDeployOptions(snapshot, "/repo")
	require.Equal(t, "/repo", opts.RepositoryRoot)
	require.Equal(t, "203.0.113.8", opts.PublicHost)
	require.Equal(t, "eventbus.example.test", opts.EventBusPublicAddress)
	require.Equal(t, 4333, opts.EventBusPort)
	require.True(t, opts.EventBusTLSEnabled)
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
		deployStorage: func(_ context.Context, _ *setupconfig.Snapshot, _ string, selectedReset bool) error {
			reset = selectedReset
			return nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"deploy-storage", "--file", "custom.toml", "--host", "compute", "--reset-storage-data"})
	require.NoError(t, cmd.Execute())
	require.True(t, reset)
	require.JSONEq(t, `{"host":"compute","status":"ready","reset_storage_data":true}`, output.String())
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
		deployStorage: func(context.Context, *setupconfig.Snapshot, string, bool) error {
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
