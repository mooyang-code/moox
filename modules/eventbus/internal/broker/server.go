// Package broker owns the only production embedded NATS server.
package broker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	natsserver "github.com/nats-io/nats-server/v2/server"
	yaml "gopkg.in/yaml.v3"
	trpc "trpc.group/trpc-go/trpc-go"
)

type Server struct {
	cfg *config.Config
	ns  *natsserver.Server
	mu  sync.RWMutex
}

type usersFile struct {
	Users []userEntry `yaml:"users"`
}

type userEntry struct {
	Username    string          `yaml:"username"`
	Password    string          `yaml:"password"`
	Permissions userPermissions `yaml:"permissions"`
}

type userPermissions struct {
	Publish   subjectPermission `yaml:"publish"`
	Subscribe subjectPermission `yaml:"subscribe"`
}

type subjectPermission struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

func New(cfg *config.Config) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("broker config is nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Broker.StoreDir, 0o750); err != nil {
		return nil, fmt.Errorf("create broker store dir: %w", err)
	}
	opts := &natsserver.Options{
		ServerName: cfg.Broker.ServerName, Host: cfg.Broker.Host, Port: cfg.Broker.Port,
		JetStream: true, JetStreamStrict: true, StoreDir: cfg.Broker.StoreDir,
		MaxPayload: int32(cfg.Broker.MaxPayloadBytes), NoSigs: true, DisableJetStreamBanner: true,
	}
	if cfg.Broker.ClientAdvertise != "" {
		opts.ClientAdvertise = cfg.Broker.ClientAdvertise
	}
	var clusterTLS *tls.Config
	// JetStream's account store limit defaults to a small development value.
	// Raise it to cover the sum of declared stream limits while still keeping
	// each configured per-stream max_bytes as the effective retention bound.
	for _, stream := range cfg.Streams {
		if stream.MaxBytes > 0 {
			opts.JetStreamMaxStore += stream.MaxBytes
		}
	}
	if cfg.Broker.Auth.Enabled {
		if cfg.Broker.Auth.UsersFile != "" {
			users, err := loadUsersFile(cfg.Broker.Auth.UsersFile)
			if err != nil {
				return nil, err
			}
			opts.Users = users
		} else {
			opts.Username, opts.Password = cfg.Broker.Auth.Username, cfg.Broker.Auth.Password
		}
	}
	if cfg.Broker.TLS.Enabled {
		opts.TLS, opts.TLSCert, opts.TLSKey, opts.TLSCaCert = true, cfg.Broker.TLS.CertFile, cfg.Broker.TLS.KeyFile, cfg.Broker.TLS.CAFile
		cert, err := tls.LoadX509KeyPair(cfg.Broker.TLS.CertFile, cfg.Broker.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load broker tls certificate: %w", err)
		}
		if err := validateServerCertificate(cfg, cert); err != nil {
			return nil, err
		}
		tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		if cfg.Broker.TLS.CAFile != "" {
			caPEM, err := os.ReadFile(cfg.Broker.TLS.CAFile)
			if err != nil {
				return nil, fmt.Errorf("read broker tls ca: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caPEM) {
				return nil, fmt.Errorf("parse broker tls ca")
			}
			tlsConfig.RootCAs, tlsConfig.ClientCAs = pool, pool
		}
		opts.TLSConfig = tlsConfig
		clusterTLS = tlsConfig.Clone()
	}
	if cfg.Broker.Cluster.Enabled {
		opts.Cluster = natsserver.ClusterOpts{Name: cfg.Broker.Cluster.Name, Host: cfg.Broker.Cluster.Host, Port: cfg.Broker.Cluster.Port}
		if clusterTLS != nil {
			opts.Cluster.TLSConfig = clusterTLS
		}
		if username, password := clusterCredentials(cfg); username != "" {
			// Route connections authenticate separately from client connections.
			// Copy credentials into ClusterOpts or clustered nodes cannot form a
			// route even though local clients authenticate successfully.
			opts.Cluster.Username = username
			opts.Cluster.Password = password
		}
		for _, route := range cfg.Broker.Cluster.Routes {
			u, err := url.Parse(route)
			if err != nil || u.Scheme == "" || u.Host == "" {
				return nil, fmt.Errorf("invalid cluster route %q", route)
			}
			opts.Routes = append(opts.Routes, u)
		}
	}
	ns, err := natsserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("create nats server: %w", err)
	}
	return &Server{cfg: cfg, ns: ns}, nil
}

func validateServerCertificate(cfg *config.Config, cert tls.Certificate) error {
	if cfg == nil || len(cert.Certificate) == 0 {
		return fmt.Errorf("broker tls certificate is empty")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse broker tls certificate: %w", err)
	}
	hosts := []string{}
	for _, value := range []string{cfg.Broker.Host, cfg.Broker.ClientAdvertise} {
		if value == "" || !configPublicHost(value) {
			continue
		}
		host := value
		if parsed, _, splitErr := net.SplitHostPort(value); splitErr == nil {
			host = parsed
		}
		hosts = append(hosts, strings.Trim(host, "[]"))
	}
	for _, host := range hosts {
		if err := leaf.VerifyHostname(host); err != nil {
			return fmt.Errorf("broker tls certificate does not cover advertised host %q: %w", host, err)
		}
	}
	return nil
}
func configPublicHost(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	return value != "127.0.0.1" && value != "localhost" && value != "::1" && value != "[::1]" && value != "0.0.0.0" && value != "::"
}

func loadUsersFile(path string) ([]*natsserver.User, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("users file path is empty")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat users file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("users file must be a regular non-symlink file with mode 0600")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return nil, fmt.Errorf("users file owner does not match process uid")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read users file: %w", err)
	}
	var parsed usersFile
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse users file: %w", err)
	}
	if len(parsed.Users) == 0 {
		return nil, fmt.Errorf("users file has no users")
	}
	users := make([]*natsserver.User, 0, len(parsed.Users))
	seen := make(map[string]struct{}, len(parsed.Users))
	for _, item := range parsed.Users {
		if strings.TrimSpace(item.Username) == "" || item.Password == "" {
			return nil, fmt.Errorf("users file contains empty username/password")
		}
		if _, ok := seen[item.Username]; ok {
			return nil, fmt.Errorf("users file contains duplicate username %q", item.Username)
		}
		seen[item.Username] = struct{}{}
		users = append(users, &natsserver.User{
			Username: item.Username,
			Password: item.Password,
			Permissions: &natsserver.Permissions{
				Publish:   &natsserver.SubjectPermission{Allow: append([]string(nil), item.Permissions.Publish.Allow...), Deny: append([]string(nil), item.Permissions.Publish.Deny...)},
				Subscribe: &natsserver.SubjectPermission{Allow: append([]string(nil), item.Permissions.Subscribe.Allow...), Deny: append([]string(nil), item.Permissions.Subscribe.Deny...)},
			},
		})
	}
	return users, nil
}

func clusterCredentials(cfg *config.Config) (string, string) {
	if cfg == nil || !cfg.Broker.Cluster.Enabled || !cfg.Broker.Auth.Enabled {
		return "", ""
	}
	return cfg.Broker.Auth.Username, cfg.Broker.Auth.Password
}

func (s *Server) Start(ctx context.Context) error {
	if s == nil || s.ns == nil {
		return fmt.Errorf("broker server is nil")
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.ns.Start()
	timeout := 10 * time.Second
	if s.cfg != nil && s.cfg.Broker.StartupTimeout > 0 {
		timeout = s.cfg.Broker.StartupTimeout
	}
	ready := make(chan bool, 1)
	go func() { ready <- s.ns.ReadyForConnections(timeout) }()
	select {
	case ok := <-ready:
		if !ok {
			s.ns.Shutdown()
			return fmt.Errorf("nats server did not become ready")
		}
		return nil
	case <-ctx.Done():
		s.ns.Shutdown()
		return fmt.Errorf("broker startup: %w", ctx.Err())
	case <-time.After(timeout):
		s.ns.Shutdown()
		return fmt.Errorf("broker startup timeout after %s", timeout)
	}
}

func (s *Server) URL() string {
	if s == nil || s.ns == nil {
		return ""
	}
	u := s.ns.ClientURL()
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	if host, _, err := net.SplitHostPort(parsed.Host); err == nil && (host == "0.0.0.0" || host == "::") {
		parsed.Host = net.JoinHostPort("127.0.0.1", parsed.Port())
	}
	return parsed.String()
}

func (s *Server) Ready() bool { return s != nil && s.ns != nil && s.ns.Running() }
func (s *Server) Connections() uint32 {
	if s == nil || s.ns == nil {
		return 0
	}
	return uint32(s.ns.NumClients())
}
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.ns == nil {
		return nil
	}
	if !s.ns.Running() {
		return nil
	}
	s.ns.Shutdown()
	done := make(chan struct{})
	go func() { s.ns.WaitForShutdown(); close(done) }()
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) Config() *config.Config {
	if s == nil {
		return nil
	}
	return s.cfg
}
func (s *Server) String() string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.URL())
}
