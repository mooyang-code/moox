// Package gatewayio owns the operator's signed gateway client and SSH tunnels.
package gatewayio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
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

func Open(ctx context.Context, snapshot *setupconfig.Snapshot) (*Client, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return open(ctx, snapshot, filepath.Join(home, ".config", "moox", "gateway-client.yaml"), setupssh.Options{Timeout: 5 * time.Second})
}

// OpenWithIdentity uses an explicitly selected native operator identity.
func OpenWithIdentity(ctx context.Context, snapshot *setupconfig.Snapshot, identityPath string, options setupssh.Options) (*Client, error) {
	return open(ctx, snapshot, identityPath, options)
}

func open(ctx context.Context, snapshot *setupconfig.Snapshot, identityPath string, options setupssh.Options) (*Client, error) {
	if snapshot == nil || ctx == nil || ctx.Err() != nil {
		return nil, errors.New("gateway tunnel requires a live context and setup configuration")
	}
	identity, err := loadIdentity(identityPath)
	if err != nil {
		return nil, err
	}
	keyPath := identity.KeyFile
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(filepath.Dir(identityPath), keyPath)
	}
	secret, err := gatewayauth.ReadSigningSecret(keyPath)
	if err != nil {
		return nil, fmt.Errorf("load operator signing key: %w", err)
	}
	lifetime, cancel := context.WithCancel(ctx)
	resolver := &tunnels{ctx: lifetime, cancel: cancel, options: options, hosts: make(map[string]setupconfig.Host), connections: make(map[string]*tunnel)}
	for _, host := range snapshot.Manifest.Hosts() {
		if _, exists := resolver.hosts[host.Name]; exists {
			cancel()
			return nil, fmt.Errorf("duplicate configured SSH host %q", host.Name)
		}
		resolver.hosts[host.Name] = host
	}
	source := &directorySource{control: snapshot.Manifest.ControlHost().Name, tunnels: resolver}
	shared, err := gatewayclient.New(gatewayclient.Config{
		Mode: gatewayclient.Tunnel, Credentials: gatewayauth.Credentials{Caller: "moox-cli", KeyID: identity.KeyID, Secret: secret},
		Source: source, Tunnels: resolver,
	})
	if err != nil {
		_ = source.Close()
		_ = resolver.Close()
		return nil, err
	}
	return &Client{Client: shared, source: source, tunnels: resolver}, nil
}

func loadIdentity(path string) (gatewayclient.FileConfig, error) {
	var config gatewayclient.FileConfig
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 4096 {
		return config, errors.New("operator gateway configuration must be a regular 0600 file of at most 4096 bytes")
	}
	file, err := os.Open(path)
	if err != nil {
		return config, err
	}
	defer file.Close()
	decoder := yaml.NewDecoder(io.LimitReader(file, 4097))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode operator gateway configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return config, errors.New("operator gateway configuration requires exactly one YAML document")
	}
	if config.Caller != "moox-cli" || config.KeyID == "" {
		return config, errors.New("operator gateway configuration requires caller moox-cli and an Admin-assigned key_id")
	}
	if len(config.KeyID) > 128 || strings.ContainsAny(config.KeyID, "/\\") || strings.ContainsFunc(config.KeyID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) ||
		config.KeyFile == "" || config.KeyFile != strings.TrimSpace(config.KeyFile) || strings.ContainsAny(config.KeyFile, "\x00\r\n") {
		return config, errors.New("operator gateway configuration has an invalid key_id or key_file")
	}
	return config, nil
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
	hosts       map[string]setupconfig.Host
	mu          sync.Mutex
	connections map[string]*tunnel
	closed      bool
}

// Resolve ignores public addresses in Directory. Only a configured SSH host
// may supply a local forward, always to that host's 127.0.0.1:11002.
func (t *tunnels) Resolve(ctx context.Context, hostID string) (string, error) {
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
	if connection := t.connections[hostID]; connection != nil {
		if err := connection.ssh.Check(ctx); err == nil {
			return connection.listener.Addr().String(), nil
		} else if ctx.Err() != nil {
			return "", ctx.Err()
		}
		_ = connection.listener.Close()
		_ = connection.ssh.Close()
		delete(t.connections, hostID)
	}
	transport, err := setupssh.Dial(ctx, setupssh.Target{Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username}, host.Password, t.options)
	if err != nil {
		return "", fmt.Errorf("open gateway SSH tunnel for %s: %w", hostID, err)
	}
	listener, err := transport.ForwardLocal(t.ctx, "127.0.0.1:11002")
	if err != nil {
		_ = transport.Close()
		return "", err
	}
	t.connections[hostID] = &tunnel{ssh: transport, listener: listener}
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
