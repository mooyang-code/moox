package gatewayauth

import (
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"unicode"
)

// ReadSigningSecret loads a bounded private signing key without following symlinks.
func ReadSigningSecret(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("signing key must be a regular file, not a symlink")
	}
	file, err := openSecretFile(path)
	if err != nil {
		return "", errors.New("cannot open signing key file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return "", errors.New("signing key changed while opening")
	}
	if runtime.GOOS != "windows" && opened.Mode().Perm() != 0o600 {
		return "", errors.New("signing key permissions must be 0600")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 {
		return "", errors.New("signing key unreadable or exceeds size limit")
	}
	defer clear(raw)
	key := strings.TrimSpace(string(raw))
	if len(key) < 32 || strings.ContainsFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", errors.New("signing key must be a single value of at least 32 bytes")
	}
	return key, nil
}
