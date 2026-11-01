//go:build linux

package unitruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	// Pin one exec image before reading either its path or inode. Reading
	// /proc/PID/exe twice can combine the launcher path with the target inode
	// when exec happens between Readlink and Stat, falsely rejecting startup.
	image, err := os.Open(base + "/exe")
	if os.IsNotExist(err) {
		return processIdentity{}, false, nil
	}
	if err != nil {
		return processIdentity{}, false, err
	}
	defer image.Close()
	executable, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", image.Fd()))
	if err != nil {
		return processIdentity{}, false, err
	}
	info, err := image.Stat()
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
	// This is the dedicated launcher process, immediately before exec. Service
	// databases and caches must remain private even when the SSH user's umask
	// permits group writes; never change the coordinator's process-wide umask.
	unix.Umask(0o077)
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

// readKernelBootID 返回内核为本次开机生成的随机 ID，每次开机都不同。
func readKernelBootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	if len(id) != 36 {
		return "", errors.New("unexpected kernel boot id")
	}
	return id, nil
}

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
		// LOCK_EX would also acquire an unlocked descriptor or upgrade a
		// shared one. Verify the existing kernel lock before reusing it.
		if !exclusiveFlock(fd) {
			inherited.Close()
			return nil, false, errors.New("inherited maintenance descriptor is not exclusively held")
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

func exclusiveFlock(fd int) bool {
	file, err := os.Open("/proc/self/fdinfo/" + strconv.Itoa(fd))
	if err != nil {
		return false
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 16<<10+1))
	if err != nil || len(raw) > 16<<10 {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[0] == "lock:" && fields[2] == "FLOCK" && fields[3] == "ADVISORY" && fields[4] == "WRITE" {
			return true
		}
	}
	return false
}

// listeningSockets 读出正在监听这些端口的 socket，返回 inode 到端口的映射。
func listeningSockets(ports []int) (map[string]int, error) {
	wanted := map[int]bool{}
	for _, port := range ports {
		wanted[port] = true
	}
	sockets := map[string]int{}
	for _, name := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for i, row := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(row)
			if i == 0 || len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			_, hexPort, found := strings.Cut(fields[1], ":")
			if !found {
				continue
			}
			port, err := strconv.ParseInt(hexPort, 16, 32)
			if err == nil && wanted[int(port)] {
				sockets[fields[9]] = int(port)
			}
		}
	}
	return sockets, nil
}

// portHolders 列出占着这些端口的进程，一个端口只报一次。看不到属主的 socket 也要报：
// 那是别的用户的程序，同样不能启动。
func portHolders(ports []int) ([]portHolder, error) {
	sockets, err := listeningSockets(ports)
	if err != nil || len(sockets) == 0 {
		return nil, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	holders := []portHolder{}
	seen := map[int]bool{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		descriptors, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if err != nil {
			continue
		}
		for _, descriptor := range descriptors {
			target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, descriptor.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			port, found := sockets[inode]
			if !found || seen[port] {
				continue
			}
			identity, alive, err := readProcess(pid)
			if err != nil || !alive {
				continue
			}
			seen[port] = true
			holders = append(holders, portHolder{Port: port, PID: pid, Executable: identity.Executable, identity: identity})
		}
	}
	for _, port := range sockets {
		if !seen[port] {
			seen[port] = true
			holders = append(holders, portHolder{Port: port})
		}
	}
	return holders, nil
}

// stopLeftover 停掉一个没有记录的残留进程：先 SIGTERM，超过时限再 SIGKILL。pidfd 绑定进程身份，PID 被复用也不会误伤。
func stopLeftover(ctx context.Context, holder portHolder, budget time.Duration) error {
	file, alive, err := openProcess(holder.identity)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	defer file.Close()
	if err := signalProcess(file, false); err != nil {
		return err
	}
	exited, err := waitProcess(ctx, file, time.Now().Add(budget))
	if err != nil || exited {
		return err
	}
	if err := signalProcess(file, true); err != nil {
		return err
	}
	if exited, err = waitProcess(ctx, file, time.Now().Add(10*time.Second)); err != nil {
		return err
	}
	if !exited {
		return errors.New("forced exit of the leftover process was not observed")
	}
	return nil
}
