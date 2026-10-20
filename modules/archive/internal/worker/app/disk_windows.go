//go:build windows

package app

import "golang.org/x/sys/windows"

// StatfsDiskUsage 用 GetDiskFreeSpaceEx 读取路径所在卷的总量与当前用户可用量。
func StatfsDiskUsage(path string) (total, free uint64, err error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var available, size, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(name, &available, &size, &totalFree); err != nil {
		return 0, 0, err
	}
	return size, available, nil
}
