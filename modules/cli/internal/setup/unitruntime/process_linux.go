//go:build linux

package unitruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func ownerMatches(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

func readProcess(pid int) (processIdentity, bool, error) {
	if pid <= 1 {
		return processIdentity{}, false, errIdentity
	}
	base := fmt.Sprintf("/proc/%d", pid)
	processInfo, err := os.Stat(base)
	if os.IsNotExist(err) {
		return processIdentity{}, false, nil
	}
	if err != nil || !ownerMatches(processInfo) {
		return processIdentity{}, false, errIdentity
	}
	raw, err := os.ReadFile(base + "/stat")
	if os.IsNotExist(err) {
		return processIdentity{}, false, nil
	}
	if err != nil {
		return processIdentity{}, false, err
	}
	closing := strings.LastIndexByte(string(raw), ')')
	if closing < 0 {
		return processIdentity{}, false, errIdentity
	}
	fields := strings.Fields(string(raw[closing+1:]))
	if len(fields) < 20 {
		return processIdentity{}, false, errIdentity
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return processIdentity{}, false, nil
	}
	if ticks, err := strconv.ParseUint(fields[19], 10, 64); err != nil || ticks == 0 {
		return processIdentity{}, false, errIdentity
	}
	executable, err := os.Readlink(base + "/exe")
	if os.IsNotExist(err) {
		return processIdentity{}, false, nil
	}
	if err != nil {
		return processIdentity{}, false, err
	}
	info, err := os.Stat(base + "/exe")
	if err != nil {
		if os.IsNotExist(err) {
			return processIdentity{}, false, nil
		}
		return processIdentity{}, false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !ownerMatches(info) {
		return processIdentity{}, false, errIdentity
	}
	return processIdentity{PID: pid, StartTicks: fields[19], Executable: strings.TrimSuffix(executable, " (deleted)"), Device: uint64(stat.Dev), Inode: stat.Ino, UID: stat.Uid}, true, nil
}

// Open the pidfd before checking /proc. Signals then remain bound to that
// specific process even if it exits and its numeric PID is subsequently reused.
func openProcess(expected processIdentity, alternatives ...processIdentity) (*os.File, bool, error) {
	fd, err := unix.PidfdOpen(expected.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open Linux pidfd: %w", err)
	}
	unix.CloseOnExec(fd)
	file := os.NewFile(uintptr(fd), "moox-process")
	actual, alive, err := readProcess(expected.PID)
	match := actual == expected
	for _, alternative := range alternatives {
		match = match || actual == alternative
	}
	if err != nil || !alive || !match {
		file.Close()
		if err != nil || !alive {
			return nil, alive, err
		}
		return nil, false, errIdentity
	}
	return file, true, nil
}

func releaseIdentity(launch processIdentity, binary string) (processIdentity, error) {
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 || !ownerMatches(info) {
		return processIdentity{}, errIdentity
	}
	stat := info.Sys().(*syscall.Stat_t)
	launch.Executable, launch.Device, launch.Inode = binary, uint64(stat.Dev), stat.Ino
	return launch, nil
}

func execComponent(binary string, args, environment []string) error {
	return unix.Exec(binary, append([]string{binary}, args...), environment)
}

func signalProcess(file *os.File, force bool) error {
	signal := unix.SIGTERM
	if force {
		signal = unix.SIGKILL
	}
	err := unix.PidfdSendSignal(int(file.Fd()), signal, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func processExited(file *os.File) (bool, error) {
	fds := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}
	_, err := unix.Poll(fds, 0)
	if errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return fds[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0, err
}

func detachCommand(command *exec.Cmd) { command.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func acquireLock(ctx context.Context, root *os.Root, held bool, inheritedFD int, nonblocking bool) (*os.File, bool, error) {
	name := "maintenance.lock"
	if info, err := root.Lstat(name); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownerMatches(info)) {
		return nil, false, errors.New("maintenance lock must be an owned regular 0600 file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, false, err
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if held {
		if inheritedFD < 3 {
			file.Close()
			return nil, false, errors.New("maintenance-lock-held requires an inherited lock descriptor")
		}
		fd, err := unix.Dup(inheritedFD)
		if err != nil {
			file.Close()
			return nil, false, errors.New("cannot duplicate inherited maintenance lock")
		}
		unix.CloseOnExec(fd)
		inherited := os.NewFile(uintptr(fd), "moox-inherited-maintenance")
		want, statErr := file.Stat()
		actual, actualErr := inherited.Stat()
		file.Close()
		if statErr != nil || actualErr != nil || !os.SameFile(want, actual) {
			inherited.Close()
			return nil, false, errors.New("inherited descriptor does not name this maintenance lock")
		}
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			inherited.Close()
			return nil, false, errors.New("inherited maintenance descriptor is not exclusively held")
		}
		return inherited, false, nil
	}
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return file, false, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			file.Close()
			return nil, false, err
		}
		if nonblocking {
			file.Close()
			return nil, true, nil
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, false, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
