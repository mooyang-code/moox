//go:build linux

package unitinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/stretchr/testify/require"
)

func activationCommand(t *testing.T, operation string, args ...string) (Activation, error) {
	t.Helper()
	output, err := exec.CommandContext(t.Context(), os.Getenv("MOOX_RUNTIME_BINARY"), append([]string{operation}, args...)...).CombinedOutput()
	require.NotContains(t, string(output), healthEnvironment()["MOOX_HEALTH_AUTH_SECRET_KEY"])
	if err != nil {
		return Activation{}, fmt.Errorf("installation helper: %w: %s", err, output)
	}
	var result Activation
	require.NoError(t, json.Unmarshal(output, &result))
	return result, nil
}

func controlActivationOptions(t *testing.T) PrepareOptions {
	t.Helper()
	options := realPrepareOptions(t)
	raw, err := os.ReadFile(os.Getenv("MOOX_HOST_MATERIAL_FIXTURE"))
	require.NoError(t, err)
	var issued []struct {
		Options  unitbundle.Options  `json:"options"`
		Metadata hostbundle.Metadata `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(raw, &issued))
	for _, item := range issued {
		if item.Options.HostID == "control" && !item.Options.AllowOperator {
			options.MaterialOptions, options.MaterialDirectory = item.Options, item.Metadata.BundleDir
		}
	}
	require.Equal(t, "control", options.MaterialOptions.HostID)
	host, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	lock, err := os.OpenFile(filepath.Join(options.DeploymentRoot, "run/maintenance.lock"), os.O_RDWR, 0)
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Getenv("MOOX_RUNTIME_BINARY"), "activate", "--directory", host.Directory, "--no-start", "--maintenance-lock-held", "--maintenance-lock-fd", "3")
	command.ExtraFiles = []*os.File{lock}
	output, activateErr := command.CombinedOutput()
	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_UN))
	require.NoError(t, lock.Close())
	require.NoError(t, activateErr, string(output))
	options.HostUnitRoot = options.UnitRoot
	options.UnitRoot = filepath.Join(filepath.Dir(options.UnitRoot), "control unit")
	require.NoError(t, os.Mkdir(options.UnitRoot, 0o700))
	options.Profile = "control"
	options.Archive, options.SHA256 = os.Getenv("MOOX_UNIT_INSTALL_CONTROL_ARCHIVE"), os.Getenv("MOOX_UNIT_INSTALL_CONTROL_SHA256")
	options.Components = []string{"web-host"}
	options.Environment = map[string]map[string]string{"web-host": healthEnvironment()}
	options.Environment["web-host"]["MOOX_WEB_HOST_ADDR"] = "127.0.0.1:0"
	options.Overrides = nil
	return options
}

func cleanupActivation(t *testing.T, prepared Prepared) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, os.Getenv("MOOX_RUNTIME_BINARY"), "stop", "--plan", planPath(prepared)).CombinedOutput()
		require.NoError(t, err, string(output))
	})
}

func currentTarget(t *testing.T, unit string) string {
	t.Helper()
	value, err := os.Readlink(filepath.Join(unit, "current"))
	require.NoError(t, err)
	return value
}

func TestUnitActivationLinuxUsesActualWebHostCopiesStateAndRollsBack(t *testing.T) {
	options := controlActivationOptions(t)
	first, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	cleanupActivation(t, first)
	active, err := activationCommand(t, "activate", "--directory", first.Directory)
	require.NoError(t, err)
	require.Equal(t, "active", active.Phase)
	started := runScript(t, first.Directory, "status").Components[0]
	require.Positive(t, started.PID)
	data := filepath.Join(first.Directory, "web-host/data")
	require.NoError(t, os.Mkdir(data, 0o700))
	file := filepath.Join(data, "snapshot")
	require.NoError(t, os.WriteFile(file, []byte("pre-upgrade-state"), 0o600))
	options.ReleaseID = "second"
	second, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	require.Equal(t, started.PID, runScript(t, first.Directory, "status").Components[0].PID)
	active, err = activationCommand(t, "activate", "--directory", second.Directory)
	require.NoError(t, err)
	require.Equal(t, first.Directory, active.PreviousDirectory)
	require.Equal(t, second.Directory, currentTarget(t, options.UnitRoot))
	require.NotEqual(t, started.PID, runScript(t, second.Directory, "status").Components[0].PID)
	require.NoFileExists(t, "/proc/"+strconv.Itoa(started.PID)+"/exe")
	copyPath := filepath.Join(second.Directory, "web-host/data/snapshot")
	raw, err := os.ReadFile(copyPath)
	require.NoError(t, err)
	require.Equal(t, "pre-upgrade-state", string(raw))
	oldInfo, err := os.Stat(file)
	require.NoError(t, err)
	newInfo, err := os.Stat(copyPath)
	require.NoError(t, err)
	require.False(t, os.SameFile(oldInfo, newInfo))
	require.NoError(t, os.WriteFile(copyPath, []byte("new-release-state"), 0o600))
	_, err = ReadInstalled(t.Context(), second.Directory)
	require.NoError(t, err)
	_, err = ReadPrepared(t.Context(), second.Directory)
	require.Error(t, err)
	rolled, err := activationCommand(t, "rollback", "--unit-root", options.UnitRoot)
	require.NoError(t, err)
	require.Equal(t, "rolled-back", rolled.Phase)
	require.Equal(t, first.Directory, currentTarget(t, options.UnitRoot))
	raw, err = os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "pre-upgrade-state", string(raw))
	raw, err = os.ReadFile(copyPath)
	require.NoError(t, err)
	require.Equal(t, "new-release-state", string(raw))
	runScript(t, first.Directory, "pause")
	options.ReleaseID = "third"
	third, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	_, err = activationCommand(t, "activate", "--directory", third.Directory)
	require.NoError(t, err)
	for _, operation := range []string{"start", "healthcheck"} {
		status := runScript(t, third.Directory, operation).Components[0]
		require.Equal(t, "paused", status.State)
		require.Zero(t, status.PID)
	}
	_, err = activationCommand(t, "activate", "--directory", third.Directory)
	require.NoError(t, err, "completed activation is idempotent with mutable data")
	require.NoFileExists(t, filepath.Join(options.DeploymentRoot, "run/installation.json"))
}

func TestUnitActivationLinuxFailedStartAndInterruptedPhasesRecover(t *testing.T) {
	options := controlActivationOptions(t)
	first, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	cleanupActivation(t, first)
	_, err = activationCommand(t, "activate", "--directory", first.Directory)
	require.NoError(t, err)
	options.ReleaseID = "bad"
	options.Environment["web-host"]["MOOX_WEB_HOST_ADDR"] = "invalid-address"
	failed, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	_, err = activationCommand(t, "activate", "--directory", failed.Directory)
	require.Error(t, err)
	require.Equal(t, first.Directory, currentTarget(t, options.UnitRoot))
	require.Positive(t, runScript(t, first.Directory, "status").Components[0].PID)
	require.NoFileExists(t, filepath.Join(options.DeploymentRoot, "run/installation.json"))
	options.Environment["web-host"]["MOOX_WEB_HOST_ADDR"] = "127.0.0.1:0"
	for _, phase := range []string{"stopping", "copying", "switching", "starting"} {
		t.Run(phase, func(t *testing.T) {
			options.ReleaseID = "interrupted-" + phase
			next, err := prepareWithHelper(t, options)
			require.NoError(t, err)
			err = unitruntime.WithMaintenance(t.Context(), options.DeploymentRoot, first.HostID, unitruntime.Options{}, func(guard *unitruntime.Maintenance) error {
				require.NoError(t, guard.BeginInstallation(t.Context(), options.UnitRoot, next.Directory))
				unit, err := fsutil.OpenPhysicalRoot(options.UnitRoot, false)
				require.NoError(t, err)
				defer unit.Close()
				journal := Activation{Version: 1, HostID: first.HostID, DeploymentRoot: options.DeploymentRoot, UnitRoot: options.UnitRoot, Profile: "control", Directory: next.Directory, PreviousDirectory: first.Directory, StartComponents: []string{"web-host"}, PreviousRunning: []string{"web-host"}}
				require.NoError(t, writeActivation(unit, &journal, phase))
				_, err = guard.Execute(t.Context(), planPath(first), "stop", nil)
				require.NoError(t, err)
				if phase == "switching" || phase == "starting" {
					require.NoError(t, setCurrent(unit, next.Directory))
				}
				return nil // Simulate process death: flock closes, barrier persists.
			})
			require.NoError(t, err)
			require.True(t, runScript(t, next.Directory, "healthcheck").Skipped)
			_, err = activationCommand(t, "recover", "--unit-root", options.UnitRoot)
			require.NoError(t, err)
			require.Equal(t, first.Directory, currentTarget(t, options.UnitRoot))
			require.Positive(t, runScript(t, first.Directory, "status").Components[0].PID)
			require.NoFileExists(t, filepath.Join(options.DeploymentRoot, "run/installation.json"))
		})
	}
}

func TestUnitActivationLinuxRefusesAComponentOwnedByAnotherUnit(t *testing.T) {
	options := controlActivationOptions(t)
	first, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	cleanupActivation(t, first)
	_, err = activationCommand(t, "activate", "--directory", first.Directory)
	require.NoError(t, err)
	pid := runScript(t, first.Directory, "status").Components[0].PID
	options.UnitRoot = filepath.Join(filepath.Dir(options.UnitRoot), "unrelated unit")
	require.NoError(t, os.Mkdir(options.UnitRoot, 0o700))
	other, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	_, err = activationCommand(t, "activate", "--directory", other.Directory)
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(options.UnitRoot, "activation.json"))
	require.Equal(t, pid, runScript(t, first.Directory, "status").Components[0].PID)
}
