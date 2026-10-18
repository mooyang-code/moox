package security

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 私密文件：0600（Windows 上为只允许当前用户的 ACL）通过；组或其他人可读、符号链接、目录、不存在都拒绝。
func TestCheckPrivateFile(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		restrictToCurrentUser(t, secret)
	}
	if err := CheckPrivateFile(secret); err != nil {
		t.Fatalf("private file rejected: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(secret, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := CheckPrivateFile(secret); err == nil {
			t.Fatal("group-readable file accepted")
		}
		link := filepath.Join(dir, "link")
		if err := os.Symlink(secret, link); err != nil {
			t.Fatal(err)
		}
		if err := CheckPrivateFile(link); err == nil {
			t.Fatal("symlink accepted")
		}
	}
	if err := CheckPrivateFile(dir); err == nil {
		t.Fatal("directory accepted")
	}
	if err := CheckPrivateFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing file accepted")
	}
}
