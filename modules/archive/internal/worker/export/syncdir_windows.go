//go:build windows

package export

// syncDir 在 Windows 上不可用（目录不能以可写方式打开并 FlushFileBuffers），改名本身由 NTFS 日志保证，直接跳过。
func syncDir(string) error { return nil }
