//go:build linux

package unitinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func realPrepareOptions(t *testing.T) PrepareOptions {
	t.Helper()
	fixture := os.Getenv("MOOX_HOST_MATERIAL_FIXTURE")
	if fixture == "" {
		t.Skip("requires real Admin material and prebuilt software; mandatory in the Linux preparation gate")
	}
	raw, err := os.ReadFile(fixture)
	require.NoError(t, err)
	var issued []struct {
		Options  unitbundle.Options  `json:"options"`
		Metadata hostbundle.Metadata `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(raw, &issued))
	var selected unitbundle.Options
	var directory string
	for _, item := range issued {
		if item.Options.HostID == "compute1" && !item.Options.AllowOperator {
			selected = item.Options
			directory = item.Metadata.BundleDir
		}
	}
	require.NotEmpty(t, directory)
	root := privateParent(t)
	deployment, unit := filepath.Join(root, "deployment"), filepath.Join(root, "host unit")
	require.NoError(t, os.Mkdir(deployment, 0o700))
	require.NoError(t, os.Mkdir(unit, 0o700))
	eventbus := filepath.Join(root, "host-agent-eventbus.yaml")
	require.NoError(t, os.WriteFile(eventbus, []byte("version: 1\nurls: [nats://127.0.0.1:4222]\nusername: host-agent\neventbus_token: synthetic-test-token\nca_file: ''\n"), 0o600))
	return PrepareOptions{Archive: os.Getenv("MOOX_UNIT_INSTALL_HOST_ARCHIVE"), SHA256: os.Getenv("MOOX_UNIT_INSTALL_HOST_SHA256"), Profile: "host", DeploymentRoot: deployment, UnitRoot: unit, ReleaseID: "first", MaterialDirectory: directory, MaterialOptions: selected, Environment: map[string]map[string]string{"host-gateway": healthEnvironment(), "host-agent": healthEnvironment()}, Overrides: map[string]string{"host-agent/config/eventbus.yaml": eventbus}}
}

func healthEnvironment() map[string]string {
	return map[string]string{"MOOX_HEALTH_AUTH_VERSION": "moox-health-v1", "MOOX_HEALTH_AUTH_ACCESS_KEY": "synthetic-monitor", "MOOX_HEALTH_AUTH_SECRET_KEY": "synthetic-health-secret-at-least-thirty-two-bytes"}
}

func prepareWithHelper(t *testing.T, options PrepareOptions) (Prepared, error) {
	t.Helper()
	raw, err := json.Marshal(options)
	require.NoError(t, err)
	request := filepath.Join(privateParent(t), "request.json")
	require.NoError(t, os.WriteFile(request, append(raw, '\n'), 0o600))
	output, err := exec.CommandContext(t.Context(), os.Getenv("MOOX_RUNTIME_BINARY"), "prepare", "--request", request).CombinedOutput()
	if err != nil {
		// The helper emits only redacted preparation errors; retain them so a
		// real-software gate failure identifies the violated contract.
		return Prepared{}, fmt.Errorf("preparation helper: %w: %s", err, output)
	}
	require.NotContains(t, string(output), healthEnvironment()["MOOX_HEALTH_AUTH_SECRET_KEY"])
	require.NotContains(t, string(output), "PRIVATE KEY")
	var prepared Prepared
	require.NoError(t, json.Unmarshal(output, &prepared))
	return prepared, nil
}

func runScript(t *testing.T, directory, operation string, args ...string) unitruntime.Result {
	t.Helper()
	output, err := exec.CommandContext(t.Context(), filepath.Join(directory, operation+".sh"), args...).CombinedOutput()
	require.NoError(t, err)
	var result unitruntime.Result
	require.NoError(t, json.Unmarshal(output, &result))
	return result
}

func TestUnitPreparationLinuxUsesActualHostSoftwareAndPreservesPause(t *testing.T) {
	options := realPrepareOptions(t)
	options.Components = []string{"host-agent", "host-gateway"}
	prepared, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	require.Equal(t, options.UnitRoot, prepared.HostUnitRoot)
	require.Equal(t, []string{"host-gateway", "host-agent"}, prepared.Components)
	verified, err := ReadPrepared(t.Context(), prepared.Directory)
	require.NoError(t, err)
	require.Equal(t, prepared, verified)
	require.NoError(t, unitruntime.ValidateRelease(filepath.Join(prepared.Directory, "runtime.json")))
	var agent map[string]any
	raw, err := os.ReadFile(filepath.Join(prepared.Directory, "host-agent/config/app.yaml"))
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(raw, &agent))
	require.Equal(t, "compute1", agent["host_id"])
	require.Equal(t, filepath.Join(prepared.Directory, "host-agent/data/identity.yaml"), agent["identity_path"])
	require.Equal(t, filepath.Join(prepared.Directory, "host-agent/config/eventbus.yaml"), agent["eventbus_config"])
	status := runScript(t, prepared.Directory, "status")
	require.Len(t, status.Components, 2)
	for _, component := range status.Components {
		require.Equal(t, "stopped", component.State)
	}
	paused := runScript(t, prepared.Directory, "pause", "--components", "host-agent")
	require.Equal(t, "paused", paused.Components[0].State)
	require.NoError(t, os.Symlink(prepared.Directory, filepath.Join(options.UnitRoot, "current")))
	options.ReleaseID = "second"
	next, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	current, err := os.Readlink(filepath.Join(options.UnitRoot, "current"))
	require.NoError(t, err)
	require.Equal(t, prepared.Directory, current)
	for _, operation := range []string{"start", "healthcheck"} {
		result := runScript(t, next.Directory, operation, "--components", "host-agent")
		require.Equal(t, "paused", result.Components[0].State)
		require.Zero(t, result.Components[0].PID)
	}
	_, err = os.Lstat(filepath.Join(next.Directory, "operator"))
	require.True(t, os.IsNotExist(err))
}

func TestUnitPreparationLinuxReceiptRejectsChangedFilesAndEscapingHostView(t *testing.T) {
	options := realPrepareOptions(t)
	host, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	output, err := exec.CommandContext(t.Context(), os.Getenv("MOOX_RUNTIME_BINARY"), "inspect-release", "--directory", host.Directory).CombinedOutput()
	require.NoError(t, err)
	var verified Prepared
	require.NoError(t, json.Unmarshal(output, &verified))
	require.Equal(t, host, verified)
	config := filepath.Join(host.Directory, "host-agent/config/app.yaml")
	original, err := os.ReadFile(config)
	require.NoError(t, err)
	for _, mutation := range []string{"config", "extra", "symlink", "mode", "receipt"} {
		t.Run(mutation, func(t *testing.T) {
			extra := filepath.Join(host.Directory, "untracked")
			receipt := filepath.Join(host.Directory, "prepared.json")
			saved, err := os.ReadFile(receipt)
			require.NoError(t, err)
			switch mutation {
			case "config":
				require.NoError(t, os.WriteFile(config, append(original, []byte("# changed\n")...), 0o600))
			case "extra":
				require.NoError(t, os.WriteFile(extra, []byte("untracked"), 0o600))
			case "symlink":
				require.NoError(t, os.Symlink(config, extra))
			case "mode":
				require.NoError(t, os.Chmod(config, 0o644))
			case "receipt":
				require.NoError(t, os.WriteFile(receipt, append(saved, '\n'), 0o600))
			}
			_, err = ReadPrepared(t.Context(), host.Directory)
			require.Error(t, err)
			require.NotContains(t, err.Error(), healthEnvironment()["MOOX_HEALTH_AUTH_SECRET_KEY"])
			require.NoError(t, os.WriteFile(config, original, 0o600))
			require.NoError(t, os.Chmod(config, 0o600))
			require.NoError(t, os.WriteFile(receipt, saved, 0o600))
			if _, err := os.Lstat(extra); err == nil {
				require.NoError(t, os.Remove(extra))
			}
		})
	}
	_, err = ReadPrepared(t.Context(), host.Directory)
	require.NoError(t, err)
	canceled, stop := context.WithCancel(t.Context())
	stop()
	_, err = ReadPrepared(canceled, host.Directory)
	require.ErrorIs(t, err, context.Canceled)
	current := filepath.Join(options.UnitRoot, "current")
	require.NoError(t, os.Symlink(host.Directory, current))
	business := options
	business.UnitRoot = filepath.Join(privateParent(t), "business")
	require.NoError(t, os.Mkdir(business.UnitRoot, 0o700))
	business.HostUnitRoot = options.UnitRoot
	require.NoError(t, validateHostView(business, host.HostID))
	require.NoError(t, os.Remove(current))
	// A real plan at an unrelated physical release cannot become the host view.
	outside := filepath.Join(privateParent(t), "outside")
	require.NoError(t, os.Rename(host.Directory, outside))
	require.NoError(t, os.Symlink(outside, current))
	require.Error(t, validateHostView(business, host.HostID))
}

func TestUnitPreparationLinuxBusinessUsesHostViewAndOnlyAccessIdentities(t *testing.T) {
	options := realPrepareOptions(t)
	host, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	require.NoError(t, os.Symlink(host.Directory, filepath.Join(options.UnitRoot, "current")))
	business := options
	business.UnitRoot = filepath.Join(filepath.Dir(options.UnitRoot), "access unit")
	require.NoError(t, os.Mkdir(business.UnitRoot, 0o700))
	business.HostUnitRoot = options.UnitRoot
	business.Profile = "access"
	business.Archive = os.Getenv("MOOX_UNIT_INSTALL_ACCESS_ARCHIVE")
	business.SHA256 = os.Getenv("MOOX_UNIT_INSTALL_ACCESS_SHA256")
	business.Environment = map[string]map[string]string{"access": healthEnvironment()}
	business.Overrides = nil
	prepared, err := prepareWithHelper(t, business)
	require.NoError(t, err)
	_, err = ReadPrepared(t.Context(), prepared.Directory)
	require.NoError(t, err)
	require.Len(t, prepared.Identity.Credentials, 1)
	require.Equal(t, "access", prepared.Identity.Credentials[0].Caller)
	view, err := os.Readlink(filepath.Join(prepared.Directory, "host-gateway"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(options.UnitRoot, "current/host-gateway"), view)
	viewPath := filepath.Join(prepared.Directory, "host-gateway")
	require.NoError(t, os.Remove(viewPath))
	_, err = ReadPrepared(t.Context(), prepared.Directory)
	require.Error(t, err, "a business candidate must retain its host view")
	require.NoError(t, os.Symlink(view, viewPath))
	physical, err := filepath.EvalSymlinks(filepath.Join(prepared.Directory, "host-gateway/config/app.yaml"))
	require.NoError(t, err)
	config, err := hostgatewayconfig.Load(physical)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(host.Directory, "certs/moox-ca.crt"), config.TLS.CAFile)
	_, err = gatewayauth.LoadCredentialRegistry(filepath.Join(prepared.Directory, "secrets/access/access-verification.json"))
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(prepared.Directory, "access/config/app.yaml"))
	require.NoError(t, err)
	var rendered struct {
		Client struct {
			Caller  string `yaml:"caller"`
			KeyID   string `yaml:"key_id"`
			KeyFile string `yaml:"key_file"`
		} `yaml:"gateway_client"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &rendered))
	require.Equal(t, "access", rendered.Client.Caller)
	require.Equal(t, prepared.Identity.Credentials[0].KeyID, rendered.Client.KeyID)
	require.Equal(t, "../../secrets/caller-access.key", rendered.Client.KeyFile)
	for _, name := range []string{"certs", "operator", "secrets/caller-host-agent.key", "secrets/caller-host-gateway.key", "secrets/caller-trade.key"} {
		_, err := os.Lstat(filepath.Join(prepared.Directory, name))
		require.True(t, os.IsNotExist(err))
	}
	status := runScript(t, prepared.Directory, "status")
	require.Len(t, status.Components, 1)
	require.Equal(t, "stopped", status.Components[0].State)
}

func TestUnitPreparationLinuxFailureDoesNotPublishOrReplaceCurrent(t *testing.T) {
	options := realPrepareOptions(t)
	host, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	require.NoError(t, os.Symlink(host.Directory, filepath.Join(options.UnitRoot, "current")))
	_, err = prepareWithHelper(t, options)
	require.Error(t, err)
	options.ReleaseID = "invalid"
	delete(options.Environment["host-agent"], "MOOX_HEALTH_AUTH_SECRET_KEY")
	_, err = prepareWithHelper(t, options)
	require.Error(t, err)
	options.Environment["host-agent"] = healthEnvironment()
	options.Overrides["host-gateway/config/app.yaml"] = options.Overrides["host-agent/config/eventbus.yaml"]
	_, err = prepareWithHelper(t, options)
	require.Error(t, err)
	entries, err := os.ReadDir(filepath.Join(options.UnitRoot, "releases"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "first", entries[0].Name())
	current, err := os.Readlink(filepath.Join(options.UnitRoot, "current"))
	require.NoError(t, err)
	require.Equal(t, host.Directory, current)
}
