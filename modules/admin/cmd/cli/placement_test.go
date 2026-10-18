package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/stretchr/testify/require"
)

func TestTopologyRecoveryProtectsControlAndRejectsInvalidOperations(t *testing.T) {
	opts := newBootstrapFixture(t)
	runBootstrapFixture(t, opts)
	for _, operation := range []struct {
		kind, host, component, status string
	}{
		{"host", "control", "", "disabled"},
		{"placement", "control", "admin", "disabled"},
		{"placement", "control", "web-host", "disabled"},
		{"placement", "control", "console-proxy", "disabled"},
		{"placement", "control", "host-gateway", "disabled"},
		{"placement", "control", "host-agent", "disabled"},
		{"placement", "control", "unknown", "enabled"},
		{"host", "absent", "", "enabled"},
		{"placement", "storage", "collector", "enabled"},
	} {
		args := []string{operation.kind, "set-status", "--db-path", opts.dbPath, "--control-host-id", "control", "--host-id", operation.host, "--status", operation.status}
		if operation.component != "" {
			args = append(args, "--component-id", operation.component)
		}
		require.Error(t, runTopologyRecoveryCommand(args, nil, nil), "%+v", operation)
	}
	db, err := openAdminCLIDB(opts.dbPath)
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	dao, err := sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	hosts, placements, err := dao.Read(context.Background())
	require.NoError(t, err)
	for _, host := range hosts {
		require.Equal(t, "enabled", host.Status)
	}
	for _, placement := range placements {
		require.Equal(t, "enabled", placement.Status)
	}
}

func TestTopologyRecoveryNeverInitializesOrFollowsSymlinkDatabase(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.db")
	args := []string{"host", "set-status", "--db-path", missing, "--control-host-id", "control", "--host-id", "storage", "--status", "enabled"}
	require.Error(t, runTopologyRecoveryCommand(args, nil, nil))
	_, err := os.Lstat(missing)
	require.True(t, os.IsNotExist(err))
	for _, invalid := range [][]string{
		{"host"}, {"host", "unknown"}, {"placement", "set-status"},
		{"host", "set-status", "--host-id", "storage", "--status", "enabled"},
		{"host", "set-status", "--control-host-id", "control", "--host-id", "storage", "--status", "invalid"},
	} {
		require.Error(t, runTopologyRecoveryCommand(invalid, nil, nil))
	}
	require.NoError(t, os.WriteFile(missing, nil, 0o600))
	// An existing empty file remains empty; recovery must not apply schema.
	require.Error(t, runTopologyRecoveryCommand(args, nil, nil))
	db, err := openAdminCLIDB(missing)
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table'").Scan(&count).Error)
	require.Zero(t, count)
	closeAdminCLIDB(db)
	link := filepath.Join(dir, "symlink.db")
	require.NoError(t, os.Symlink(missing, link))
	args[3] = link
	require.ErrorContains(t, runTopologyRecoveryCommand(args, nil, nil), "regular 0600")
	require.True(t, isTopologyRecoveryCommand([]string{"admin-cli", "host"}))
	require.True(t, isTopologyRecoveryCommand([]string{"admin-cli", "placement"}))
	require.False(t, isTopologyRecoveryCommand([]string{"admin-cli", "bootstrap"}))
}
