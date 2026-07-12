package security

import (
	"errors"
	"os"
	"strings"
)

var ErrMissingEncryptionKey = errors.New("admin encryption key is not configured")

// GetEncryptionKey returns the admin key used for stored secrets.
func GetEncryptionKey() (string, error) {
	if key := strings.TrimSpace(os.Getenv("MOOX_ADMIN_ENCRYPTION_KEY")); key != "" {
		return key, nil
	}
	return "", ErrMissingEncryptionKey
}
