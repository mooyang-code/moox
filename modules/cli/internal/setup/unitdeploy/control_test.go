package unitdeploy

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type controlTransport struct {
	setupssh.Client
	run func([]string) (setupssh.Result, error)
}

func (c controlTransport) Run(_ context.Context, args []string, _ io.Reader) (setupssh.Result, error) {
	return c.run(args)
}

func TestControlConfigurationsPreserveBusinessSettingsAndBindActualEndpoints(t *testing.T) {
	directory := t.TempDir()
	for id, template := range map[string]string{
		"console-proxy": "public: {host: localhost, port: 9527, http3: true}\ntls: {mode: internal, storage_root: ../data/caddy/caddy}\nlifecycle: {drain_timeout: 91s}\n",
		"monitor":       "placement: {enabled: true, https: {}}\nmetrics: {enabled: true, no_data_intervals: 7}\n",
		"collector":     "database: {path: ./data/collector.db}\nstorage: {result_data_node_id: obsolete-node}\nscf_access: {key_id: original-scf-id}\nperiod_readiness: {grace: 7m}\n",
		"factor-mgr":    "python: {bin: python3}\nengine: {lease_ttl: 73s}\n",
	} {
		parent := filepath.Join(directory, id, "config")
		require.NoError(t, os.MkdirAll(parent, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(parent, "app.yaml"), []byte(template), 0o600))
	}
	snapshot := &setupconfig.Snapshot{Manifest: setupconfig.Manifest{HostCatalog: map[string]setupconfig.HostDefinition{"control": {Address: "2001:db8::10", TLSMode: "internal"}, "storage1": {Address: "192.0.2.20"}}, Placements: map[string][]string{"control": {"admin", "eventbus", "console-proxy", "monitor", "collector", "factor-mgr"}, "storage1": {"storage-primary", "storage-node", "storage-view"}}, CollectorRetention: setupconfig.CollectorRetention{MaintenanceInterval: "2m", MaxRowsPerPass: 19}}}
	components := []string{"admin", "eventbus", "console-proxy", "monitor", "collector", "factor-mgr"}
	rendered, err := controlConfigurations(snapshot, directory, components, "/runtime/factor/bin/python")
	require.NoError(t, err)
	require.Len(t, rendered, 4)
	decode := func(name string) map[string]any {
		var value map[string]any
		require.NoError(t, yaml.Unmarshal(rendered[name+"/config/app.yaml"], &value))
		return value
	}
	proxy, monitor, collector, factor := decode("console-proxy"), decode("monitor"), decode("collector"), decode("factor-mgr")
	require.Equal(t, "2001:db8::10", proxy["public"].(map[string]any)["host"])
	require.Equal(t, "91s", proxy["lifecycle"].(map[string]any)["drain_timeout"])
	https := monitor["placement"].(map[string]any)["https"].(map[string]any)["console-proxy"].(map[string]any)
	require.Equal(t, "https://[2001:db8::10]:9527/", https["url"])
	require.Equal(t, "[2001:db8::10]:9527", https["connect_address"])
	require.Equal(t, "../../console-proxy/data/caddy/internal-ca.sha256", https["ca_baseline"])
	require.Equal(t, 7, monitor["metrics"].(map[string]any)["no_data_intervals"])
	require.Equal(t, "config/dataset-health-policy.yaml", monitor["metrics"].(map[string]any)["dataset_health_policy_path"])
	require.Equal(t, "storage1-storage-node", collector["storage"].(map[string]any)["result_data_node_id"])
	require.Equal(t, "original-scf-id", collector["scf_access"].(map[string]any)["key_id"])
	require.Equal(t, "7m", collector["period_readiness"].(map[string]any)["grace"])
	require.Equal(t, 19, collector["collector_retention"].(map[string]any)["max_rows_per_pass"])
	require.Equal(t, "/runtime/factor/bin/python", factor["python"].(map[string]any)["bin"])
	require.Equal(t, "73s", factor["engine"].(map[string]any)["lease_ttl"])
	definition := snapshot.Manifest.HostCatalog["control"]
	definition.TLSMode = "public"
	snapshot.Manifest.HostCatalog["control"] = definition
	rendered, err = controlConfigurations(snapshot, directory, components, "/runtime/factor/bin/python")
	require.NoError(t, err)
	https = decode("monitor")["placement"].(map[string]any)["https"].(map[string]any)["console-proxy"].(map[string]any)
	require.Equal(t, "public", https["trust_mode"])
	require.NotContains(t, https, "ca_file")
	require.NotContains(t, https, "ca_baseline")
}

func TestControlEnvironmentDistributesOnlyRequiredStorageRoleSecrets(t *testing.T) {
	identity := runtimeIdentity{StoragePrimarySecret: strings.Repeat("p", 32), StorageViewSecret: strings.Repeat("v", 32), StorageNodeSecret: strings.Repeat("n", 32)}
	env, err := unitEnvironment(setupconfig.Manifest{}, identity, "control", []string{"admin", "eventbus", "console-proxy", "monitor", "collector", "factor-mgr", "strategy"})
	require.NoError(t, err)
	for _, id := range []string{"admin", "eventbus", "console-proxy"} {
		require.NotContains(t, env[id], "MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	}
	for _, id := range []string{"monitor", "collector", "factor-mgr", "strategy"} {
		require.Equal(t, identity.StoragePrimarySecret, env[id]["MOOX_STORAGE_PRIMARY_AUTH_SECRET"])
		require.NotContains(t, env[id], "MOOX_STORAGE_NODE_AUTH_SECRET")
	}
	require.Equal(t, identity.StorageViewSecret, env["strategy"]["MOOX_STORAGE_VIEW_AUTH_SECRET"])
	require.NotContains(t, env["monitor"], "MOOX_STORAGE_VIEW_AUTH_SECRET")
}

func TestControlPreflightRejectsUnreadyAndUnrelatedRuntimeOrPython(t *testing.T) {
	transport := controlTransport{run: func(args []string) (setupssh.Result, error) {
		require.Equal(t, []string{"python3", "-I", "-c"}, args[:3])
		return setupssh.Result{Stdout: `{"executable":"/prepared/bin/python","pandas":"2.3.3","numpy":"2.4.0"}`}, nil
	}}
	python, err := checkFactorPython(t.Context(), transport, "")
	require.NoError(t, err)
	require.Equal(t, "/prepared/bin/python", python)
	for _, output := range []string{`{"executable":"/prepared/python","pandas":"2.1.0","numpy":"2.0.0"}`, `{"executable":"/prepared/python","pandas":"2.3.3","numpy":"1.26.0"}`, `{"executable":"../python","pandas":"2.3.3","numpy":"2.0.0"}`, `{"executable":"/prepared/python","pandas":"2.3.3","numpy":"2.0.0","unexpected":"private-secret"}`} {
		transport.run = func([]string) (setupssh.Result, error) { return setupssh.Result{Stdout: output}, nil }
		_, err := checkFactorPython(t.Context(), transport, "")
		require.Error(t, err)
		require.NotContains(t, err.Error(), "private-secret")
	}
	transport.run = func([]string) (setupssh.Result, error) { return setupssh.Result{}, errors.New("private-secret") }
	_, err = checkFactorPython(t.Context(), transport, "")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private-secret")
	for _, output := range []string{`{"host_id":"unrelated","operation":"status","components":[]}`, `{"host_id":"control","operation":"status","components":[{"id":"monitor","state":"running","pid":42,"ready":false}]}`, `{"host_id":"control","operation":"status","components":[{"id":"unrelated","state":"running","pid":42,"ready":true}]}`} {
		transport.run = func([]string) (setupssh.Result, error) { return setupssh.Result{Stdout: output}, nil }
		_, err := activeControlStatus(t.Context(), transport, "/runtime", "/unit", "control", []string{"monitor"})
		require.Error(t, err)
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(directory, 0o700))
	root, err := privateRoot(directory)
	require.NoError(t, err)
	defer root.Close()
	require.NoError(t, bindControlRequest(root, "sha256:original"))
	require.NoError(t, bindControlRequest(root, "sha256:original"))
	require.Error(t, bindControlRequest(root, "sha256:changed"))
}
