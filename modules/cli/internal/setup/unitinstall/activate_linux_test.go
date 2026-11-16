//go:build linux

package unitinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	require.NotContains(t, string(output), syntheticEnvironment()["MOOX_SYNTHETIC_SECRET"])
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
	// T10 之后控制机上没有不依赖别的组件就能起来的进程，这里用真实 Admin：
	// 首次激活经状态导入带上离线 bootstrap 出的数据库（见 activateFirstAdmin），主密钥与 CA 放在发布之外。
	trust := privateParent(t)
	admin := syntheticEnvironment()
	admin["MOOX_ADMIN_NODE_ID"] = "control"
	admin["MOOX_ADMIN_ENCRYPTION_KEY_FILE"] = filepath.Join(trust, "admin-master.key")
	admin["MOOX_ADMIN_PKI_DIR"] = filepath.Join(trust, "pki")
	admin["MOOX_ADMIN_DB_PATH"] = "./data/admin.db"
	admin["MOOX_ADMIN_WEB_LISTEN"] = "127.0.0.1:0"
	admin["MOOX_ADMIN_WEB_ADDRESS"] = "127.0.0.1"
	options.Components = []string{"admin"}
	options.Environment = map[string]map[string]string{"admin": admin}
	options.Overrides = nil
	return options
}

// activateFirstAdmin 首次激活 control 单元：和五步 bootstrap 一样，先用发布自带的 moox-admin-cli 离线建库，
// 封存成状态导入再激活，Admin 才能起来。之后的发布从上一版复制 admin/data。
func activateFirstAdmin(t *testing.T, options PrepareOptions, first Prepared) (Activation, error) {
	t.Helper()
	imports := filepath.Join(options.UnitRoot, "state-imports")
	require.NoError(t, os.MkdirAll(imports, 0o700))
	seed := filepath.Join(imports, filepath.Base(first.Directory))
	require.NoError(t, os.MkdirAll(filepath.Join(seed, "admin/data"), 0o700))
	environment := options.Environment["admin"]
	bootstrapOfflineAdmin(t, filepath.Join(first.Directory, "bin/moox-admin-cli"), filepath.Join(seed, "admin/data/admin.db"), environment["MOOX_ADMIN_ENCRYPTION_KEY_FILE"], environment["MOOX_ADMIN_PKI_DIR"], "initial")
	reference, err := sealWithHelper(t, SealStateOptions{Directory: seed, ReleaseDirectory: first.Directory, Paths: []string{"admin/data"}})
	require.NoError(t, err)
	return activationCommand(t, "activate", "--directory", first.Directory, "--state-seed", reference.Directory, "--state-seed-sha256", reference.SHA256)
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

func TestUnitActivationLinuxCompositeAbortRemovesOnlyItsFirstInstallation(t *testing.T) {
	options := controlActivationOptions(t)
	first, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	cleanupActivation(t, first)
	_, err = activateFirstAdmin(t, options, first)
	require.NoError(t, err)
	_, err = activationCommand(t, "rollback", "--unit-root", options.UnitRoot)
	require.Error(t, err, "operator rollback still requires an older snapshot")
	options.ReleaseID = "unrelated-candidate"
	unrelated, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	_, err = Abort(t.Context(), unrelated.Directory, unitruntime.Options{})
	require.Error(t, err)
	require.Equal(t, first.Directory, currentTarget(t, options.UnitRoot))
	_, err = Abort(t.Context(), first.Directory, unitruntime.Options{})
	require.NoError(t, err)
	_, err = os.Lstat(filepath.Join(options.UnitRoot, "current"))
	require.True(t, os.IsNotExist(err))
	_, err = ReadInstalled(t.Context(), first.Directory)
	require.NoError(t, err, "retired first-installation data is retained")
	_, err = Abort(t.Context(), first.Directory, unitruntime.Options{})
	require.NoError(t, err, "composite recovery is repeatable")
}

func TestUnitActivationLinuxUsesActualAdminCopiesStateAndRollsBack(t *testing.T) {
	options := controlActivationOptions(t)
	first, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	cleanupActivation(t, first)
	active, err := activateFirstAdmin(t, options, first)
	require.NoError(t, err)
	require.Equal(t, "active", active.Phase)
	started := runScript(t, first.Directory, "status").Components[0]
	require.Positive(t, started.PID)
	// admin/data 已由状态导入建好，这里再放一个文件，核对升级时独立复制、回滚时各自保留。
	file := filepath.Join(first.Directory, "admin/data/snapshot")
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
	copyPath := filepath.Join(second.Directory, "admin/data/snapshot")
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

// ageReleaseCatalog 把一个已安装的发布改造成“由上一个 runtime 版本打包”：自带的服务目录换成另一份（组件相同），
// 包清单、运行计划与准备回执都记录这份目录，和 runtime 升级前生产上的发布一样。
func ageReleaseCatalog(t *testing.T, prepared Prepared) {
	t.Helper()
	catalogPath := filepath.Join(prepared.Directory, "config/servicecatalog/catalog.yaml")
	raw, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	older := append(raw, []byte("\n# packaged by an older runtime release\n")...)
	require.NoError(t, os.WriteFile(catalogPath, older, 0o644))
	sum := sha256.Sum256(older)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	rewrite := func(name string, change func(map[string]any)) {
		path := filepath.Join(prepared.Directory, name)
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		var document map[string]any
		require.NoError(t, json.Unmarshal(raw, &document))
		change(document)
		raw, err = json.Marshal(document)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(raw, '\n'), 0))
	}
	rewrite("package-manifest.json", func(manifest map[string]any) {
		manifest["catalog_sha256"] = digest
		for _, item := range manifest["files"].([]any) {
			if file := item.(map[string]any); file["path"] == "config/servicecatalog/catalog.yaml" {
				file["sha256"], file["size"] = digest, len(older)
			}
		}
	})
	rewrite("runtime.json", func(plan map[string]any) { plan["catalog_sha256"] = digest })

	root, err := fsutil.OpenPhysicalRoot(prepared.Directory, false)
	require.NoError(t, err)
	defer root.Close()
	prepared.Files, err = inventory(t.Context(), root, prepared, true)
	require.NoError(t, err)
	receipt, err := json.Marshal(prepared)
	require.NoError(t, err)
	require.NoError(t, fsutil.WritePrivate(root, "prepared.json", append(receipt, '\n'), true))
}

// runtime 跨服务目录变更升级：当前发布是旧目录打包的，新 runtime 仍能读取它、在激活新发布时停止它、回滚时再启用它；
// 它不能再作为候选发布。
func TestUnitActivationLinuxReplacesAndRollsBackAcrossServiceCatalogChange(t *testing.T) {
	options := controlActivationOptions(t)
	first, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	cleanupActivation(t, first)
	_, err = activateFirstAdmin(t, options, first)
	require.NoError(t, err)
	started := runScript(t, first.Directory, "status").Components[0]
	require.Positive(t, started.PID)

	ageReleaseCatalog(t, first)
	_, err = ReadInstalled(t.Context(), first.Directory)
	require.NoError(t, err)
	_, err = ReadPrepared(t.Context(), first.Directory)
	require.ErrorContains(t, err, "candidate release must use this runtime's service catalog")
	require.Equal(t, started.PID, runScript(t, first.Directory, "status").Components[0].PID)

	options.ReleaseID = "second"
	second, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	active, err := activationCommand(t, "activate", "--directory", second.Directory)
	require.NoError(t, err)
	require.Equal(t, first.Directory, active.PreviousDirectory)
	require.Equal(t, second.Directory, currentTarget(t, options.UnitRoot))
	require.NoFileExists(t, "/proc/"+strconv.Itoa(started.PID)+"/exe")

	rolled, err := activationCommand(t, "rollback", "--unit-root", options.UnitRoot)
	require.NoError(t, err)
	require.Equal(t, "rolled-back", rolled.Phase)
	require.Equal(t, first.Directory, currentTarget(t, options.UnitRoot))
	require.Positive(t, runScript(t, first.Directory, "status").Components[0].PID)
}

func TestUnitActivationLinuxFailedStartAndInterruptedPhasesRecover(t *testing.T) {
	options := controlActivationOptions(t)
	first, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	cleanupActivation(t, first)
	_, err = activateFirstAdmin(t, options, first)
	require.NoError(t, err)
	options.ReleaseID = "bad"
	options.Environment["admin"]["MOOX_ADMIN_WEB_LISTEN"] = "invalid-address"
	failed, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	_, err = activationCommand(t, "activate", "--directory", failed.Directory)
	require.Error(t, err)
	require.Equal(t, first.Directory, currentTarget(t, options.UnitRoot))
	require.Positive(t, runScript(t, first.Directory, "status").Components[0].PID)
	require.NoFileExists(t, filepath.Join(options.DeploymentRoot, "run/installation.json"))
	options.Environment["admin"]["MOOX_ADMIN_WEB_LISTEN"] = "127.0.0.1:0"
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
				journal := Activation{Version: 1, HostID: first.HostID, DeploymentRoot: options.DeploymentRoot, UnitRoot: options.UnitRoot, Profile: "control", Directory: next.Directory, PreviousDirectory: first.Directory, StartComponents: []string{"admin"}, PreviousRunning: []string{"admin"}}
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
	_, err = activateFirstAdmin(t, options, first)
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
