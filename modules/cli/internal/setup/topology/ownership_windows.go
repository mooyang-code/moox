//go:build windows

package topology

import "os"

// Windows 不用 Unix 权限位表示私有文件，NTFS 的访问控制由使用者自己负责。
const permissionsChecked = false

func ownedByCurrentUser(os.FileInfo) bool {
	return true
}
