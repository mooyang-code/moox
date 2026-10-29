//go:build windows

package doctor

import "os"

// pidAlive 在 Windows 上以能否打开进程句柄判断进程是否存在。
func pidAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = process.Release()
	return true
}
