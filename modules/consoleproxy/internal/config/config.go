// Package config defines the single configuration source for the console proxy.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Public    Public    `yaml:"public"`
	TLS       TLS       `yaml:"tls"`
	Upstreams Upstreams `yaml:"upstreams"`
	Health    Health    `yaml:"health"`
	Lifecycle Lifecycle `yaml:"lifecycle"`
}

type Public struct {
	Host  string `yaml:"host"`
	Bind  string `yaml:"bind"`
	Port  int    `yaml:"port"`
	HTTP3 bool   `yaml:"http3"`
}

type TLS struct {
	Mode         string `yaml:"mode"`
	StorageRoot  string `yaml:"storage_root"`
	CABaseline   string `yaml:"ca_baseline"`
	CAPublishDir string `yaml:"ca_publish_dir"`
	Email        string `yaml:"email"`
	ACMECA       string `yaml:"acme_ca"`
}

type Upstreams struct {
	Admin string `yaml:"admin"`
	Web   string `yaml:"web"`
}

type Health struct {
	Listen string `yaml:"listen"`
}

type Lifecycle struct {
	DrainTimeout      time.Duration `yaml:"drain_timeout"`
	EngineStopTimeout time.Duration `yaml:"engine_stop_timeout"`
	CleanupMargin     time.Duration `yaml:"cleanup_margin"`
	StartupTimeout    time.Duration `yaml:"startup_timeout"`
}

func Defaults() Config {
	return Config{
		Public:    Public{Host: "localhost", Bind: "0.0.0.0", Port: 9527, HTTP3: true},
		TLS:       TLS{Mode: "internal", StorageRoot: "../data/caddy/caddy", CABaseline: "../data/caddy/internal-ca.sha256", CAPublishDir: "../certs/caddy"},
		Upstreams: Upstreams{Admin: "127.0.0.1:11000", Web: "127.0.0.1:9528"},
		Health:    Health{Listen: "127.0.0.1:19528"},
		Lifecycle: Lifecycle{DrainTimeout: 30 * time.Second, EngineStopTimeout: 10 * time.Second, CleanupMargin: 5 * time.Second, StartupTimeout: 3 * time.Minute},
	}
}

// Load performs only parsing and structural validation, with no provisioning,
// certificate issuance, state mutation, or network access.
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	cfg := Defaults()
	raw, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil {
		return Config{}, err
	}
	if len(raw) > 64*1024 {
		return Config{}, errors.New("console-proxy config exceeds 64 KiB")
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode console-proxy config: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Config{}, errors.New("console-proxy config must contain exactly one YAML document")
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return Config{}, err
	}
	for _, p := range []*string{&cfg.TLS.StorageRoot, &cfg.TLS.CABaseline, &cfg.TLS.CAPublishDir} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(base, *p)
		}
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	host := c.Public.Host
	if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "/*?@#{}\\\r\n\t ") || strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return errors.New("public.host must be a literal IP or DNS hostname")
	}
	if net.ParseIP(host) == nil {
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return errors.New("invalid public hostname")
			}
			for _, ch := range label {
				if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
					return errors.New("invalid public hostname")
				}
			}
		}
		if len(host) > 253 {
			return errors.New("public hostname is too long")
		}
	}
	if net.ParseIP(c.Public.Bind) == nil {
		return errors.New("public.bind must be a literal IP address")
	}
	if c.Public.Port < 1 || c.Public.Port > 65535 {
		return errors.New("public.port must be in 1..65535")
	}
	if c.TLS.Mode != "internal" && c.TLS.Mode != "public" {
		return errors.New("tls.mode must be internal or public")
	}
	for _, p := range []string{c.TLS.StorageRoot, c.TLS.CABaseline, c.TLS.CAPublishDir} {
		if p == "" {
			return errors.New("TLS state paths must be explicit")
		}
	}
	if c.TLS.ACMECA != "" {
		u, err := url.Parse(c.TLS.ACMECA)
		if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Scheme != "https" && !(u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback()) {
			return errors.New("acme_ca must be HTTPS, or HTTP on a literal loopback test server")
		}
	}
	if err := loopbackAddress(c.Health.Listen); err != nil {
		return fmt.Errorf("health: %w", err)
	}
	publicAddress := net.JoinHostPort(c.Public.Bind, fmt.Sprint(c.Public.Port))
	if overlaps(publicAddress, c.Health.Listen) {
		return errors.New("public and health listeners overlap")
	}
	for _, addr := range []string{c.Upstreams.Admin, c.Upstreams.Web} {
		if err := loopbackAddress(addr); err != nil {
			return fmt.Errorf("upstream: %w", err)
		}
		if overlaps(publicAddress, addr) || overlaps(c.Health.Listen, addr) {
			return errors.New("upstream must not point at proxy listeners")
		}
	}
	for name, d := range map[string]time.Duration{"drain_timeout": c.Lifecycle.DrainTimeout, "engine_stop_timeout": c.Lifecycle.EngineStopTimeout, "cleanup_margin": c.Lifecycle.CleanupMargin, "startup_timeout": c.Lifecycle.StartupTimeout} {
		if d <= 0 || d > 10*time.Minute {
			return fmt.Errorf("%s must be positive and at most 10m", name)
		}
	}
	return nil
}

// The inputs have already been checked as literal IP addresses and ports.
// An unspecified bind can accept loopback traffic too; comparing address
// strings alone would allow the proxy to route requests back to itself.
func overlaps(a, b string) bool {
	ah, ap, _ := net.SplitHostPort(a)
	bh, bp, _ := net.SplitHostPort(b)
	ipA, ipB := net.ParseIP(ah), net.ParseIP(bh)
	return ap == bp && (ipA.Equal(ipB) || ipA.IsUnspecified() || ipB.IsUnspecified())
}

func loopbackAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("address must use a literal loopback IP")
	}
	var p int
	if _, err := fmt.Sscanf(port, "%d", &p); err != nil || fmt.Sprint(p) != port || p < 1 || p > 65535 {
		return errors.New("invalid address port")
	}
	return nil
}

func (c Config) StopBudget() time.Duration {
	return c.Lifecycle.DrainTimeout + c.Lifecycle.EngineStopTimeout + c.Lifecycle.CleanupMargin
}
