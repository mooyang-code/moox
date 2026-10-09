// Package gateway 为 moox-cli 提供隧道方式的 gatewayclient：经 SSH 隧道从 control 的主机网关取服务目录，
// 再经隧道连目标主机网关的本机入口（127.0.0.1:11002）。操作员机器不是任何一台 MooX 主机。
package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// DialFunc 建立到一台主机的 SSH 连接。
type DialFunc func(ctx context.Context, hostID string) (setupssh.Client, error)

// SSHTunnel 实现 gatewayclient.Tunnel：每台主机复用一条 SSH 连接和一个转发到主机网关本机入口的本地端口。
type SSHTunnel struct {
	dial    DialFunc
	mu      sync.Mutex
	closed  bool
	tunnels map[string]*hostTunnel
}

type hostTunnel struct {
	client   setupssh.Client
	listener net.Listener
}

// NewSSHTunnel 创建隧道；dial 负责按主机 ID 建立 SSH 连接。
func NewSSHTunnel(dial DialFunc) *SSHTunnel {
	return &SSHTunnel{dial: dial, tunnels: map[string]*hostTunnel{}}
}

// Address 返回转发到 hostID 主机网关本机入口的本地地址，首次调用时建立隧道。
func (t *SSHTunnel) Address(ctx context.Context, hostID string) (string, error) {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return "", errors.New("隧道需要主机 ID")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return "", errors.New("隧道已关闭")
	}
	if tunnel, ok := t.tunnels[hostID]; ok {
		return tunnel.listener.Addr().String(), nil
	}
	client, err := t.dial(ctx, hostID)
	if err != nil {
		return "", fmt.Errorf("SSH 连接主机 %s: %w", hostID, err)
	}
	listener, err := client.ForwardLocal(ctx, gatewayclient.DefaultLocalAddress)
	if err != nil {
		_ = client.Close()
		return "", fmt.Errorf("建立到主机 %s 主机网关的隧道: %w", hostID, err)
	}
	t.tunnels[hostID] = &hostTunnel{client: client, listener: listener}
	return listener.Addr().String(), nil
}

// Close 关闭全部隧道和 SSH 连接。
func (t *SSHTunnel) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	var errs []error
	for hostID, tunnel := range t.tunnels {
		errs = append(errs, tunnel.listener.Close(), tunnel.client.Close())
		delete(t.tunnels, hostID)
	}
	return errors.Join(errs...)
}

// 签名密钥与目录缓存的默认位置，可用环境变量覆盖。
const (
	EnvKeyFile      = "MOOX_CLI_CALLER_KEY_FILE"
	DefaultKeyFile  = "~/.config/moox/caller-moox-cli.key"
	DefaultCacheDir = "~/.cache/moox/gatewayclient"
)

// Client 是 moox-cli 的 gatewayclient 及其隧道，用完调用 Close。
type Client struct {
	*gatewayclient.Client
	tunnel *SSHTunnel
}

// Close 停止 gatewayclient 并关闭隧道。
func (c *Client) Close() {
	c.Client.Close()
	_ = c.tunnel.Close()
}

// New 以 moox-cli 身份创建隧道方式的 gatewayclient；SSH 连接信息取自 moox.toml。
func New(manifest setupconfig.Manifest, sshOptions setupssh.Options) (*Client, error) {
	return NewWithDial(func(ctx context.Context, hostID string) (setupssh.Client, error) {
		host, err := findHost(manifest, hostID)
		if err != nil {
			return nil, err
		}
		return setupssh.DialHost(ctx, host, sshOptions)
	})
}

// NewWithDial 与 New 相同，但由调用方提供 SSH 连接方式（测试使用）。
func NewWithDial(dial DialFunc) (*Client, error) {
	keyFile := strings.TrimSpace(os.Getenv(EnvKeyFile))
	if keyFile == "" {
		keyFile = DefaultKeyFile
	}
	tunnel := NewSSHTunnel(dial)
	client, err := gatewayclient.New(gatewayclient.Options{
		Config: gatewayclient.Config{
			Mode: gatewayclient.ModeTunnel, Caller: servicecatalog.MooxCLICaller, KeyFile: keyFile, CacheDir: DefaultCacheDir,
		},
		Tunnel: tunnel,
	})
	if err != nil {
		_ = tunnel.Close()
		return nil, err
	}
	return &Client{Client: client, tunnel: tunnel}, nil
}

func findHost(manifest setupconfig.Manifest, hostID string) (setupconfig.Host, error) {
	if host, ok := manifest.Host(strings.TrimSpace(hostID)); ok {
		return host, nil
	}
	return setupconfig.Host{}, fmt.Errorf("moox.toml 中没有主机 %s", hostID)
}
