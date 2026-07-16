package command

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
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
	require.Equal(t, 3, validateCalls, "validate, deploy-control, and apply must each validate the full manifest")
}

func TestSetupHelpListsFiveCommands(t *testing.T) {
	t.Parallel()
	cmd := newSetupCommand(setupDeps{})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	for _, name := range []string{"validate", "trust-host", "deploy-control", "apply", "status"} {
		require.Contains(t, output.String(), name)
	}
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
