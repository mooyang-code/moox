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
	"strings"
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

func TestOfflineAdministratorUsesStdinAndRedactsPrivateChildFailures(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	binary := filepath.Join(root, "admin-cli")
	user := AdminUser{Username: "admin", Password: "synthetic-private-password"}
	// The child rejects a missing inherited lock, password arguments or extra
	// environment entries. Only stdin contains the supplied password.
	script := "#!/bin/sh\nset -eu\n[ -e /proc/self/fd/3 ]\n[ \"$#\" -eq 7 ]\n[ \"$1 $2 $3 $4 $5 $6 $7\" = 'user ensure --db-path closed.db --username admin --password-stdin' ]\nIFS= read -r password\n[ \"$password\" = 'synthetic-private-password' ]\n[ \"$(env | wc -l)\" -le 3 ]\nprintf '%s\\n' '{\"status\":\"ok\",\"command\":\"user.ensure\",\"action\":\"created\",\"username\":\"admin\"}'\n"
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o700))
	withLock := func(run func(unitruntime.Options) error) error {
		return unitruntime.WithMaintenance(t.Context(), root, "control", unitruntime.Options{}, func(guard *unitruntime.Maintenance) error {
			return guard.UseLock(t.Context(), run)
		})
	}
	require.NoError(t, withLock(func(lock unitruntime.Options) error {
		return offlineAdminUser(t.Context(), binary, "closed.db", filepath.Join(root, "master.key"), user, lock)
	}))
	for _, failure := range []string{"printf '%s' '" + user.Password + "'; exit 1", "printf '%s' '" + user.Password + "'"} {
		require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\n"+failure+"\n"), 0o700))
		err := withLock(func(lock unitruntime.Options) error {
			return offlineAdminUser(t.Context(), binary, "closed.db", filepath.Join(root, "master.key"), user, lock)
		})
		require.Error(t, err)
		require.False(t, strings.Contains(err.Error(), user.Password))
	}
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
