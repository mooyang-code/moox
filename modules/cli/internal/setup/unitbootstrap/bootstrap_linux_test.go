//go:build linux

package unitbootstrap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func bootstrapFixture(t *testing.T) (Request, string) {
	t.Helper()
	if os.Getenv("MOOX_BOOTSTRAP_HOST_ARCHIVE") == "" {
		t.Skip("requires real prebuilt host/control software; mandatory in the Linux bootstrap gate")
	}
	root, err := os.MkdirTemp("", "moox-bootstrap-actual-")
	require.NoError(t, err)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("synthetic diagnostic directory: %s", root)
			return
		}
		os.RemoveAll(root)
	})
	deployment := filepath.Join(root, "deployment")
	require.NoError(t, os.Mkdir(deployment, 0o700))
	for _, name := range []string{"host", "control"} {
		require.NoError(t, os.Mkdir(filepath.Join(deployment, name), 0o700))
	}
	health := func() map[string]string {
		return map[string]string{"MOOX_HEALTH_AUTH_VERSION": "moox-health-v1", "MOOX_HEALTH_AUTH_ACCESS_KEY": "synthetic-monitor", "MOOX_HEALTH_AUTH_SECRET_KEY": "synthetic-bootstrap-health-at-least-32-bytes"}
	}
	components := []string{"admin", "eventbus", "web-host", "console-proxy"}
	request := Request{Version: 1, DeploymentRoot: deployment, Topology: hostbundle.Topology{Version: 1, ControlHostID: "control", Hosts: []hostbundle.Host{{HostID: "control", Address: "127.0.0.1", Description: "initial", Components: components}}}, Host: Unit{Archive: os.Getenv("MOOX_BOOTSTRAP_HOST_ARCHIVE"), SHA256: os.Getenv("MOOX_BOOTSTRAP_HOST_SHA256"), UnitRoot: filepath.Join(deployment, "host"), Environment: map[string]map[string]string{"host-gateway": health(), "host-agent": health()}, Overrides: map[string]string{}}, Control: Unit{Archive: os.Getenv("MOOX_BOOTSTRAP_CONTROL_ARCHIVE"), SHA256: os.Getenv("MOOX_BOOTSTRAP_CONTROL_SHA256"), UnitRoot: filepath.Join(deployment, "control"), Environment: map[string]map[string]string{}, Overrides: map[string]string{}}}
	for _, id := range components {
		request.Control.Environment[id] = health()
	}
	request.Control.Environment["admin"]["MOOX_ADMIN_JWT_SECRET_KEY"] = "synthetic-bootstrap-jwt-secret-at-least-32-bytes"
	write := func(name, raw string) string {
		filename := filepath.Join(root, name)
		require.NoError(t, os.WriteFile(filename, []byte(raw), 0o600))
		return filename
	}
	request.Control.Overrides["admin/config/console.yaml"] = write("console.yaml", "jwt:\n  secret_key: ''\n  access_expired: 24h\nconsole:\n  debug: false\ncors:\n  allowed_origins: [https://localhost:9527]\n")
	request.Control.Overrides["console-proxy/config/app.yaml"] = write("proxy.yaml", "public:\n  host: localhost\n  bind: 127.0.0.1\n  port: 9527\n  http3: false\ntls:\n  mode: internal\n  storage_root: ../data/caddy/caddy\n  ca_baseline: ../data/caddy/internal-ca.sha256\n  ca_publish_dir: ../certs/caddy\n  initialize_ca: true\nupstreams:\n  admin: 127.0.0.1:11000\n  web: 127.0.0.1:9528\nhealth:\n  listen: 127.0.0.1:19528\nlifecycle:\n  drain_timeout: 2s\n  engine_stop_timeout: 1s\n  cleanup_margin: 1s\n  startup_timeout: 15s\n")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, unit := range []Unit{request.Control, request.Host} {
			plan := filepath.Join(unit.UnitRoot, "current/runtime.json")
			if _, err := os.Stat(plan); err == nil {
				output, err := exec.CommandContext(ctx, os.Getenv("MOOX_RUNTIME_BINARY"), "stop", "--plan", plan).CombinedOutput()
				require.NoError(t, err, "cleanup: %s", output)
			}
		}
	})
	return request, root
}

func runBootstrap(t *testing.T, request Request, root string) (Result, error) {
	t.Helper()
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	filename := filepath.Join(root, "request.json")
	require.NoError(t, os.WriteFile(filename, append(raw, '\n'), 0o600))
	command := exec.CommandContext(t.Context(), os.Getenv("MOOX_RUNTIME_BINARY"), "bootstrap", "--request", filename)
	output, err := command.CombinedOutput()
	require.NotContains(t, string(output), "synthetic-bootstrap-jwt-secret")
	require.NotContains(t, string(output), "PRIVATE KEY")
	if err != nil {
		return Result{}, fmt.Errorf("bootstrap helper: %w: %s", err, output)
	}
	var result Result
	require.NoError(t, json.Unmarshal(output, &result))
	return result, nil
}

func runtimeStatus(t *testing.T, directory string) unitruntime.Result {
	t.Helper()
	output, err := exec.CommandContext(t.Context(), os.Getenv("MOOX_RUNTIME_BINARY"), "status", "--plan", filepath.Join(directory, "runtime.json")).CombinedOutput()
	require.NoError(t, err, "status: %s", output)
	var status unitruntime.Result
	require.NoError(t, json.Unmarshal(output, &status))
	return status
}

func TestBootstrapLinuxActualAdminGatewayAndServicesRecoverTogether(t *testing.T) {
	request, root := bootstrapFixture(t)
	initial, err := runBootstrap(t, request, root)
	require.NoError(t, err)
	require.Equal(t, "complete", initial.Phase)
	busCA := filepath.Join(initial.ControlDirectory, "eventbus/secrets/eventbus/ca.pem")
	unauthenticated, err := jetstream.Connect(t.Context(), jetstream.Config{URLs: []string{"tls://127.0.0.1:4222"}, TLSCAFile: busCA, ConnectTimeout: 2 * time.Second})
	if unauthenticated != nil {
		unauthenticated.Close()
	}
	require.Error(t, err, "the deployed broker must reject TLS clients without a role")
	initialRole, err := jetstream.LoadCredentialFile(filepath.Join(initial.HostDirectory, "host-agent/config/eventbus.yaml"))
	require.NoError(t, err)
	require.Equal(t, "hostagent-publisher", initialRole.Username)
	require.NotEmpty(t, initialRole.EventBusToken)
	host, control := runtimeStatus(t, initial.HostDirectory), runtimeStatus(t, initial.ControlDirectory)
	for _, status := range append(host.Components, control.Components...) {
		require.True(t, status.Ready, "%s", status.ID)
		require.Positive(t, status.PID)
	}
	repeated, err := runBootstrap(t, request, root)
	require.NoError(t, err)
	require.Equal(t, initial, repeated)
	require.Equal(t, host, runtimeStatus(t, initial.HostDirectory))
	require.Equal(t, control, runtimeStatus(t, initial.ControlDirectory))
	for _, directory := range []string{initial.ControlDirectory, initial.HostDirectory} {
		output, err := exec.CommandContext(t.Context(), os.Getenv("MOOX_RUNTIME_BINARY"), "stop", "--plan", filepath.Join(directory, "runtime.json")).CombinedOutput()
		require.NoError(t, err, "simulate host reboot: %s", output)
	}
	repaired, err := runBootstrap(t, request, root)
	require.NoError(t, err)
	require.Equal(t, initial, repaired, "completed bootstrap repairs the same releases without offline initialization")
	for _, directory := range []string{initial.HostDirectory, initial.ControlDirectory} {
		for _, status := range runtimeStatus(t, directory).Components {
			require.True(t, status.Ready, "repaired %s", status.ID)
		}
	}
	ca, err := os.ReadFile(filepath.Join(initial.ControlDirectory, "console-proxy/certs/caddy/root.crt"))
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(ca))
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 10 * time.Second}
	response, err := client.Get("https://localhost:9527/")
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	proxy := request.Control.Overrides["console-proxy/config/app.yaml"]
	config, err := os.ReadFile(proxy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(proxy, []byte(strings.ReplaceAll(string(config), "initialize_ca: true", "initialize_ca: false")), 0o600))
	request.Topology.Hosts[0].Description = "rerun"
	upgraded, err := runBootstrap(t, request, root)
	require.NoError(t, err)
	require.NotEqual(t, initial.ControlDirectory, upgraded.ControlDirectory)
	require.Equal(t, initial.CAFingerprint, upgraded.CAFingerprint)
	upgradedRole, err := jetstream.LoadCredentialFile(filepath.Join(upgraded.HostDirectory, "host-agent/config/eventbus.yaml"))
	require.NoError(t, err)
	require.Equal(t, initialRole.EventBusToken, upgradedRole.EventBusToken, "offline reruns reuse issued EventBus role tokens")
	upgradedBusCA, err := os.ReadFile(filepath.Join(upgraded.ControlDirectory, "eventbus/secrets/eventbus/ca.pem"))
	require.NoError(t, err)
	initialBusCA, err := os.ReadFile(busCA)
	require.NoError(t, err)
	require.Equal(t, initialBusCA, upgradedBusCA, "EventBus TLS identity survives upgrades")
	newCA, err := os.ReadFile(filepath.Join(upgraded.ControlDirectory, "console-proxy/certs/caddy/root.crt"))
	require.NoError(t, err)
	require.Equal(t, ca, newCA)
	description := func(directory string) string {
		db, err := gorm.Open(sqlite.Open(filepath.Join(directory, "admin/data/admin.db")), &gorm.Config{})
		require.NoError(t, err)
		connection, err := db.DB()
		require.NoError(t, err)
		defer connection.Close()
		var value string
		require.NoError(t, db.Raw("SELECT c_description FROM t_hosts WHERE c_host_id = ?", "control").Scan(&value).Error)
		return value
	}
	require.Equal(t, "initial", description(initial.ControlDirectory))
	require.Equal(t, "rerun", description(upgraded.ControlDirectory))
	request.Control.Environment["admin"]["MOOX_ADMIN_JWT_SECRET_KEY"] = ""
	_, err = runBootstrap(t, request, root)
	require.Error(t, err)
	for _, item := range []struct{ unit, directory string }{{request.Host.UnitRoot, upgraded.HostDirectory}, {request.Control.UnitRoot, upgraded.ControlDirectory}} {
		current, err := os.Readlink(filepath.Join(item.unit, "current"))
		require.NoError(t, err)
		require.Equal(t, item.directory, current)
		for _, status := range runtimeStatus(t, item.directory).Components {
			require.True(t, status.Ready, "recovered %s", status.ID)
		}
	}
	_, err = os.Lstat(filepath.Join(request.DeploymentRoot, "run/bootstrap.json"))
	require.True(t, os.IsNotExist(err))
}

func TestBootstrapLinuxSIGKILLAtAdminStartupRecoversWholeHost(t *testing.T) {
	request, root := bootstrapFixture(t)
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	filename := filepath.Join(root, "request.json")
	require.NoError(t, os.WriteFile(filename, append(raw, '\n'), 0o600))
	command := exec.CommandContext(t.Context(), os.Getenv("MOOX_RUNTIME_BINARY"), "bootstrap", "--request", filename)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	require.NoError(t, command.Start())
	defer command.Process.Kill()
	var captured journal
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(request.DeploymentRoot, "bootstrap/workflow.json"))
		if err == nil && json.Unmarshal(raw, &captured) == nil && captured.Phase == "admin-starting" {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	require.Equal(t, "admin-starting", captured.Phase, "must kill the actual workflow after both units are active and before Admin readiness completes")
	require.NoError(t, command.Process.Signal(syscall.SIGKILL))
	err = command.Wait()
	require.Error(t, err)
	exit, ok := err.(*exec.ExitError)
	require.True(t, ok)
	require.Equal(t, syscall.SIGKILL, exit.Sys().(syscall.WaitStatus).Signal())
	issuedRole, err := jetstream.LoadCredentialFile(filepath.Join(captured.Host.Candidate, "host-agent/config/eventbus.yaml"))
	require.NoError(t, err)
	issuedBusCA, err := os.ReadFile(filepath.Join(captured.Control.Candidate, "eventbus/secrets/eventbus/ca.pem"))
	require.NoError(t, err)
	_, err = os.Lstat(filepath.Join(request.DeploymentRoot, "run/bootstrap.json"))
	require.NoError(t, err)
	for _, directory := range []string{captured.Host.Candidate, captured.Control.Candidate} {
		output, err := exec.CommandContext(t.Context(), os.Getenv("MOOX_RUNTIME_BINARY"), "start", "--plan", filepath.Join(directory, "runtime.json")).CombinedOutput()
		require.Error(t, err)
		require.Contains(t, string(output), "interrupted bootstrap")
	}
	changed := request
	changed.Topology.Hosts = append([]hostbundle.Host(nil), request.Topology.Hosts...)
	changed.Topology.Hosts[0].Description = "unrelated-request"
	_, err = runBootstrap(t, changed, root)
	require.Error(t, err)
	require.Contains(t, err.Error(), "matching coordinator")
	recovered, err := runBootstrap(t, request, root)
	require.NoError(t, err)
	require.Equal(t, "complete", recovered.Phase)
	require.Equal(t, captured.CAFingerprint, recovered.CAFingerprint)
	require.Equal(t, captured.ExpectedHash, recovered.ExpectedHash, "interrupted first initialization must retain issued gateway KeyIDs")
	recoveredRole, err := jetstream.LoadCredentialFile(filepath.Join(recovered.HostDirectory, "host-agent/config/eventbus.yaml"))
	require.NoError(t, err)
	require.Equal(t, issuedRole.EventBusToken, recoveredRole.EventBusToken, "SIGKILL recovery must retain issued EventBus roles")
	recoveredBusCA, err := os.ReadFile(filepath.Join(recovered.ControlDirectory, "eventbus/secrets/eventbus/ca.pem"))
	require.NoError(t, err)
	require.Equal(t, issuedBusCA, recoveredBusCA, "SIGKILL recovery must retain the EventBus TLS identity")
	require.NotEqual(t, captured.Control.Candidate, recovered.ControlDirectory)
	for _, directory := range []string{recovered.HostDirectory, recovered.ControlDirectory} {
		for _, status := range runtimeStatus(t, directory).Components {
			require.True(t, status.Ready, "%s", status.ID)
		}
	}
	_, err = os.Lstat(filepath.Join(request.DeploymentRoot, "run/bootstrap.json"))
	require.True(t, os.IsNotExist(err))
}
