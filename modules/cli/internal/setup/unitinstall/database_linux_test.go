//go:build linux

package unitinstall

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/stretchr/testify/require"
)

func databaseCandidate(t *testing.T, flag string) Prepared {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	candidate := Prepared{HostID: "storage1", Profile: "storage", DeploymentRoot: root, UnitRoot: filepath.Join(root, "storage"), Directory: filepath.Join(root, "storage/releases/one"), Components: []string{"storage-primary"}}
	for _, name := range []string{"bin", "secrets", "storage-primary"} {
		require.NoError(t, os.MkdirAll(filepath.Join(candidate.Directory, name), 0o700))
	}
	env := map[string]string{"MOOX_HEALTH_AUTH_VERSION": "moox-health-v1", "MOOX_HEALTH_AUTH_ACCESS_KEY": "synthetic-storage-health", "MOOX_HEALTH_AUTH_SECRET_KEY": "synthetic-storage-health-secret-long-enough", "MOOX_TEST_INIT": flag}
	raw, err := json.Marshal(env)
	require.NoError(t, err)
	envFile := filepath.Join(candidate.Directory, "secrets/runtime-storage-primary.json")
	require.NoError(t, os.WriteFile(envFile, raw, 0o600))
	plan := unitruntime.Plan{Version: 1, HostID: candidate.HostID, DeploymentRoot: root, ReleaseRoot: candidate.Directory, CatalogSHA256: unitruntime.CatalogSHA256(), Components: []unitruntime.Component{{ID: "storage-primary", EnvironmentFile: envFile}}}
	raw, err = json.Marshal(plan)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(planPath(candidate), raw, 0o600))
	script := "#!/bin/sh\nset -eu\ntest -e /proc/$$/fd/3\nmkdir -p var/storage/metadata\nprintf '%s' candidate-only > var/storage/metadata/storage_metadata.db\nprintf '%s' $$ > var/storage/metadata/helper.pid\necho synthetic-private-child-output >&2\nif [ \"$MOOX_TEST_INIT\" = fail ]; then exit 9; fi\nif [ \"$MOOX_TEST_INIT\" = block ]; then exec sleep 30; fi\n"
	require.NoError(t, os.WriteFile(filepath.Join(candidate.Directory, "bin/moox-storage-cli"), []byte(script), 0o755))
	return candidate
}

func TestStorageCandidateInitializationKeepsLockAndRedactsFailedHelper(t *testing.T) {
	for _, flag := range []string{"success", "fail"} {
		t.Run(flag, func(t *testing.T) {
			candidate := databaseCandidate(t, flag)
			err := unitruntime.WithMaintenance(t.Context(), candidate.DeploymentRoot, candidate.HostID, unitruntime.Options{}, func(guard *unitruntime.Maintenance) error {
				return initializeCandidateDatabase(t.Context(), guard, candidate)
			})
			if flag == "success" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "synthetic-private-child-output")
			}
			raw, readErr := os.ReadFile(filepath.Join(candidate.Directory, "storage-primary/var/storage/metadata/storage_metadata.db"))
			require.NoError(t, readErr)
			require.Equal(t, "candidate-only", string(raw))
		})
	}
}

func TestStorageCandidateInitializationCancellationReapsWriterBeforeUnlock(t *testing.T) {
	candidate := databaseCandidate(t, "block")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err := unitruntime.WithMaintenance(ctx, candidate.DeploymentRoot, candidate.HostID, unitruntime.Options{}, func(guard *unitruntime.Maintenance) error { return initializeCandidateDatabase(ctx, guard, candidate) })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	raw, err := os.ReadFile(filepath.Join(candidate.Directory, "storage-primary/var/storage/metadata/helper.pid"))
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH)
	lock, err := os.OpenFile(filepath.Join(candidate.DeploymentRoot, "run/maintenance.lock"), os.O_RDWR, 0)
	require.NoError(t, err)
	defer lock.Close()
	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
}

func TestStorageCandidateInitializationRejectsEnvironmentEscapeBeforeWriter(t *testing.T) {
	for _, extra := range []map[string]string{{"LD_PRELOAD": "/synthetic/private-marker"}, {"MOOX_STORAGE_HOME": "/synthetic/private-marker"}} {
		candidate := databaseCandidate(t, "success")
		filename := filepath.Join(candidate.Directory, "secrets/runtime-storage-primary.json")
		raw, err := os.ReadFile(filename)
		require.NoError(t, err)
		var values map[string]string
		require.NoError(t, json.Unmarshal(raw, &values))
		for key, value := range extra {
			values[key] = value
		}
		raw, err = json.Marshal(values)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filename, raw, 0o600))
		err = unitruntime.WithMaintenance(t.Context(), candidate.DeploymentRoot, candidate.HostID, unitruntime.Options{}, func(guard *unitruntime.Maintenance) error {
			return initializeCandidateDatabase(t.Context(), guard, candidate)
		})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "private-marker")
		require.NoFileExists(t, filepath.Join(candidate.Directory, "storage-primary/var/storage/metadata/helper.pid"))
	}
}
