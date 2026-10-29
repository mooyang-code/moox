package configinit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	xssh "golang.org/x/crypto/ssh"
)

// MooX 专用的 SSH 密钥：本机 ~/.ssh/moox_ed25519。录入页用它给每台服务器开通免密登录，
// moox.toml 只记录私钥路径（控制台终端也用这把密钥），不保存 SSH 密码。
const (
	sshKeyName     = "moox_ed25519"
	sshKeyTOMLPath = "~/.ssh/" + sshKeyName
	sshKeyComment  = "moox"
)

// SSHKey 是本机的 MooX 密钥。
type SSHKey struct {
	Path      string `json:"path"`
	PublicKey string `json:"public_key"`
}

// LoginReport 是用 MooX 密钥登录一台服务器的结果：Ready 表示免密登录可用；
// HostKey 不为空表示第一次连接，需要用户确认主机指纹；两者都为空表示还没有安装公钥。
type LoginReport struct {
	Ready   bool   `json:"ready"`
	HostKey string `json:"host_key,omitempty"`
}

// ensureSSHKey 读取本机的 MooX 密钥，没有时生成一把（不加口令，只给 MooX 用）。
func ensureSSHKey() (SSHKey, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return SSHKey{}, fmt.Errorf("找不到用户主目录: %w", err)
	}
	dir := filepath.Join(home, ".ssh")
	path := filepath.Join(dir, sshKeyName)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return SSHKey{}, fmt.Errorf("创建 %s 失败: %w", dir, err)
		}
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return SSHKey{}, err
		}
		block, err := xssh.MarshalPrivateKey(private, sshKeyComment)
		if err != nil {
			return SSHKey{}, err
		}
		raw = pem.EncodeToMemory(block)
		if err := writeNew(path, raw); err != nil {
			return SSHKey{}, fmt.Errorf("保存 MooX 密钥失败: %w", err)
		}
	} else if err != nil {
		return SSHKey{}, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	signer, err := xssh.ParsePrivateKey(raw)
	clear(raw)
	if err != nil {
		return SSHKey{}, fmt.Errorf("%s 不是不带口令的 SSH 私钥，请把它改名后重新打开配置页", path)
	}
	public := strings.TrimSpace(string(xssh.MarshalAuthorizedKey(signer.PublicKey()))) + " " + sshKeyComment
	if err := os.WriteFile(path+".pub", []byte(public+"\n"), 0o644); err != nil {
		return SSHKey{}, fmt.Errorf("保存 MooX 公钥失败: %w", err)
	}
	return SSHKey{Path: path, PublicKey: public}, nil
}

// keyOnly 只用 MooX 密钥登录，不读 SSH agent 和其他默认密钥，确认的是这把密钥本身可用。
func keyOnly(key SSHKey) setupssh.Options {
	return setupssh.Options{Timeout: 15 * time.Second, IdentityFiles: []string{key.Path}, DisableAgent: true}
}

func sshTarget(target SSHTarget) setupssh.Target {
	return setupssh.Target{Name: "host", Address: target.Address, Port: target.SSHPort, Username: target.SSHUser}
}

func (systemProber) Login(ctx context.Context, target SSHTarget) (LoginReport, error) {
	key, err := ensureSSHKey()
	if err != nil {
		return LoginReport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := setupssh.Dial(ctx, sshTarget(target), "", keyOnly(key))
	switch {
	case errors.Is(err, setupssh.ErrHostKeyUnknown):
		_, fingerprint, _ := strings.Cut(err.Error(), ": ")
		return LoginReport{HostKey: strings.TrimSpace(fingerprint)}, nil
	case errors.Is(err, setupssh.ErrAuthFailed):
		return LoginReport{}, nil
	case err != nil:
		return LoginReport{}, unreachable(target)
	}
	client.Close()
	return LoginReport{Ready: true}, nil
}

func (systemProber) Trust(ctx context.Context, target SSHTarget, fingerprint string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := setupssh.TrustHost(ctx, sshTarget(target), strings.TrimSpace(fingerprint), setupssh.Options{Timeout: 15 * time.Second}); err != nil {
		return fmt.Errorf("没能记录 %s 的主机指纹：机器的指纹可能已经变化，请重新检测", target.Address)
	}
	return nil
}

// authorizeScript 把标准输入里的一行公钥追加到 authorized_keys（已有则不重复），并收紧权限。
const authorizeScript = `umask 077; read -r key || exit 3; mkdir -p "$HOME/.ssh" && touch "$HOME/.ssh/authorized_keys" &&
chmod 700 "$HOME/.ssh" && chmod 600 "$HOME/.ssh/authorized_keys" &&
{ grep -qxF "$key" "$HOME/.ssh/authorized_keys" || printf '%s\n' "$key" >> "$HOME/.ssh/authorized_keys"; }`

// Authorize 用一次性的 SSH 密码登录，把 MooX 公钥装到服务器上。密码只用这一次，不保存。
func (p systemProber) Authorize(ctx context.Context, target SSHTarget, password string) error {
	key, err := ensureSSHKey()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	client, err := setupssh.Dial(ctx, sshTarget(target), password, setupssh.Options{Timeout: 15 * time.Second, IdentityFiles: []string{}, DisableAgent: true})
	switch {
	case errors.Is(err, setupssh.ErrHostKeyUnknown):
		return fmt.Errorf("请先确认 %s 的主机指纹", target.Address)
	case errors.Is(err, setupssh.ErrAuthFailed):
		return fmt.Errorf("用密码登录 %s 失败，请检查用户名和密码（服务器需要允许密码登录）", target.Address)
	case err != nil:
		return unreachable(target)
	}
	defer client.Close()
	result, err := client.Run(ctx, []string{"sh", "-c", authorizeScript}, strings.NewReader(key.PublicKey+"\n"))
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("已登录 %s，但写入 ~/.ssh/authorized_keys 失败", target.Address)
	}
	return nil
}

func unreachable(target SSHTarget) error {
	return fmt.Errorf("连不上 %s:%s，请确认机器已开机、SSH 服务已打开、防火墙放行了 SSH 端口，并且这台电脑能访问它", target.Address, strconv.Itoa(target.SSHPort))
}

// copyIDCommand 是不想在页面里输密码时，用户自己在终端里执行的等价命令。
func copyIDCommand(key SSHKey, target SSHTarget) string {
	return fmt.Sprintf("ssh-copy-id -i %s.pub -p %d %s@%s", key.Path, target.SSHPort, target.SSHUser, target.Address)
}
