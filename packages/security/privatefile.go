package security

import (
	"fmt"
	"os"
)

// CheckPrivateFile 检查保存凭据的私密文件：必须是普通文件、不是符号链接，并且只有当前用户能访问。
// “只有当前用户能访问”按平台判断：Unix 上权限必须是 0600；Windows 的文件模式表示不了，改查 ACL
// （只允许当前用户、SYSTEM 与 Administrators，不能含 Everyone、Users、Authenticated Users 等）。
func CheckPrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file, not a symlink", path)
	}
	return checkPrivateAccess(path, info)
}
