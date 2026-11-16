//go:build !windows

package topology

import (
	"os"
	"syscall"
)

// permissionsChecked 表示这个系统用 Unix 权限位表示“只有本人可读”。
const permissionsChecked = true

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(stat.Uid) == os.Geteuid()
}
