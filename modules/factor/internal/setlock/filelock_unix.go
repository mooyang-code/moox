//go:build !windows

package setlock

import "golang.org/x/sys/unix"

// fileLock 是一个跨进程的排他文件锁（flock）。
type fileLock struct{ fd int }

func openLockFile(path string) (*fileLock, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	return &fileLock{fd: fd}, nil
}

// tryLock 非阻塞加锁；被其他进程持有时返回 false。
func (f *fileLock) tryLock() (bool, error) {
	err := unix.Flock(f.fd, unix.LOCK_EX|unix.LOCK_NB)
	switch err {
	case nil:
		return true, nil
	case unix.EWOULDBLOCK, unix.EINTR:
		return false, nil
	default:
		return false, err
	}
}

func (f *fileLock) unlock() { _ = unix.Flock(f.fd, unix.LOCK_UN) }

func (f *fileLock) close() error { return unix.Close(f.fd) }
