// Package tlstrust 判断控制台入口的证书模式，并管理操作员本机对内部 CA 的信任。
package tlstrust

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type TLSMode string

const (
	TLSModePublic   TLSMode = "public"
	TLSModeInternal TLSMode = "internal"
)

func resolveTLSMode(mode TLSMode, publicHost string) TLSMode {
	if mode == TLSModePublic || mode == TLSModeInternal {
		return mode
	}
	host := strings.TrimSpace(publicHost)
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return TLSModeInternal
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return TLSModeInternal
	}
	return TLSModePublic
}

func UsesPublicTLSMode(mode TLSMode, publicHost string) bool {
	return resolveTLSMode(mode, publicHost) == TLSModePublic
}

// RequiresLocalCATrust reports whether browsers need the MooX Caddy root CA
// installed in the operator's trust store for the selected endpoint.
func RequiresLocalCATrust(mode TLSMode, publicHost string) bool {
	return resolveTLSMode(mode, publicHost) == TLSModeInternal
}

type FileCAStore struct{}

func CAPath(publicHost string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r) {
			return r
		}
		return '_'
	}, publicHost)
	return filepath.Join(home, ".moox", "certs", "moox-caddy-root-"+safe+".crt")
}

func (FileCAStore) Save(publicHost string, raw []byte) error {
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return fmt.Errorf("invalid ca")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !certificate.IsCA {
		return fmt.Errorf("invalid ca")
	}
	path := CAPath(publicHost)
	if path == "" {
		return fmt.Errorf("invalid ca path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".next"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// Fingerprint 返回 PEM 证书文件的 SHA-256 指纹（冒号分隔的大写十六进制）；文件不存在或不是单张证书时返回空串。
func Fingerprint(caPath string) string {
	raw, err := os.ReadFile(caPath)
	if err != nil {
		return ""
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// ErrBrowserCATrust 标记操作员本机信任控制台 CA 失败。
var ErrBrowserCATrust = errors.New("browser_ca_trust_failed")

// EnsureLocalCATrust checks and, when needed, installs the public Caddy root
// certificate into the operator machine's trust store. The repository script
// owns the platform-specific trust-store details and prompts for elevation
// only when the current user cannot perform the operation directly.
func EnsureLocalCATrust(ctx context.Context, repositoryRoot, caPath string) error {
	repositoryRoot = strings.TrimSpace(repositoryRoot)
	caPath = strings.TrimSpace(caPath)
	if repositoryRoot == "" || caPath == "" {
		return fmt.Errorf("%w: installer or CA path is empty", ErrBrowserCATrust)
	}
	script := filepath.Join(repositoryRoot, "scripts", "deploy", "install-caddy-ca.sh")
	info, err := os.Stat(script)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: installer not found at %s", ErrBrowserCATrust, script)
	}
	if err := runLocalCACommand(ctx, script, caPath, true); err == nil {
		return nil
	}
	if err := runLocalCACommand(ctx, script, caPath, false); err != nil {
		return fmt.Errorf("%w: install %s --ca-file %s: %w", ErrBrowserCATrust, script, caPath, err)
	}
	if err := runLocalCACommand(ctx, script, caPath, true); err != nil {
		return fmt.Errorf("%w: trust store rejected %s after installation: %w", ErrBrowserCATrust, caPath, err)
	}
	return nil
}

// EnsureLocalCATrustForHost is the endpoint-aware form used by setup flows.
// Public ACME certificates are already trusted by normal browsers and must
// not cause any local trust-store mutation.
func EnsureLocalCATrustForHost(ctx context.Context, repositoryRoot, publicHost string, mode TLSMode) error {
	if !RequiresLocalCATrust(mode, publicHost) {
		return nil
	}
	return EnsureLocalCATrust(ctx, repositoryRoot, CAPath(publicHost))
}

func runLocalCACommand(ctx context.Context, script, caPath string, checkOnly bool) error {
	args := []string{"--ca-file", caPath}
	if checkOnly {
		args = append(args, "--check")
	}
	command := exec.CommandContext(ctx, script, args...)
	// Keep the command attached to the invoking terminal so sudo/security can
	// explain what is happening and request the user's administrator approval.
	command.Stdin = os.Stdin
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	return command.Run()
}
