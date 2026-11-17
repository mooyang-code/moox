//go:build !unix

package hostpolicy

import "os"

// checkPrivate 在非 Unix 平台不检查文件模式（策略缓存只在 Linux 服务器上使用）。
func checkPrivate(os.FileInfo) error { return nil }
