//go:build !windows

package doctor

import "syscall"

// pidAlive 用 0 号信号探测进程是否存在。
func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }
