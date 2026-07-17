//go:build windows

package broker

import "os"

func ownedByCurrentUser(os.FileInfo) bool {
	return true
}
