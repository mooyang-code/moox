//go:build !unix

package app

import "errors"

// StatfsDiskUsage 在不支持 statfs 的平台上不可用。
func StatfsDiskUsage(string) (uint64, uint64, error) {
	return 0, 0, errors.New("disk usage is not supported on this platform")
}
