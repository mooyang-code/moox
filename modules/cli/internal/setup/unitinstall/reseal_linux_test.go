//go:build linux

package unitinstall

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func resealCommand(t *testing.T, args ...string) (ResealResult, error) {
	t.Helper()
	output, err := exec.CommandContext(t.Context(), os.Getenv("MOOX_RUNTIME_BINARY"), append([]string{"reseal"}, args...)...).CombinedOutput()
	if err != nil {
		return ResealResult{}, err
	}
	var result ResealResult
	require.NoError(t, json.Unmarshal(output, &result), string(output))
	return result, nil
}

// 原地改过的已安装发布（模拟 native-release 换二进制、手工改配置）：预览只列差异不改动，确认后清单改成现状并重新通过安装校验。
func TestUnitResealLinuxSealsInPlaceChangesOfTheCurrentRelease(t *testing.T) {
	options := realPrepareOptions(t)
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
	_, err = ReadInstalled(t.Context(), host.Directory)
	require.NoError(t, err)

	config := filepath.Join(host.Directory, "host-agent/config/app.yaml")
	original, err := os.ReadFile(config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(config, append(original, []byte("# edited in place\n")...), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(host.Directory, "host-agent/config/extra.yaml"), []byte("extra: true\n"), 0o600))
	_, err = ReadInstalled(t.Context(), host.Directory)
	require.Error(t, err, "原地改动后清单校验应失败")
	receipt, err := os.ReadFile(filepath.Join(host.Directory, "prepared.json"))
	require.NoError(t, err)

	preview, err := resealCommand(t, "--unit-root", options.UnitRoot)
	require.NoError(t, err)
	require.False(t, preview.Resealed)
	require.Equal(t, filepath.Base(host.Directory), preview.Release)
	require.Equal(t, []string{"host-agent/config/extra.yaml"}, preview.Added)
	require.Equal(t, []string{"host-agent/config/app.yaml"}, preview.Changed)
	require.Empty(t, preview.Removed)
	unchanged, err := os.ReadFile(filepath.Join(host.Directory, "prepared.json"))
	require.NoError(t, err)
	require.Equal(t, receipt, unchanged, "预览不能改动清单")

	sealed, err := resealCommand(t, "--unit-root", options.UnitRoot, "--yes")
	require.NoError(t, err)
	require.True(t, sealed.Resealed)
	backup, err := os.ReadFile(sealed.Backup)
	require.NoError(t, err)
	require.Equal(t, receipt, backup, "改写前的清单原样留在 reseal-backups/")
	installed, err := ReadInstalled(t.Context(), host.Directory)
	require.NoError(t, err)
	require.Equal(t, host.Identity, installed.Identity, "清单以外的收据字段不变")

	again, err := resealCommand(t, "--unit-root", options.UnitRoot)
	require.NoError(t, err)
	require.Empty(t, again.Added)
	require.Empty(t, again.Changed)
	require.False(t, again.Resealed)
	_, err = resealCommand(t, "--unit-root", filepath.Join(options.UnitRoot, "missing"))
	require.Error(t, err)
}
