//go:build linux

package unitruntime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRuntimeLinuxScopedMaintenanceSerializesAndBoundsLifecycle(t *testing.T) {
	path, plan := planFixture(t, "web-host")
	otherPath, other := planFixture(t, "web-host")
	var expired *Maintenance
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := WithMaintenance(ctx, plan.DeploymentRoot, plan.HostID, Options{}, func(guard *Maintenance) error {
		expired = guard
		result, err := guard.Execute(ctx, path, "status", nil)
		require.NoError(t, err)
		require.Equal(t, "stopped", result.Components[0].State)
		_, err = guard.Execute(ctx, otherPath, "pause", nil)
		require.Error(t, err)
		require.NoFileExists(t, filepath.Join(other.DeploymentRoot, "run", "paused", "web-host"))
		checked, err := Execute(ctx, path, "healthcheck", nil, Options{})
		require.NoError(t, err)
		require.True(t, checked.Skipped)
		waiting, stop := context.WithTimeout(ctx, 80*time.Millisecond)
		defer stop()
		called := false
		err = WithMaintenance(waiting, plan.DeploymentRoot, plan.HostID, Options{}, func(*Maintenance) error { called = true; return nil })
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.False(t, called)
		_, err = guard.Execute(ctx, path, "pause", nil)
		require.NoError(t, err)
		return nil
	})
	require.NoError(t, err)
	_, err = expired.Execute(t.Context(), path, "resume", nil)
	require.Error(t, err)
	require.FileExists(t, filepath.Join(plan.DeploymentRoot, "run", "paused", "web-host"))
	called := false
	err = WithMaintenance(t.Context(), plan.DeploymentRoot, "other-host", Options{}, func(*Maintenance) error { called = true; return nil })
	require.Error(t, err)
	require.False(t, called)
	stopped, stop := context.WithCancel(t.Context())
	stop()
	err = WithMaintenance(stopped, plan.DeploymentRoot, plan.HostID, Options{}, func(*Maintenance) error { called = true; return nil })
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, called)
	err = WithMaintenance(t.Context(), plan.DeploymentRoot, plan.HostID, Options{}, func(guard *Maintenance) error {
		_, err := guard.Execute(t.Context(), path, "status", nil)
		return err
	})
	require.NoError(t, err, "callback must release the lock even after host/cancellation failures")
}

func TestRuntimeLinuxInterruptedInstallationBlocksAutomaticStartsUntilRecovery(t *testing.T) {
	path, plan := planFixture(t, "web-host")
	unitRoot := filepath.Dir(filepath.Dir(plan.ReleaseRoot))
	err := WithMaintenance(t.Context(), plan.DeploymentRoot, plan.HostID, Options{}, func(guard *Maintenance) error {
		return guard.BeginInstallation(t.Context(), unitRoot, plan.ReleaseRoot)
	})
	require.NoError(t, err)
	checked, err := Execute(t.Context(), path, "healthcheck", nil, Options{})
	require.NoError(t, err)
	require.True(t, checked.Skipped)
	for _, operation := range []string{"start", "restart", "resume"} {
		_, err := Execute(t.Context(), path, operation, nil, Options{})
		require.Error(t, err)
	}
	_, err = Execute(t.Context(), path, "pause", nil, Options{})
	require.NoError(t, err)
	err = WithMaintenance(t.Context(), plan.DeploymentRoot, plan.HostID, Options{}, func(guard *Maintenance) error {
		otherPath, other := planFixture(t, "web-host")
		_ = otherPath
		require.Error(t, guard.BeginInstallation(t.Context(), filepath.Dir(filepath.Dir(other.ReleaseRoot)), other.ReleaseRoot))
		require.NoError(t, guard.BeginInstallation(t.Context(), unitRoot, plan.ReleaseRoot))
		result, err := guard.Execute(t.Context(), path, "start", nil)
		require.NoError(t, err)
		require.Equal(t, "paused", result.Components[0].State)
		return guard.EndInstallation()
	})
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(plan.DeploymentRoot, "run/installation.json"))
	require.FileExists(t, filepath.Join(plan.DeploymentRoot, "run/paused/web-host"))
}
