package command

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupRebuildStorageDefaultsToDryRun(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	var selectedHost, selectedFile, selectedConfigDir string
	var apply bool
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		loadInitBundle: func(configDir string) (setupInitBundle, error) {
			selectedConfigDir = configDir
			return setupInitBundle{}, nil
		},
		validateDeployment: func(_ context.Context, _ *setupconfig.Snapshot, hosts []setupconfig.Host) (setupvalidate.Result, error) {
			assert.Equal(t, []string{"control", "compute"}, []string{hosts[0].Name, hosts[1].Name})
			return setupvalidate.Result{}, nil
		},
		status: func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error) {
			return setupclient.StatusResult{State: "completed"}, nil
		},
		rebuildStorage: func(_ context.Context, _ *setupconfig.Snapshot, host, file, configDir string, selectedApply bool) (storageRebuildSummary, error) {
			selectedHost, selectedFile, selectedConfigDir, apply = host, file, configDir, selectedApply
			return storageRebuildSummary{Status: "dry_run", Host: host, DryRun: true}, nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"rebuild-storage", "--file", "custom.toml", "--config-dir", "seed-dir", "--host", "compute"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, "compute", selectedHost)
	assert.Equal(t, "custom.toml", selectedFile)
	assert.Equal(t, "seed-dir", selectedConfigDir)
	assert.False(t, apply)
	var result map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	assert.Equal(t, "dry_run", result["status"])
}

func TestSetupRebuildStorageYesPassesDestructiveConfirmation(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	var apply bool
	cmd := newSetupCommand(setupDeps{
		load:           func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		loadInitBundle: func(string) (setupInitBundle, error) { return setupInitBundle{}, nil },
		validateDeployment: func(context.Context, *setupconfig.Snapshot, []setupconfig.Host) (setupvalidate.Result, error) {
			return setupvalidate.Result{}, nil
		},
		status: func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error) {
			return setupclient.StatusResult{State: "completed"}, nil
		},
		rebuildStorage: func(_ context.Context, _ *setupconfig.Snapshot, _, _, _ string, selectedApply bool) (storageRebuildSummary, error) {
			apply = selectedApply
			return storageRebuildSummary{Status: "ready", DryRun: false}, nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"rebuild-storage", "--file", "moox.toml", "--host", "compute", "--yes"})
	require.NoError(t, cmd.Execute())
	assert.True(t, apply)
	assert.Contains(t, output.String(), `"status":"ready"`)
}

func TestParseStorageResetOperationResult(t *testing.T) {
	result, err := parseStorageResetOperationResult("log line\n{\"module\":\"storage\",\"action\":\"reset-view-consumers\",\"status\":\"dry_run\",\"summary\":{\"views\":19,\"dry_run\":true}}\n")
	require.NoError(t, err)
	assert.Equal(t, "dry_run", result.Status)
	assert.Equal(t, 19, result.Summary.Views)
	assert.True(t, result.Summary.DryRun)
}

func TestStorageRebuildScriptsPauseAndRestoreHealthchecks(t *testing.T) {
	assert.Contains(t, storageRebuildQuiesceScript, "# moox-healthchecks")
	assert.Contains(t, storageRebuildQuiesceScript, "storage-rebuild-healthchecks.crontab")
	assert.Contains(t, storageRebuildResumeScript, "trap restore_healthchecks EXIT")
	assert.Contains(t, storageRebuildRemoteScript, "trap restore_healthchecks EXIT")
	assert.Contains(t, storageRebuildRemoteScript, "storage-rebuild-healthchecks.crontab")
}

func TestStorageRebuildDoesNotRequireEmptyViewReadiness(t *testing.T) {
	assert.Contains(t, storageRebuildRemoteScript, "moox-storage-primary([[:space:]]|$)")
	assert.Contains(t, storageRebuildRemoteScript, "moox-storage-view([[:space:]]|$)")
	assert.Contains(t, storageRebuildRemoteScript, "moox-storage-node([[:space:]]|$)")
	assert.NotContains(t, storageRebuildRemoteScript, "/readyz")
}

func TestStorageRebuildFinalViewResetPreservesPrimaryData(t *testing.T) {
	assert.Contains(t, storageRebuildFinalizeViewsScript, "--restart=false --yes")
	assert.NotContains(t, storageRebuildFinalizeViewsScript, "--reset-all-storage-data")
	assert.Contains(t, storageRebuildFinalizeViewsScript, "storage-rebuild-view-healthchecks.crontab")
}
