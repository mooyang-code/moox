package deploy

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/release"
)

// ErrBrowserCATrust 表示没能在操作员机器上信任控制台代理的内置 CA。
var ErrBrowserCATrust = errors.New("browser_ca_trust_failed")

// CAPath 是操作员机器上保存控制台代理内置 CA 根证书的位置。
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

// RequiresLocalCATrust 判断控制台代理是否使用内置 CA，需要操作员机器信任它的根证书。
func RequiresLocalCATrust(host setupconfig.Host) bool {
	return release.ResolveTLSMode(host.TLSMode, host.Address) == release.TLSModeInternal
}

// SaveCA 校验并保存控制台代理的内置 CA 根证书。
func SaveCA(publicHost string, raw []byte) error {
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return fmt.Errorf("控制台代理的根证书无效")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !certificate.IsCA {
		return fmt.Errorf("控制台代理的根证书无效")
	}
	path := CAPath(publicHost)
	if path == "" {
		return fmt.Errorf("找不到保存根证书的目录")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".next"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// EnsureLocalCATrust 检查操作员机器是否信任控制台代理的根证书，不信任时调用 scripts/deploy/install-caddy-ca.sh 安装
// （需要时会请求管理员授权）。控制台代理用公网证书时什么也不做。
func EnsureLocalCATrust(ctx context.Context, repositoryRoot string, host setupconfig.Host) error {
	if !RequiresLocalCATrust(host) {
		return nil
	}
	caPath := CAPath(host.Address)
	script := filepath.Join(repositoryRoot, "scripts", "deploy", "install-caddy-ca.sh")
	if info, err := os.Stat(script); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: 找不到 %s", ErrBrowserCATrust, script)
	}
	if _, err := os.Stat(caPath); err != nil {
		return fmt.Errorf("%w: 本机还没有控制台代理的根证书 %s，请先部署 control", ErrBrowserCATrust, caPath)
	}
	if runCACommand(ctx, script, caPath, true) == nil {
		return nil
	}
	if err := runCACommand(ctx, script, caPath, false); err != nil {
		return fmt.Errorf("%w: 安装 %s: %w", ErrBrowserCATrust, caPath, err)
	}
	if err := runCACommand(ctx, script, caPath, true); err != nil {
		return fmt.Errorf("%w: 安装后系统仍不信任 %s: %w", ErrBrowserCATrust, caPath, err)
	}
	return nil
}

func runCACommand(ctx context.Context, script, caPath string, checkOnly bool) error {
	args := []string{"--ca-file", caPath}
	if checkOnly {
		args = append(args, "--check")
	}
	command := exec.CommandContext(ctx, script, args...)
	// 保持连接到终端，sudo 或 security 可以向用户说明并请求授权。
	command.Stdin = os.Stdin
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	return command.Run()
}
