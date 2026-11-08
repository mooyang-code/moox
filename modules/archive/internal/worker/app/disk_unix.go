//go:build linux || darwin

package app

import "syscall"

// StatfsDiskUsage 用 statfs 读取路径所在文件系统的总量（Blocks）、已用（Blocks − Bfree）与可用（Bavail，不含 root 保留空间）。
func StatfsDiskUsage(path string) (total, used, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	block := uint64(st.Bsize)
	return uint64(st.Blocks) * block, (uint64(st.Blocks) - uint64(st.Bfree)) * block, uint64(st.Bavail) * block, nil
}
