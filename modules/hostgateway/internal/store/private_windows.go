//go:build windows

package store

import "os"

// The Windows deployment must apply owner-only ACLs. Go Mode bits cannot
// inspect those ACLs, and directory FlushFileBuffers is unsupported.
func privateFile(os.FileInfo) bool      { return true }
func privateDirectory(os.FileInfo) bool { return true }
func syncDirectory(*os.Root) error      { return nil }
