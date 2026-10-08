//go:build windows

package config

import "os"

// Windows has no portable O_NOFOLLOW equivalent in the standard library.
// ACLs should be used by the deployment environment for secret protection.
func openSecretFile(path string) (*os.File, error) {
	return os.Open(path)
}
