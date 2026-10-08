package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/stretchr/testify/require"
)

// fakeConfigHost keeps files in memory and runs the config scripts.
type fakeConfigHost struct {
	files       map[string][]byte
	restarts    []string
	failRestart bool
}

func (h *fakeConfigHost) Run(_ context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	script := argv[2]
	switch {
	case strings.Contains(script, "exit 3"):
		raw, ok := h.files[argv[4]]
		if !ok {
			return setupssh.Result{ExitCode: 3}, errors.New("ssh_command_failed: Process exited with status 3")
		}
		return setupssh.Result{Stdout: string(raw)}, nil
	case strings.Contains(script, ".sha256\" 2>/dev/null"):
		return setupssh.Result{Stdout: string(h.files[argv[4]+".sha256"])}, nil
	case script == configPublishScript:
		file, tmp, services := argv[5], argv[6], argv[7:]
		previous, existed := h.files[file]
		h.files[file] = h.files[tmp]
		delete(h.files, tmp)
		if h.failRestart {
			if existed {
				h.files[file] = previous
			} else {
				delete(h.files, file)
			}
			return setupssh.Result{ExitCode: 70, Stderr: "restart failed; restoring the previous file"}, errors.New("ssh_command_failed: Process exited with status 70")
		}
		h.files[file+".sha256"] = []byte(sha256Hex(h.files[file]) + "\n")
		h.restarts = append(h.restarts, services...)
		return setupssh.Result{}, nil
	}
	return setupssh.Result{ExitCode: 1, Stderr: "unexpected script"}, nil
}

func (h *fakeConfigHost) Upload(_ context.Context, src io.Reader, _ int64, dst string, _ fs.FileMode) error {
	raw, err := io.ReadAll(src)
	h.files[dst] = raw
	return err
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
host = "203.0.113.8"
tls_enabled = true
[hosts."203.0.113.8"]
username = "ubuntu"
password = "control-ssh-password"
[hosts."203.0.113.20"]
username = "ubuntu"
password = "storage-ssh-password"
[control_host]
name = "control"
host = "203.0.113.8"
[storage_host]
name = "storage"
host = "203.0.113.20"
`

const collectorTestConfig = "server:\n  name: collector\n"

func newConfigTestDeps(t *testing.T, extra string) (configDeps, map[string]*fakeConfigHost, string) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "moox.toml")
	require.NoError(t, os.WriteFile(file, []byte(configTestManifest+extra), 0o600))
	hosts := map[string]*fakeConfigHost{
		"control": {files: map[string][]byte{"/data/moox/prod/collector/config/app.yaml": []byte(collectorTestConfig)}},
		"storage": {files: map[string][]byte{}},
	}
	return configDeps{
		load: func(path string) (*setupconfig.Snapshot, error) { return setupconfig.Load(path, dir) },
		dial: func(_ context.Context, host setupconfig.Host) (configHost, error) { return hosts[host.Name], nil },
	}, hosts, file
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

func TestConfigPlanRendersEveryTargetWithoutChangingHosts(t *testing.T) {
	deps, hosts, file := newConfigTestDeps(t, "")
	result, err := configCommand(t, deps, file, "plan")
	require.NoError(t, err)
	policy := targetPlan(t, result, "storage-policy")
	require.Equal(t, configStatusMissing, policy["status"])
	require.Equal(t, "/data/moox/storage/config/storage-policy.json", policy["path"])
	require.Equal(t, []any{"storage-primary", "storage-view"}, policy["restart"])
	collector := targetPlan(t, result, "collector-runtime")
	require.Equal(t, configStatusChanged, collector["status"])
	require.Contains(t, collector["changed_keys"], "collector_retention.max_rows_per_pass")
	require.Empty(t, hosts["storage"].files, "plan must not write")
	require.Equal(t, collectorTestConfig, string(hosts["control"].files["/data/moox/prod/collector/config/app.yaml"]))
	require.NotContains(t, configJSON(t, result), "password")
}

func TestConfigPublishWritesChangedTargetsAndIsIdempotent(t *testing.T) {
	deps, hosts, file := newConfigTestDeps(t, "")
	_, err := configCommand(t, deps, file, "publish")
	require.ErrorContains(t, err, "--yes")

	result, err := configCommand(t, deps, file, "publish", "--yes")
	require.NoError(t, err)
	require.Equal(t, []any{"storage-policy", "collector-runtime"}, result["published"])
	written, err := storagepolicy.Parse(hosts["storage"].files["/data/moox/storage/config/storage-policy.json"])
	require.NoError(t, err)
	require.Equal(t, storagepolicy.Default().Retention, written.Retention)
	require.Equal(t, []string{"storage-primary", "storage-view"}, hosts["storage"].restarts)
	require.Equal(t, []string{"collector"}, hosts["control"].restarts)

	again, err := configCommand(t, deps, file, "publish", "--yes")
	require.NoError(t, err)
	require.EqualValues(t, 0, again["changed"])
	require.Empty(t, again["published"])
	require.Len(t, hosts["storage"].restarts, 2, "an unchanged target must not restart")
}

func TestConfigPublishRequiresConsentToShortenRetention(t *testing.T) {
	deps, hosts, file := newConfigTestDeps(t, "")
	_, err := configCommand(t, deps, file, "publish", "--yes", "--only", "storage-policy")
	require.NoError(t, err)

	shorter := strings.Replace(configTestManifest, "[control_host]", "[storage_retention.spaces.crypto]\n\"1m\" = \"5d\"\n\n[control_host]", 1)
	require.NoError(t, os.WriteFile(file, []byte(shorter), 0o600))
	plan, err := configCommand(t, deps, file, "plan", "--only", "storage-policy")
	require.NoError(t, err)
	require.Contains(t, targetPlan(t, plan, "storage-policy")["retention_shrinks"], "crypto 1m: 168h -> 120h")

	_, err = configCommand(t, deps, file, "publish", "--yes", "--only", "storage-policy")
	require.ErrorContains(t, err, "--allow-retention-shrink")
	current, err := storagepolicy.Parse(hosts["storage"].files["/data/moox/storage/config/storage-policy.json"])
	require.NoError(t, err)
	require.Equal(t, "168h", current.Retention.Defaults["1m"], "a refused shrink leaves the host untouched")

	_, err = configCommand(t, deps, file, "publish", "--yes", "--only", "storage-policy", "--allow-retention-shrink")
	require.NoError(t, err)
	current, err = storagepolicy.Parse(hosts["storage"].files["/data/moox/storage/config/storage-policy.json"])
	require.NoError(t, err)
	require.Equal(t, "120h", current.Retention.Spaces["crypto"]["1m"])
}

func TestConfigPublishStopsAtTheFirstFailedTarget(t *testing.T) {
	deps, hosts, file := newConfigTestDeps(t, "")
	hosts["storage"].failRestart = true
	result, err := configCommand(t, deps, file, "publish", "--yes")
	require.ErrorContains(t, err, "storage-policy: publish failed")
	require.Empty(t, result["published"])
	require.NotContains(t, hosts["storage"].files, "/data/moox/storage/config/storage-policy.json", "the failed file is rolled back")
	require.Equal(t, collectorTestConfig, string(hosts["control"].files["/data/moox/prod/collector/config/app.yaml"]), "later targets are not published")
}

func TestConfigPlanWarnsAboutHandEditedFiles(t *testing.T) {
	deps, hosts, file := newConfigTestDeps(t, "")
	_, err := configCommand(t, deps, file, "publish", "--yes", "--only", "collector-runtime")
	require.NoError(t, err)
	path := "/data/moox/prod/collector/config/app.yaml"
	edited := strings.Replace(string(hosts["control"].files[path]), "max_rows_per_pass: 50000", "max_rows_per_pass: 1", 1)
	require.NotEqual(t, string(hosts["control"].files[path]), edited)
	hosts["control"].files[path] = []byte(edited)
	plan, err := configCommand(t, deps, file, "plan", "--only", "collector-runtime")
	require.NoError(t, err)
	collector := targetPlan(t, plan, "collector-runtime")
	require.Equal(t, configStatusChanged, collector["status"])
	require.Contains(t, configJSON(t, collector["warnings"]), "edited after the last publish")
}

func TestConfigSkipsModulesNotDeployedOnTheHost(t *testing.T) {
	deps, hosts, file := newConfigTestDeps(t, "")
	delete(hosts["control"].files, "/data/moox/prod/collector/config/app.yaml")
	result, err := configCommand(t, deps, file, "publish", "--yes")
	require.NoError(t, err)
	require.Equal(t, configStatusNotDeployed, targetPlan(t, result, "collector-runtime")["status"])
	require.Equal(t, []any{"storage-policy"}, result["published"])
	require.NotContains(t, hosts["control"].files, "/data/moox/prod/collector/config/app.yaml", "a missing module config is never created")
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
