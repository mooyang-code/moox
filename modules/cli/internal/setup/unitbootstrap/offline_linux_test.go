//go:build linux

package unitbootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	if root := os.Getenv("MOOX_TEST_OFFLINE_PARENT"); root != "" {
		err := unitruntime.WithMaintenance(context.Background(), root, "control", unitruntime.Options{}, func(guard *unitruntime.Maintenance) error {
			return guard.UseLock(context.Background(), func(lock unitruntime.Options) error {
				command := exec.Command("/bin/sleep", "60")
				closeLock, err := offlineProcess(command, lock)
				if err != nil {
					return err
				}
				defer closeLock()
				runtime.LockOSThread()
				defer runtime.UnlockOSThread()
				if err := command.Start(); err != nil {
					return err
				}
				raw, err := json.Marshal(command.Process.Pid)
				if err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(root, "child.json"), raw, 0o600); err != nil {
					return err
				}
				return command.Wait()
			})
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "offline parent fixture failed")
			os.Exit(31)
		}
		return
	}
	os.Exit(m.Run())
}

func TestBootstrapLinuxOfflineChildHoldsLockAndDiesWithParent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	self, err := os.Executable()
	require.NoError(t, err)
	parent := exec.CommandContext(t.Context(), self)
	parent.Env = append(os.Environ(), "MOOX_TEST_OFFLINE_PARENT="+root)
	require.NoError(t, parent.Start())
	defer parent.Process.Kill()
	var child int
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(filepath.Join(root, "child.json"))
		return err == nil && json.Unmarshal(raw, &child) == nil && child > 1
	}, 5*time.Second, 5*time.Millisecond)
	defer syscall.Kill(child, syscall.SIGKILL)
	lock, err := os.OpenFile(filepath.Join(root, "run/maintenance.lock"), os.O_RDWR, 0)
	require.NoError(t, err)
	defer lock.Close()
	require.Error(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	fdinfo, err := os.ReadFile(fmt.Sprintf("/proc/%d/fdinfo/3", child))
	require.NoError(t, err)
	require.Contains(t, string(fdinfo), "FLOCK")
	require.Contains(t, string(fdinfo), "WRITE")
	require.NoError(t, parent.Process.Signal(syscall.SIGKILL))
	require.Error(t, parent.Wait())
	require.Eventually(t, func() bool { return syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil }, 5*time.Second, 5*time.Millisecond, "child must close the inherited writer lock after parent death")
	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_UN))
	// An inherited lock without Pdeathsig would keep the 60-second sleeper alive
	// and prevent the preceding lock acquisition for the whole fixture lifetime.
}
