package ssh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/pkg/sftp"
	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

var (
	ErrHostKeyUnknown      = errors.New("host_key_unknown")
	ErrHostKeyChanged      = errors.New("host_key_changed")
	ErrFingerprintMismatch = errors.New("host_key_fingerprint_mismatch")
	ErrAuthFailed          = errors.New("ssh_auth_failed")
	ErrUnreachable         = errors.New("ssh_unreachable")
)

type Target struct {
	Name     string
	Address  string
	Port     int
	Username string
}

// HostTarget 返回 moox.toml 中一台主机的 SSH 目标。
func HostTarget(host setupconfig.Host) Target {
	return Target{Name: host.ID, Address: host.Address, Port: host.SSH.Port, Username: host.SSH.Username}
}

// DialHost 用 moox.toml 中的 SSH 口令连接一台主机。
func DialHost(ctx context.Context, host setupconfig.Host, opts Options) (Client, error) {
	return Dial(ctx, HostTarget(host), host.SSH.Password, opts)
}

func (t Target) DialAddress() string {
	return net.JoinHostPort(t.Address, strconv.Itoa(t.Port))
}

type Options struct {
	KnownHostsPath string
	Timeout        time.Duration
}

type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type Client interface {
	Check(ctx context.Context) error
	ForwardLocal(ctx context.Context, remote string) (net.Listener, error)
	Upload(ctx context.Context, src io.Reader, size int64, dst string, mode fs.FileMode) error
	Download(ctx context.Context, src string, dst io.Writer) (int64, error)
	Run(ctx context.Context, argv []string, stdin io.Reader) (Result, error)
	Close() error
}

type transport struct {
	client *xssh.Client
	mu     sync.Mutex
	closed bool
}

func Dial(ctx context.Context, target Target, password string, opts Options) (Client, error) {
	if err := validateTarget(target); err != nil {
		return nil, err
	}
	knownHostsPath, err := secureKnownHostsPath(opts.KnownHostsPath, false)
	if err != nil {
		return nil, err
	}
	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("host_key_store_invalid")
	}
	wrappedCallback := func(hostname string, remote net.Addr, key xssh.PublicKey) error {
		if err := callback(hostname, remote, key); err != nil {
			// known_hosts 里已有这台主机的另一把密钥：主机被重装，或者连到了冒充者。和"第一次连接"分开报告。
			var keyErr *knownhosts.KeyError
			if errors.As(err, &keyErr) && len(keyErr.Want) > 0 {
				return fmt.Errorf("%w: %s", ErrHostKeyChanged, xssh.FingerprintSHA256(key))
			}
			return fmt.Errorf("%w: %s", ErrHostKeyUnknown, xssh.FingerprintSHA256(key))
		}
		return nil
	}
	config := &xssh.ClientConfig{
		User:            target.Username,
		Auth:            []xssh.AuthMethod{xssh.Password(password)},
		HostKeyCallback: wrappedCallback,
		Timeout:         timeout(opts),
	}
	connection, err := dialContext(ctx, target.DialAddress(), config, timeout(opts))
	if err != nil {
		switch {
		case errors.Is(err, ErrHostKeyUnknown), errors.Is(err, ErrHostKeyChanged):
			return nil, err
		case isAuthenticationError(err):
			return nil, ErrAuthFailed
		default:
			return nil, ErrUnreachable
		}
	}
	return &transport{client: connection}, nil
}

var errHostKeyCaptured = errors.New("host key captured")

func TrustHost(ctx context.Context, target Target, expectedFingerprint string, opts Options) error {
	if err := validateTarget(target); err != nil {
		return err
	}
	knownHostsPath, err := secureKnownHostsPath(opts.KnownHostsPath, true)
	if err != nil {
		return err
	}
	var captured xssh.PublicKey
	config := &xssh.ClientConfig{
		User: target.Username,
		HostKeyCallback: func(_ string, _ net.Addr, key xssh.PublicKey) error {
			captured = key
			if xssh.FingerprintSHA256(key) != strings.TrimSpace(expectedFingerprint) {
				return ErrFingerprintMismatch
			}
			return errHostKeyCaptured
		},
		Timeout: timeout(opts),
	}
	_, err = dialContext(ctx, target.DialAddress(), config, timeout(opts))
	if errors.Is(err, ErrFingerprintMismatch) {
		return ErrFingerprintMismatch
	}
	if !errors.Is(err, errHostKeyCaptured) || captured == nil {
		return ErrUnreachable
	}
	// known_hosts 里已有这台主机的另一把密钥时拒绝追加：追加会让两把密钥都被接受，等于绕过了密钥变更告警。
	// 主机确实重装过，由操作员核对后手工删除旧条目再信任。
	if existing, err := knownhosts.New(knownHostsPath); err == nil {
		var keyErr *knownhosts.KeyError
		if checkErr := existing(knownhosts.Normalize(target.DialAddress()), &net.TCPAddr{}, captured); errors.As(checkErr, &keyErr) && len(keyErr.Want) > 0 {
			return ErrHostKeyChanged
		}
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(target.DialAddress())}, captured) + "\n"
	f, err := os.OpenFile(knownHostsPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("host_key_store_invalid")
	}
	defer f.Close()
	if _, err := io.WriteString(f, line); err != nil {
		return fmt.Errorf("host_key_store_invalid")
	}
	return f.Sync()
}

func validateTarget(target Target) error {
	if strings.TrimSpace(target.Address) == "" || strings.TrimSpace(target.Username) == "" || target.Port < 1 || target.Port > 65535 {
		return fmt.Errorf("ssh_target_invalid")
	}
	return nil
}

func timeout(opts Options) time.Duration {
	if opts.Timeout <= 0 {
		return 15 * time.Second
	}
	return opts.Timeout
}

func secureKnownHostsPath(path string, create bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("host_key_store_invalid")
		}
		path = filepath.Join(home, ".config", "moox", "known_hosts")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("host_key_store_invalid")
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", fmt.Errorf("host_key_store_invalid")
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return "", fmt.Errorf("host_key_store_invalid")
		}
		_ = f.Close()
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("host_key_store_invalid: known_hosts must be a regular 0600 file")
	}
	return path, nil
}

func dialContext(ctx context.Context, address string, config *xssh.ClientConfig, timeout time.Duration) (*xssh.Client, error) {
	raw, err := (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	if tcp, ok := raw.(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	}
	deadline := time.Now().Add(timeout)
	_ = raw.SetDeadline(deadline)
	connection, channels, requests, err := xssh.NewClientConn(raw, address, config)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return xssh.NewClient(connection, channels, requests), nil
}

func isAuthenticationError(err error) bool {
	return strings.Contains(err.Error(), "unable to authenticate") || strings.Contains(err.Error(), "no supported methods remain")
}

func (t *transport) Check(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		_, _, err := t.client.SendRequest("keepalive@moox", true, nil)
		done <- err
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return ErrUnreachable
		}
		return nil
	}
}

func (t *transport) ForwardLocal(ctx context.Context, remote string) (net.Listener, error) {
	if _, _, err := net.SplitHostPort(remote); err != nil {
		return nil, fmt.Errorf("ssh_forward_target_invalid")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("ssh_forward_failed")
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	go func() {
		for {
			local, err := listener.Accept()
			if err != nil {
				return
			}
			go t.forwardConnection(local, remote)
		}
	}()
	return listener, nil
}

func (t *transport) forwardConnection(local net.Conn, remote string) {
	upstream, err := t.client.Dial("tcp", remote)
	if err != nil {
		_ = local.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(local, upstream); done <- struct{}{} }()
	go func() { _, _ = io.Copy(upstream, local); done <- struct{}{} }()
	<-done
	_ = local.Close()
	_ = upstream.Close()
}

func (t *transport) Download(ctx context.Context, src string, dst io.Writer) (int64, error) {
	client, err := sftp.NewClient(t.client)
	if err != nil {
		return 0, fmt.Errorf("ssh_download_failed: %w", err)
	}
	defer closeSFTPClient(client)
	file, err := client.Open(src)
	if err != nil {
		return 0, fmt.Errorf("ssh_download_failed: %w", err)
	}
	defer file.Close()
	return io.Copy(dst, &contextReader{ctx: ctx, reader: file})
}

func (t *transport) Upload(ctx context.Context, src io.Reader, size int64, dst string, mode fs.FileMode) error {
	if size < 0 || !mode.IsRegular() {
		return fmt.Errorf("ssh_upload_invalid")
	}
	client, err := sftp.NewClient(t.client)
	if err != nil {
		return fmt.Errorf("ssh_upload_failed: %w", err)
	}
	defer closeSFTPClient(client)
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("ssh_upload_failed")
	}
	temporary := dst + ".next-" + hex.EncodeToString(suffix)
	file, err := client.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return fmt.Errorf("ssh_upload_failed: %w", err)
	}
	fileOpen := true
	removeTemporary := true
	// 发布包里有全部密钥：写入任何内容之前先收紧权限，不能等写完再 chmod（服务端按 umask 创建，通常是 0644）。
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = client.Remove(temporary)
		return fmt.Errorf("ssh_upload_failed: 无法收紧临时文件权限: %w", err)
	}
	defer func() {
		if fileOpen {
			_ = file.Close()
		}
		if removeTemporary {
			_ = client.Remove(temporary)
		}
	}()
	written, err := io.Copy(file, &contextReader{ctx: ctx, reader: io.LimitReader(src, size+1)})
	if err != nil {
		return fmt.Errorf("ssh_upload_failed: %w", err)
	}
	if written != size {
		return fmt.Errorf("ssh_upload_failed")
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("ssh_upload_failed")
	}
	if err := file.Sync(); err != nil && !strings.Contains(err.Error(), "fsync not supported") {
		return fmt.Errorf("ssh_upload_failed")
	}
	fileOpen = false
	if err := file.Close(); err != nil {
		return fmt.Errorf("ssh_upload_failed: %w", err)
	}
	// OpenSSH SFTP rename refuses to replace an existing file. `mv -f` replaces
	// in place so a failed install cannot delete the previous secret first.
	result, err := t.Run(ctx, []string{"mv", "-f", temporary, dst}, nil)
	if err != nil {
		if result.ExitCode != 0 {
			return fmt.Errorf("ssh_upload_failed")
		}
		return fmt.Errorf("ssh_upload_failed: %w", err)
	}
	removeTemporary = false
	return nil
}

func closeSFTPClient(client *sftp.Client) {
	done := make(chan struct{})
	go func() {
		_ = client.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		// Some OpenSSH servers acknowledge the final rename but never finish
		// the SFTP subsystem close handshake. The parent SSH connection owns
		// the channel and will release it when the setup command completes.
	}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(p)
	}
}

func (t *transport) Run(ctx context.Context, argv []string, stdin io.Reader) (Result, error) {
	if len(argv) == 0 {
		return Result{}, fmt.Errorf("ssh_command_invalid")
	}
	session, err := t.client.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("ssh_command_failed: %w", err)
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	session.Stdin = stdin
	command := make([]string, len(argv))
	for i, arg := range argv {
		command[i] = shellQuote(arg)
	}
	done := make(chan error, 1)
	go func() { done <- session.Run(strings.Join(command, " ")) }()
	select {
	case <-ctx.Done():
		_ = session.Close()
		return Result{}, ctx.Err()
	case err := <-done:
		result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
		if err == nil {
			return result, nil
		}
		var exitErr *xssh.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitStatus()
		}
		return result, fmt.Errorf("ssh_command_failed: %w", err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (t *transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	return t.client.Close()
}
