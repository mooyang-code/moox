//go:build !darwin && !linux

package device

import "errors"

func FreeDiskBytes(string) (uint64, error) {
	return 0, errors.New("free disk reporting is unsupported on this platform")
}
