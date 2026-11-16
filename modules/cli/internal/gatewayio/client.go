// Package gatewayio 持有操作电脑的网关客户端与 SSH 隧道：以 moox-cli 的身份、用这台电脑上的私钥签名。
package gatewayio

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// 操作电脑身份目录（~/.config/moox）里的两个文件：moox-cli 的签名私钥（setup operator 生成），
// 以及部署控制机时记下的 MooX CA 根证书。
const (
	OperatorKeyFile = "moox-cli.key"
	OperatorCAFile  = "moox-ca.crt"
)

// Client owns all connections, including the directory source borrowed by the
// shared client. Closing it first joins directory refresh, then closes SSH.
type Client struct {
	*gatewayclient.Client
	source  *directorySource
	tunnels *tunnels
	once    sync.Once
	err     error
}

// sshHost 是建 SSH 隧道要用的一台主机。
type sshHost struct {
	name, address, username string
	port                    int
	identityFiles           []string
}

// Open 按 moox.toml 的主机经 SSH 隧道连到各主机网关，用本机 ~/.config/moox/moox-cli.key 签名。
func Open(ctx context.Context, cfg *topology.Config) (*Client, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, errors.New("gateway tunnel requires a live context and setup configuration")
	}
	hosts := make([]sshHost, 0, len(cfg.HostCatalog))
	for _, host := range cfg.Hosts() {
		hosts = append(hosts, sshHost{name: host.Name, address: host.Address, username: host.Username, port: host.Port, identityFiles: host.IdentityFiles()})
	}
	options := setupssh.Options{Timeout: 5 * time.Second}
	return open(ctx, hosts, cfg.ControlHost().Name, filepath.Join(home, ".config", "moox"), options)
}

func open(ctx context.Context, hosts []sshHost, control, identityDirectory string, options setupssh.Options) (*Client, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errors.New("gateway tunnel requires a live context and setup configuration")
	}
	keyPath := filepath.Join(identityDirectory, OperatorKeyFile)
	if _, err := os.Lstat(keyPath); os.IsNotExist(err) {
		return nil, errors.New("admin_not_ready: 这台电脑没有 moox-cli 的签名私钥。系统还没装时运行 setup validate 看下一步；已经装好、换了电脑时运行 moox-cli setup operator --register")
	}
	key, err := gatewayauth.ReadSigningKey(keyPath)
	if err != nil {
		return nil, fmt.Errorf("load operator signing key: %w", err)
	}
	lifetime, cancel := context.WithCancel(ctx)
	resolver := &tunnels{ctx: lifetime, cancel: cancel, options: options, hosts: make(map[string]sshHost), connections: make(map[string]*tunnel)}
	for _, host := range hosts {
		if _, exists := resolver.hosts[host.name]; exists {
			cancel()
			return nil, fmt.Errorf("duplicate configured SSH host %q", host.name)
		}
		resolver.hosts[host.name] = host
	}
	source := &directorySource{control: control, tunnels: resolver}
	// 部署控制机时记下的 MooX CA；连接只能出站的主机（经 control 中转）时用它校验证书。
	caFile := filepath.Join(identityDirectory, OperatorCAFile)
	if _, err := os.Stat(caFile); err != nil {
		caFile = ""
	}
	shared, err := gatewayclient.New(gatewayclient.Config{
		Mode: gatewayclient.Tunnel, Credentials: gatewayauth.Credentials{Caller: "moox-cli", Key: key},
		Source: source, Tunnels: resolver, CAFile: caFile,
	})
	if err != nil {
		_ = source.Close()
		_ = resolver.Close()
		return nil, err
	}
	return &Client{Client: shared, source: source, tunnels: resolver}, nil
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.once.Do(func() {
		c.err = errors.Join(c.Client.Close(), c.source.Close(), c.tunnels.Close())
	})
	return c.err
}

type tunnel struct {
	ssh      setupssh.Client
	listener net.Listener
}

type tunnels struct {
	ctx         context.Context
	cancel      context.CancelFunc
	options     setupssh.Options
	hosts       map[string]sshHost
	mu          sync.Mutex
	connections map[string]*tunnel
	closed      bool
}

// Resolve ignores public addresses in Directory. Only a configured SSH host
// may supply a local forward, always to that host's 127.0.0.1:11002.
func (t *tunnels) Resolve(ctx context.Context, hostID string) (string, error) {
	return t.forward(ctx, hostID, hostID, "127.0.0.1:11002")
}

// ResolveRelay 经 SSH 转发到 control 的跨主机端口 127.0.0.1:11003，用来经中转连接只能出站的主机；
// 不会去 SSH 直连出站主机。
func (t *tunnels) ResolveRelay(ctx context.Context, controlHostID string) (string, error) {
	return t.forward(ctx, "relay/"+controlHostID, controlHostID, "127.0.0.1:11003")
}

func (t *tunnels) forward(ctx context.Context, key, hostID, remote string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.ctx.Err() != nil {
		return "", net.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	host, ok := t.hosts[hostID]
	if !ok {
		return "", fmt.Errorf("directory host %q has no configured SSH identity", hostID)
	}
	if connection := t.connections[key]; connection != nil {
		if err := connection.ssh.Check(ctx); err == nil {
			return connection.listener.Addr().String(), nil
		} else if ctx.Err() != nil {
			return "", ctx.Err()
		}
		_ = connection.listener.Close()
		_ = connection.ssh.Close()
		delete(t.connections, key)
	}
	options := t.options
	options.IdentityFiles = host.identityFiles
	transport, err := setupssh.Dial(ctx, setupssh.Target{Name: host.name, Address: host.address, Port: host.port, Username: host.username}, "", options)
	if err != nil {
		return "", fmt.Errorf("open gateway SSH tunnel for %s: %w", hostID, err)
	}
	listener, err := transport.ForwardLocal(t.ctx, remote)
	if err != nil {
		_ = transport.Close()
		return "", err
	}
	t.connections[key] = &tunnel{ssh: transport, listener: listener}
	return listener.Addr().String(), nil
}

func (t *tunnels) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	t.cancel()
	var result error
	for _, connection := range t.connections {
		_ = connection.listener.Close() // cancellation may already close it
		result = errors.Join(result, connection.ssh.Close())
	}
	return result
}

type directorySource struct {
	control string
	tunnels *tunnels
	mu      sync.Mutex
	client  *gatewayclient.LocalDirectorySource
	address string
	closed  bool
}

func (s *directorySource) Fetch(ctx context.Context, version string) (gatewayclient.DirectoryUpdate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return gatewayclient.DirectoryUpdate{}, net.ErrClosed
	}
	address, err := s.tunnels.Resolve(ctx, s.control)
	if err != nil {
		return gatewayclient.DirectoryUpdate{}, err
	}
	if s.client == nil || s.address != address {
		if s.client != nil {
			_ = s.client.Close()
		}
		s.client, err = gatewayclient.NewLocalDirectorySource(address, 5*time.Second)
		if err != nil {
			return gatewayclient.DirectoryUpdate{}, err
		}
		s.address = address
	}
	return s.client.Fetch(ctx, version)
}

func (s *directorySource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}
