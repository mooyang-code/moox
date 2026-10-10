//go:build linux

package unitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "exec-component" {
		if err := RunComponentChild(os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(32)
		}
		return
	}
	if os.Getenv("MOOX_RUNTIME_TEST_CHILD") == "1" {
		runFixtureProcess()
		os.Exit(0)
	}
	if raw := os.Getenv("MOOX_RUNTIME_TEST_OPERATION"); raw != "" {
		var input struct {
			Plan string `json:"plan"`
		}
		if json.Unmarshal([]byte(raw), &input) != nil {
			os.Exit(30)
		}
		result, err := Execute(context.Background(), input.Plan, "status", nil, Options{MaintenanceLockHeld: true, MaintenanceLockFD: 3})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(31)
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runFixtureProcess() {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		os.Exit(20)
	}
	component, ok := catalog.Component(os.Getenv("MOOX_SERVICE_NAME"))
	if !ok {
		os.Exit(21)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(component.Health.Port)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "synthetic health listener:", err)
		os.Exit(22)
	}
	if os.Getenv("MOOX_RUNTIME_TEST_RESET_LIVE_ON_BOOT") == "1" {
		_ = os.Remove(os.Getenv("MOOX_RUNTIME_TEST_NOT_LIVE"))
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.Header.Get("X-Moox-Health-Auth"), "/")
		if len(parts) != 5 || parts[0] != os.Getenv("MOOX_HEALTH_AUTH_VERSION") || parts[1] != os.Getenv("MOOX_HEALTH_AUTH_ACCESS_KEY") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		timestamp, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || requestauth.Verify(os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY"), requestauth.Material{Method: r.Method, Path: r.URL.Path, Timestamp: timestamp, Nonce: parts[3]}, parts[4]) != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		marker := os.Getenv("MOOX_RUNTIME_TEST_NOT_READY")
		if r.URL.Path == "/healthz" {
			marker = os.Getenv("MOOX_RUNTIME_TEST_NOT_LIVE")
		}
		_, markerErr := os.Stat(marker)
		ready := marker == "" || os.IsNotExist(markerErr)
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": ready, "binary_sha256": os.Getenv("MOOX_BINARY_SHA256"), "boot_id": os.Getenv("MOOX_BOOT_ID")})
	})
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	if path := os.Getenv("MOOX_RUNTIME_TEST_PROOF"); path != "" {
		raw, _ := json.Marshal(map[string]bool{"operator_leaked": os.Getenv("MOOX_OPERATOR_SHOULD_NOT_LEAK") != ""})
		_ = os.WriteFile(path, raw, 0o600)
	}
	for range term {
		if os.Getenv("MOOX_RUNTIME_TEST_IGNORE_TERM") == "1" {
			continue
		}
		if value := os.Getenv("MOOX_RUNTIME_TEST_TERM_DELAY"); value != "" {
			delay, _ := time.ParseDuration(value)
			time.Sleep(delay)
		}
		_ = server.Close()
		return
	}
}

func executableFixture(t *testing.T, plan Plan) {
	t.Helper()
	source, err := os.Executable()
	require.NoError(t, err)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(plan.ReleaseRoot, "bin"), 0o700))
	for _, component := range plan.Components {
		entry, _ := catalog.Component(component.ID)
		input, err := os.Open(source)
		require.NoError(t, err)
		output, err := os.OpenFile(filepath.Join(plan.ReleaseRoot, "bin", entry.Binary), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		require.NoError(t, err)
		_, copyErr := io.Copy(output, input)
		require.NoError(t, input.Close())
		require.NoError(t, output.Close())
		require.NoError(t, copyErr)
	}
}

func runRuntime(t *testing.T, path, operation string, ids ...string) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if binary := os.Getenv("MOOX_RUNTIME_BINARY"); binary != "" {
		args := []string{operation, "--plan", path}
		if len(ids) > 0 {
			args = append(args, "--components", strings.Join(ids, ","))
		}
		output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		require.NoError(t, err, string(output))
		var result Result
		require.NoError(t, json.Unmarshal(output, &result))
		return result
	}
	result, err := Execute(ctx, path, operation, ids, Options{})
	require.NoError(t, err)
	return result
}

func cleanupRuntime(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_, err := Execute(ctx, path, "stop", nil, Options{})
		require.NoError(t, err)
	})
}

func TestRuntimeLinuxProcessIdentityRemainsCoherentAcrossExec(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	paths := []string{filepath.Join(root, "exec-first"), filepath.Join(root, "exec-second")}
	for _, path := range paths {
		input, err := os.Open("/bin/sh")
		require.NoError(t, err)
		output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		require.NoError(t, err)
		_, copyErr := io.Copy(output, input)
		require.NoError(t, errors.Join(copyErr, input.Close(), output.Close()))
	}
	// Continuously exec between two different inodes, keeping PID/start ticks.
	// Every observation must describe one executable, never a mixed path/inode.
	script := `exec "$1" -c "$0" "$0" "$2" "$1"`
	command := exec.Command(paths[0], "-c", script, script, paths[1], paths[0])
	require.NoError(t, command.Start())
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	seen := map[string]bool{}
	for range 2000 {
		identity, alive, err := readProcess(command.Process.Pid)
		require.NoError(t, err)
		require.True(t, alive)
		require.Contains(t, paths, identity.Executable)
		info, err := os.Stat(identity.Executable)
		require.NoError(t, err)
		stat := info.Sys().(*syscall.Stat_t)
		require.Equal(t, uint64(stat.Dev), identity.Device)
		require.Equal(t, stat.Ino, identity.Inode, "path and inode must belong to the same exec image")
		seen[identity.Executable] = true
	}
	require.Len(t, seen, 2)
}

func TestRuntimeLinuxPausePersistsAcrossReleaseAndAllAutomaticStarts(t *testing.T) {
	path, plan := planFixture(t, "console-proxy")
	proxyBudgetFixture(t, plan.ReleaseRoot, "1s", "1s", "1s", "5s")
	executableFixture(t, plan)
	cleanupRuntime(t, path)
	started := runRuntime(t, path, "start")
	require.True(t, started.Components[0].Ready)
	paused := runRuntime(t, path, "pause")
	require.Equal(t, "paused", paused.Components[0].State)
	for _, action := range []string{"start", "restart", "healthcheck"} {
		result := runRuntime(t, path, action)
		require.Equal(t, "paused", result.Components[0].State)
		require.Zero(t, result.Components[0].PID)
	}
	newPath, next := planFixture(t, "console-proxy")
	next.DeploymentRoot = plan.DeploymentRoot
	writeFixture(t, newPath, next)
	proxyBudgetFixture(t, next.ReleaseRoot, "1s", "1s", "1s", "5s")
	executableFixture(t, next)
	cleanupRuntime(t, newPath)
	require.Equal(t, "paused", runRuntime(t, newPath, "start").Components[0].State)
	require.Equal(t, "paused", runRuntime(t, newPath, "healthcheck").Components[0].State)
	resumed := runRuntime(t, newPath, "resume")
	require.True(t, resumed.Components[0].Ready)
	require.NotEqual(t, started.Components[0].PID, resumed.Components[0].PID)
	var record pidRecord
	require.NoError(t, privateJSON(filepath.Join(plan.DeploymentRoot, "run", "pids", "console-proxy.json"), &record))
	require.Equal(t, next.ReleaseRoot, record.ReleaseRoot)
}

func TestRuntimeLinuxUsesRunningReleaseBudgetBeforeForceAndKeepsDrainOnCancellation(t *testing.T) {
	path, plan := planFixture(t, "console-proxy")
	proxyBudgetFixture(t, plan.ReleaseRoot, "1s", "1s", "1s", "5s")
	values := fixtureEnvironment()
	values["MOOX_RUNTIME_TEST_TERM_DELAY"] = "1200ms"
	writeFixture(t, plan.Components[0].EnvironmentFile, values)
	executableFixture(t, plan)
	cleanupRuntime(t, path)
	runRuntime(t, path, "start")
	// A new release with a shorter budget must not shorten the running
	// process's recorded drain + engine + cleanup allowance.
	newPath, next := planFixture(t, "console-proxy")
	next.DeploymentRoot = plan.DeploymentRoot
	writeFixture(t, newPath, next)
	proxyBudgetFixture(t, next.ReleaseRoot, "1ms", "1ms", "1ms", "5s")
	before := time.Now()
	stopped := runRuntime(t, newPath, "stop")
	require.False(t, stopped.Components[0].Forced)
	require.GreaterOrEqual(t, time.Since(before), 1100*time.Millisecond)
	runRuntime(t, path, "start")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err := Execute(ctx, path, "stop", nil, Options{})
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, "draining", runRuntime(t, path, "healthcheck").Components[0].State)
	require.Eventually(t, func() bool {
		state, err := openRuntime(plan)
		require.NoError(t, err)
		defer state.close()
		_, alive, err := state.record(plan.Components[0])
		require.NoError(t, err)
		return !alive
	}, 3*time.Second, 50*time.Millisecond)
	require.Equal(t, "draining", runRuntime(t, path, "healthcheck").Components[0].State)
	runRuntime(t, path, "stop")
	require.True(t, runRuntime(t, path, "start").Components[0].Ready)
}

func TestRuntimeLinuxForceWaitsFullBudgetAndObservesExit(t *testing.T) {
	path, plan := planFixture(t, "console-proxy")
	proxyBudgetFixture(t, plan.ReleaseRoot, "100ms", "100ms", "100ms", "5s")
	values := fixtureEnvironment()
	values["MOOX_RUNTIME_TEST_IGNORE_TERM"] = "1"
	writeFixture(t, plan.Components[0].EnvironmentFile, values)
	executableFixture(t, plan)
	cleanupRuntime(t, path)
	started := runRuntime(t, path, "start")
	before := time.Now()
	stopped := runRuntime(t, path, "stop")
	require.GreaterOrEqual(t, time.Since(before), 300*time.Millisecond)
	require.True(t, stopped.Components[0].Forced)
	_, alive, err := readProcess(started.Components[0].PID)
	require.NoError(t, err)
	require.False(t, alive)
	require.NoFileExists(t, filepath.Join(plan.DeploymentRoot, "run", "pids", "console-proxy.json"))
	require.NoFileExists(t, filepath.Join(plan.DeploymentRoot, "run", "draining", "console-proxy"))
}

func TestRuntimeLinuxWatchdogUsesLivenessAndDoesNotRestartForReadinessOrBadCredentials(t *testing.T) {
	path, plan := planFixture(t, "web-host")
	values := fixtureEnvironment()
	marker := filepath.Join(plan.ReleaseRoot, "not-ready")
	values["MOOX_RUNTIME_TEST_NOT_READY"] = marker
	writeFixture(t, plan.Components[0].EnvironmentFile, values)
	executableFixture(t, plan)
	cleanupRuntime(t, path)
	started := runRuntime(t, path, "start")
	require.NoError(t, os.WriteFile(marker, []byte("not-ready"), 0o600))
	checked := runRuntime(t, path, "healthcheck")
	require.False(t, checked.Components[0].Ready)
	require.Equal(t, started.Components[0].PID, checked.Components[0].PID)
	values["MOOX_HEALTH_AUTH_SECRET_KEY"] = "different-fixture-health-signing-key"
	writeFixture(t, plan.Components[0].EnvironmentFile, values)
	_, err := Execute(context.Background(), path, "healthcheck", nil, Options{})
	require.ErrorIs(t, err, errHealthIdentity)
	require.Equal(t, started.Components[0].PID, runRuntime(t, path, "status").Components[0].PID)
	values["MOOX_HEALTH_AUTH_SECRET_KEY"] = fixtureEnvironment()["MOOX_HEALTH_AUTH_SECRET_KEY"]
	writeFixture(t, plan.Components[0].EnvironmentFile, values)
	require.NoError(t, os.Remove(marker))
	require.True(t, runRuntime(t, path, "healthcheck").Components[0].Ready)
	runRuntime(t, path, "stop")
	restarted := runRuntime(t, path, "healthcheck")
	require.True(t, restarted.Components[0].Ready)
	require.NotEqual(t, started.Components[0].PID, restarted.Components[0].PID)
}

func TestRuntimeLinuxMaintenanceLockSkipCancellationAndInheritedDescriptor(t *testing.T) {
	path, plan := planFixture(t, "web-host")
	state, err := openRuntime(plan)
	require.NoError(t, err)
	defer state.close()
	lock, _, err := acquireLock(context.Background(), state.run, false, 0, false)
	require.NoError(t, err)
	defer lock.Close()
	checked := runRuntime(t, path, "healthcheck")
	require.True(t, checked.Skipped)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err = Execute(ctx, path, "pause", nil, Options{})
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoFileExists(t, filepath.Join(plan.DeploymentRoot, "run", "paused", "web-host"))
	result, err := Execute(context.Background(), path, "status", nil, Options{MaintenanceLockHeld: true, MaintenanceLockFD: int(lock.Fd())})
	require.NoError(t, err)
	require.Equal(t, "stopped", result.Components[0].State)
	_, err = Execute(context.Background(), path, "status", nil, Options{MaintenanceLockHeld: true, MaintenanceLockFD: 0})
	require.Error(t, err)
	input, err := json.Marshal(map[string]string{"plan": path})
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable)
	command.ExtraFiles = []*os.File{lock}
	command.Env = append(os.Environ(), "MOOX_RUNTIME_TEST_OPERATION="+string(input))
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.NoError(t, json.Unmarshal(output, &result))
	require.Equal(t, "stopped", result.Components[0].State)
}

func TestRuntimeLinuxWatchdogRestartsFailedLivenessWithRunningGraceBudget(t *testing.T) {
	path, plan := planFixture(t, "console-proxy")
	proxyBudgetFixture(t, plan.ReleaseRoot, "1s", "1s", "1s", "5s")
	values := fixtureEnvironment()
	marker := filepath.Join(plan.ReleaseRoot, "not-live")
	values["MOOX_RUNTIME_TEST_NOT_LIVE"] = marker
	values["MOOX_RUNTIME_TEST_RESET_LIVE_ON_BOOT"] = "1"
	values["MOOX_RUNTIME_TEST_TERM_DELAY"] = "1200ms"
	writeFixture(t, plan.Components[0].EnvironmentFile, values)
	executableFixture(t, plan)
	cleanupRuntime(t, path)
	started := runRuntime(t, path, "start")
	require.NoError(t, os.WriteFile(marker, []byte("not-live"), 0o600))
	before := time.Now()
	checked := runRuntime(t, path, "healthcheck")
	require.GreaterOrEqual(t, time.Since(before), 1100*time.Millisecond)
	require.NotEqual(t, started.Components[0].PID, checked.Components[0].PID)
	require.True(t, checked.Components[0].Ready)
	require.NoFileExists(t, marker)
}

func TestRuntimeLinuxCancelledPauseMarksAllTargetsAndHostIdentityIsPersistent(t *testing.T) {
	path, plan := planFixture(t, "web-host", "console-proxy")
	proxyBudgetFixture(t, plan.ReleaseRoot, "1s", "1s", "1s", "5s")
	values := fixtureEnvironment()
	values["MOOX_RUNTIME_TEST_TERM_DELAY"] = "500ms"
	writeFixture(t, plan.Components[1].EnvironmentFile, values)
	executableFixture(t, plan)
	cleanupRuntime(t, path)
	runRuntime(t, path, "start")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err := Execute(ctx, path, "pause", nil, Options{})
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	for _, id := range []string{"web-host", "console-proxy"} {
		require.FileExists(t, filepath.Join(plan.DeploymentRoot, "run", "paused", id))
		require.Equal(t, "paused", runRuntime(t, path, "healthcheck", id).Components[0].State)
	}
	runRuntime(t, path, "stop")
	changed := plan
	changed.HostID = "different-host"
	writeFixture(t, path, changed)
	_, err = Execute(context.Background(), path, "status", nil, Options{})
	require.ErrorContains(t, err, "different host identity")
	writeFixture(t, path, plan)
}

func TestRuntimeLinuxRefusesUnrelatedOrReusedPIDAndDoesNotExposeSecrets(t *testing.T) {
	t.Setenv("MOOX_OPERATOR_SHOULD_NOT_LEAK", "fixture-operator-secret")
	path, plan := planFixture(t, "web-host")
	values := fixtureEnvironment()
	proof := filepath.Join(plan.ReleaseRoot, "child-proof.json")
	values["MOOX_RUNTIME_TEST_PROOF"] = proof
	writeFixture(t, plan.Components[0].EnvironmentFile, values)
	executableFixture(t, plan)
	cleanupRuntime(t, path)
	started := runRuntime(t, path, "start")
	require.Eventually(t, func() bool { _, err := os.Stat(proof); return err == nil }, time.Second, 10*time.Millisecond)
	var childProof map[string]bool
	require.NoError(t, privateJSON(proof, &childProof))
	require.False(t, childProof["operator_leaked"])
	arguments, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", started.Components[0].PID))
	require.NoError(t, err)
	require.NotContains(t, string(arguments), values["MOOX_HEALTH_AUTH_SECRET_KEY"])
	metadata, err := json.Marshal(started)
	require.NoError(t, err)
	require.NotContains(t, string(metadata), values["MOOX_HEALTH_AUTH_SECRET_KEY"])
	state, err := openRuntime(plan)
	require.NoError(t, err)
	defer state.close()
	actual, alive, err := state.record(plan.Components[0])
	require.NoError(t, err)
	require.True(t, alive)
	wrong := actual
	wrong.StartTicks = "1"
	require.NoError(t, writeState(state.pids, "web-host.json", wrong))
	_, err = Execute(context.Background(), path, "stop", nil, Options{})
	require.ErrorIs(t, err, errIdentity)
	_, alive, err = readProcess(actual.PID)
	require.NoError(t, err)
	require.True(t, alive)
	require.NoError(t, writeState(state.pids, "web-host.json", actual))
	runRuntime(t, path, "stop")
	sleep, err := os.ReadFile("/bin/sleep")
	require.NoError(t, err)
	unrelatedPath := filepath.Join(plan.ReleaseRoot, "unrelated-sleep")
	require.NoError(t, os.WriteFile(unrelatedPath, sleep, 0o755))
	unrelated := exec.Command(unrelatedPath, "30")
	require.NoError(t, unrelated.Start())
	defer func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() }()
	identity, alive, err := readProcess(unrelated.Process.Pid)
	require.NoError(t, err)
	require.True(t, alive)
	wrong = actual
	wrong.processIdentity = identity
	require.NoError(t, writeState(state.pids, "web-host.json", wrong))
	_, err = Execute(context.Background(), path, "stop", nil, Options{})
	require.ErrorIs(t, err, errIdentity)
	require.NoError(t, unrelated.Process.Signal(syscall.Signal(0)))
	require.NoError(t, removeState(state.pids, "web-host.json"))
}

func TestRuntimeLinuxStartBarrierRejectsUnrecordedChildAndRecoversDurableLaunch(t *testing.T) {
	for _, phase := range []string{"unrecorded-parent-exit", "recorded-before-exec", "recorded-after-exec"} {
		t.Run(phase, func(t *testing.T) {
			path, plan := planFixture(t, "console-proxy")
			proxyBudgetFixture(t, plan.ReleaseRoot, "100ms", "100ms", "100ms", "5s")
			executableFixture(t, plan)
			cleanupRuntime(t, path)
			binary := os.Getenv("MOOX_RUNTIME_BINARY")
			if binary == "" {
				var err error
				binary, err = os.Executable()
				require.NoError(t, err)
			}
			gate, release, err := os.Pipe()
			require.NoError(t, err)
			defer release.Close()
			defer gate.Close()
			child := exec.Command(binary, "exec-component", path, "console-proxy")
			child.ExtraFiles = []*os.File{gate}
			child.Env = componentEnvironment(fixtureEnvironment())
			require.NoError(t, child.Start())
			defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
			require.NoError(t, gate.Close())
			if phase == "unrecorded-parent-exit" {
				// Closing the only writer is exactly what the kernel does when
				// the parent dies before it has durably recorded the launch.
				require.NoError(t, release.Close())
				require.Eventually(t, func() bool {
					_, alive, err := readProcess(child.Process.Pid)
					require.NoError(t, err)
					return !alive
				}, 3*time.Second, 10*time.Millisecond)
				require.Equal(t, "stopped", runRuntime(t, path, "status").Components[0].State)
				return
			}
			state, err := openRuntime(plan)
			require.NoError(t, err)
			defer state.close()
			launch, alive, err := readProcess(child.Process.Pid)
			require.NoError(t, err)
			require.True(t, alive)
			final, err := releaseIdentity(launch, filepath.Join(plan.ReleaseRoot, "bin", "moox-console-proxy"))
			require.NoError(t, err)
			values, stop, startup, err := state.prepare(plan.Components[0])
			require.NoError(t, err)
			record := pidRecord{processIdentity: final, HostID: plan.HostID, ComponentID: "console-proxy", ReleaseRoot: plan.ReleaseRoot, StopTimeout: stop, BinarySHA256: values["MOOX_BINARY_SHA256"], BootID: strings.Repeat("a", 64), StartedAt: time.Now().UTC(), StartupTimeout: startup, Launcher: &launch}
			require.NoError(t, writeState(state.pids, "console-proxy.json", record))
			if phase == "recorded-after-exec" {
				_, err = release.Write([]byte{1})
				require.NoError(t, err)
				require.NoError(t, release.Close())
				// A fresh helper takes over a launch whose original parent never
				// observed readiness or removed the launcher identity.
				status := runRuntime(t, path, "healthcheck")
				require.True(t, status.Components[0].Ready)
				require.Equal(t, child.Process.Pid, status.Components[0].PID)
				require.True(t, runRuntime(t, path, "start").Components[0].Ready)
				recovered, alive, err := state.record(plan.Components[0])
				require.NoError(t, err)
				require.True(t, alive)
				require.Nil(t, recovered.Launcher)
			}
			stopped := runRuntime(t, path, "stop")
			require.Equal(t, "stopped", stopped.Components[0].State)
			require.False(t, stopped.Components[0].Forced)
			_, alive, err = readProcess(child.Process.Pid)
			require.NoError(t, err)
			require.False(t, alive)
		})
	}
}
