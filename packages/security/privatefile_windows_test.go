//go:build windows

package security

import (
	"os/exec"
	"os/user"
	"testing"
)

// restrictToCurrentUser 去掉继承的权限，只给当前用户完全控制（与安装脚本的做法一致）。
func restrictToCurrentUser(t *testing.T, path string) {
	t.Helper()
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("icacls", path, "/inheritance:r", "/grant:r", current.Username+":F").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v: %s", err, out)
	}
	// /grant:r 只替换该用户自己的条目；测试里显式授予 Everyone 的读权限是单独的条目，必须另外移除，
	// 否则收紧之后文件仍然对所有人可读（GitHub 的 Windows 运行器上就是这样失败的）。
	if out, err := exec.Command("icacls", path, "/remove", "*S-1-1-0").CombinedOutput(); err != nil {
		t.Fatalf("icacls remove Everyone: %v: %s", err, out)
	}
}

// 默认继承的 ACL 通常包含 Users / Authenticated Users，必须被拒绝；收紧后通过。
func TestCheckPrivateFileRejectsInheritedGroupAccess(t *testing.T) {
	path := t.TempDir() + `\shared`
	if out, err := exec.Command("cmd", "/c", "echo x>"+path).CombinedOutput(); err != nil {
		t.Fatalf("create: %v: %s", err, out)
	}
	if out, err := exec.Command("icacls", path, "/grant", "*S-1-1-0:R").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v: %s", err, out)
	}
	if err := CheckPrivateFile(path); err == nil {
		t.Fatal("file readable by Everyone accepted")
	}
	restrictToCurrentUser(t, path)
	if err := CheckPrivateFile(path); err != nil {
		t.Fatalf("restricted file rejected: %v", err)
	}
}
