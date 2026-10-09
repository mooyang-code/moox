// Package config loads the shared deployment/runtime contract and private
// identity. There is no fallback to legacy HTTP or static verifier tables.
package config

import (
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"unicode"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
)

type Config = hostgatewayconfig.Config

func Load(path string) (Config, error) { return hostgatewayconfig.Load(path) }

func LoadIdentity(cfg Config) (*tlsconfig.Material, gatewayauth.Credentials, error) {
	if err := cfg.Validate(); err != nil {
		return nil, gatewayauth.Credentials{}, err
	}
	material, err := tlsconfig.Load(cfg.Host.ID, cfg.TLS)
	if err != nil {
		return nil, gatewayauth.Credentials{}, err
	}
	secret, err := ReadSecret(cfg.Control.KeyFile)
	if err != nil {
		return nil, gatewayauth.Credentials{}, err
	}
	return material, gatewayauth.Credentials{Caller: cfg.Control.Caller, KeyID: cfg.Control.KeyID, Secret: secret}, nil
}

func ReadSecret(path string) (string, error) {
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
