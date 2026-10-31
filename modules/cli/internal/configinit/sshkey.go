package configinit

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

// sshKeyMu 串行化 ensureSSHKey：页面会同时发出好几个请求，各自都可能发现密钥还不存在，
// 不加锁时后到的那个会在写入时撞上先到的，报“已存在”。
var sshKeyMu sync.Mutex

// ensureSSHKey 读取本机的 MooX 密钥，没有时生成一把（不加口令，只给 MooX 用）。
// 已有的文件不是可用的私钥（损坏或带口令）时，先改名备份再生成新的，不覆盖也不让用户手工处理。
func ensureSSHKey() (SSHKey, error) {
	sshKeyMu.Lock()
	defer sshKeyMu.Unlock()
	home, err := os.UserHomeDir()
	if err != nil {
		return SSHKey{}, fmt.Errorf("找不到用户主目录: %w", err)
	}
	dir := filepath.Join(home, ".ssh")
	path := filepath.Join(dir, sshKeyName)
	signer, err := readSSHKey(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		backup, err := backupSSHKey(path)
		if err != nil {
			return SSHKey{}, err
		}
		fmt.Fprintf(os.Stderr, "%s 不是可用的不带口令的 SSH 私钥，已备份为 %s，并生成新的 MooX 密钥\n", path, backup)
	default:
		return writePublicKey(path, signer)
	}
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
	raw := pem.EncodeToMemory(block)
	err = writeNew(path, raw)
	clear(raw)
	if err != nil {
		return SSHKey{}, fmt.Errorf("保存 MooX 密钥失败: %w", err)
	}
	signer, err = readSSHKey(path)
	if err != nil {
		return SSHKey{}, fmt.Errorf("读取刚生成的 MooX 密钥失败: %w", err)
	}
	return writePublicKey(path, signer)
}

// readSSHKey 读取不带口令的私钥；文件不存在时返回 os.ErrNotExist。
func readSSHKey(path string) (xssh.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	signer, err := xssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("%s 不是不带口令的 SSH 私钥: %w", path, err)
	}
	return signer, nil
}

// backupSSHKey 把不可用的旧密钥（及其公钥文件）改名为 <名字>.bak-<时间>，返回备份后的私钥路径。
func backupSSHKey(path string) (string, error) {
	backup := fmt.Sprintf("%s.bak-%s", path, time.Now().Format("20060102-150405"))
	if err := os.Rename(path, backup); err != nil {
		return "", fmt.Errorf("备份旧密钥 %s 失败: %w", path, err)
	}
	if err := os.Rename(path+".pub", backup+".pub"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("备份旧公钥 %s.pub 失败: %w", path, err)
	}
	return backup, nil
}

func writePublicKey(path string, signer xssh.Signer) (SSHKey, error) {
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

func (systemProber) Reach(ctx context.Context, target SSHTarget) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	addr := net.JoinHostPort(target.Address, strconv.Itoa(target.SSHPort))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "", fmt.Errorf("%s 拒绝了连接：IP 通，但这个端口上没有 SSH 服务，请确认 SSH 服务已启动、端口号填对了", addr)
	case err != nil:
		return "", fmt.Errorf("连不上 %s：请确认 IP 填对、机器已开机，并且防火墙或安全组放行了 SSH 端口 %d（局域网机器还要确认这台电脑能访问到它）", addr, target.SSHPort)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, _ := bufio.NewReader(io.LimitReader(conn, 255)).ReadString('\n')
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "SSH-") {
		return "", fmt.Errorf("%s 能连上，但对方没有按 SSH 协议回应：端口 %d 上运行的可能不是 SSH 服务", addr, target.SSHPort)
	}
	return line, nil
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
