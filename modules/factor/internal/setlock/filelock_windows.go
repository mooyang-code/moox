//go:build windows

package setlock

import (
	"errors"

	"golang.org/x/sys/windows"
)

// fileLock 是一个跨进程的排他文件锁（LockFileEx，锁住文件的第一个字节）。
type fileLock struct{ handle windows.Handle }

func openLockFile(path string) (*fileLock, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return &fileLock{handle: handle}, nil
}

// tryLock 非阻塞加锁；被其他进程持有时返回 false。
func (f *fileLock) tryLock() (bool, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(f.handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return false, err
}

func (f *fileLock) unlock() {
	var overlapped windows.Overlapped
	_ = windows.UnlockFileEx(f.handle, 0, 1, 0, &overlapped)
}

func (f *fileLock) close() error { return windows.CloseHandle(f.handle) }
