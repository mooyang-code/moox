//go:build windows

package config

import "os"

// Windows does not expose a portable Unix uid through os.FileInfo. NTFS ACL
// checks remain the responsibility of the operator and deployment environment.
func ownedByCurrentUser(os.FileInfo) bool {
	return true
}
