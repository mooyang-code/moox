//go:build !windows

package security

import "testing"

func restrictToCurrentUser(*testing.T, string) {}
