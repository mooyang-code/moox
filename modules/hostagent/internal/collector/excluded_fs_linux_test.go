//go:build linux

package collector

import "testing"

// 内核伪文件系统不是业务磁盘：efivarfs 几乎总是 90% 以上，不能让它触发“磁盘空间使用率”告警。
func TestExcludedFSSkipsKernelPseudoFilesystems(t *testing.T) {
	for _, name := range []string{"efivarfs", "bpf", "rpc_pipefs", "nsfs", "tmpfs", "overlay", "fuse.sshfs", "nfs4"} {
		if !excludedFS(name) {
			t.Errorf("%s 应被排除", name)
		}
	}
	for _, name := range []string{"ext4", "xfs", "btrfs", "vfat"} {
		if excludedFS(name) {
			t.Errorf("%s 不应被排除", name)
		}
	}
}
