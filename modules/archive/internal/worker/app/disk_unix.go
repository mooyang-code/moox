package app

import "syscall"

// StatfsDiskUsage 用 statfs 读取路径所在文件系统的总量与可用量。
func StatfsDiskUsage(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	block := uint64(st.Bsize)
	return uint64(st.Blocks) * block, uint64(st.Bavail) * block, nil
}
