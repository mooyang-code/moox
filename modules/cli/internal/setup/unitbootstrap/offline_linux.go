//go:build linux

package unitbootstrap

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
)

func offlineProcess(command *exec.Cmd, lock unitruntime.Options) (func(), error) {
	if !lock.MaintenanceLockHeld || lock.MaintenanceLockFD < 3 {
		return nil, errors.New("offline operation requires the coordinator's inherited maintenance lock")
	}
	fd, err := syscall.Dup(lock.MaintenanceLockFD)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "bootstrap-maintenance")
	command.ExtraFiles = []*os.File{file}
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return func() { file.Close() }, nil
}
