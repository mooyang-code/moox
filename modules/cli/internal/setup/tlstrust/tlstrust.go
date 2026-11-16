// Package tlstrust 管理操作员本机对 MooX CA（管理台 HTTPS 证书的签发者）的信任。
package tlstrust

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type FileCAStore struct{}

// CAPath 是本机缓存的 MooX CA 根证书：~/.moox/certs/moox-ca-{控制机地址}.crt。
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
	return filepath.Join(home, ".moox", "certs", "moox-ca-"+safe+".crt")
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

// EnsureLocalCATrust 检查本机是否信任 MooX CA 根证书，没信任时装进系统信任库。各平台信任库的细节由仓库里的
// scripts/deploy/install-moox-ca.sh 负责，只有当前用户权限不够时它才请求管理员授权。
func EnsureLocalCATrust(ctx context.Context, repositoryRoot, caPath string) error {
	repositoryRoot = strings.TrimSpace(repositoryRoot)
	caPath = strings.TrimSpace(caPath)
	if repositoryRoot == "" || caPath == "" {
		return fmt.Errorf("%w: installer or CA path is empty", ErrBrowserCATrust)
	}
	script := filepath.Join(repositoryRoot, "scripts", "deploy", "install-moox-ca.sh")
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
