//go:build linux

package unitruntime

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const bootstrapFixtureDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func runBootstrapFixture(path string) error {
	plan, err := LoadPlan(path)
	if err != nil {
		return err
	}
	return WithMaintenance(context.Background(), plan.DeploymentRoot, plan.HostID, Options{}, func(guard *Maintenance) error {
		if err := guard.BeginBootstrap(context.Background(), bootstrapFixtureDigest); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "bootstrap-ready")
		<-time.After(30 * time.Second) // Parent sends actual SIGKILL first.
		return fmt.Errorf("bootstrap fixture was not killed by its parent")
	})
}

func TestRuntimeLinuxInterruptedBootstrapBlocksAllUnitsAndScopedRecovery(t *testing.T) {
	path, plan := planFixture(t, "web-host")
	otherPath, other := planFixture(t, "host-agent")
	other.DeploymentRoot = plan.DeploymentRoot
	writeFixture(t, otherPath, other)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0])
	child.Env = append(os.Environ(), "MOOX_RUNTIME_TEST_BOOTSTRAP_PLAN="+path)
	stdout, err := child.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, child.Start())
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "bootstrap-ready\n", ready)
	checked, err := Execute(t.Context(), path, "healthcheck", nil, Options{})
	require.NoError(t, err)
	require.True(t, checked.Skipped, "coordinator holds the real kernel maintenance lock")
	require.NoError(t, child.Process.Kill())
	require.Error(t, child.Wait())
	waited, ok := child.ProcessState.Sys().(syscall.WaitStatus)
	require.True(t, ok)
	require.True(t, waited.Signaled())
	require.Equal(t, syscall.SIGKILL, waited.Signal())
	require.FileExists(t, filepath.Join(plan.DeploymentRoot, "run/bootstrap.json"))
	for _, planPath := range []string{path, otherPath} {
		checked, err := Execute(t.Context(), planPath, "healthcheck", nil, Options{})
		require.NoError(t, err)
		require.True(t, checked.Skipped, "persistent barrier applies to every unit in this host root")
		for _, operation := range []string{"start", "restart", "resume"} {
			_, err := Execute(t.Context(), planPath, operation, nil, Options{BootstrapID: bootstrapFixtureDigest})
			require.ErrorContains(t, err, "interrupted bootstrap", "request identity alone is not an inherited lock")
		}
	}
	_, err = Execute(t.Context(), path, "pause", nil, Options{})
	require.NoError(t, err)
	for _, options := range []Options{{}, {BootstrapID: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}} {
		called := false
		err := WithMaintenance(t.Context(), plan.DeploymentRoot, plan.HostID, options, func(*Maintenance) error { called = true; return nil })
		require.ErrorContains(t, err, "matching coordinator")
		require.False(t, called)
	}
	unit := filepath.Dir(filepath.Dir(plan.ReleaseRoot))
	var expired *Maintenance
	var escaped Options
	err = WithMaintenance(t.Context(), plan.DeploymentRoot, plan.HostID, Options{BootstrapID: bootstrapFixtureDigest}, func(guard *Maintenance) error {
		expired = guard
		require.NoError(t, guard.BeginBootstrap(t.Context(), bootstrapFixtureDigest))
		require.Error(t, guard.BeginBootstrap(t.Context(), "invalid"))
		require.Error(t, guard.BeginBootstrap(t.Context(), "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
		err := guard.UseLock(t.Context(), func(options Options) error {
			escaped = options
			return WithMaintenance(t.Context(), plan.DeploymentRoot, plan.HostID, options, func(inner *Maintenance) error {
				require.NoError(t, inner.BeginInstallation(t.Context(), unit, plan.ReleaseRoot))
				started, err := inner.Execute(t.Context(), path, "start", nil)
				require.NoError(t, err)
				require.Equal(t, "paused", started.Components[0].State)
				return nil
			})
		})
		require.NoError(t, err, "nested libraries must reuse the actual lock without deadlocking")
		_, err = Execute(t.Context(), path, "status", nil, escaped)
		require.ErrorContains(t, err, "expired", "options expire even while the outer maintenance callback is still active")
		require.ErrorContains(t, guard.EndBootstrap(), "unfinished unit installation")
		require.NoError(t, guard.UseLock(t.Context(), func(options Options) error {
			return WithMaintenance(t.Context(), plan.DeploymentRoot, plan.HostID, options, func(inner *Maintenance) error {
				require.NoError(t, inner.BeginInstallation(t.Context(), unit, plan.ReleaseRoot))
				return inner.EndInstallation()
			})
		}))
		require.FileExists(t, filepath.Join(plan.DeploymentRoot, "run/bootstrap.json"), "completing one unit must not clear the host workflow")
		return guard.EndBootstrap()
	})
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(plan.DeploymentRoot, "run/bootstrap.json"))
	require.FileExists(t, filepath.Join(plan.DeploymentRoot, "run/paused/web-host"))
	require.Error(t, expired.UseLock(t.Context(), func(Options) error { return nil }))
	require.Error(t, expired.EndBootstrap())
	_, err = Execute(t.Context(), path, "status", nil, escaped)
	require.Error(t, err, "an escaped closed lock descriptor must fail kernel validation")
}
