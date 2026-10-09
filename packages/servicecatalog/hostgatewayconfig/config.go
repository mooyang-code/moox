// Package hostgatewayconfig defines the deployment and runtime configuration
// contract for a host gateway. It contains paths and public identity only.
package hostgatewayconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Host    Host    `yaml:"host"`
	Server  Server  `yaml:"server"`
	TLS     TLS     `yaml:"tls"`
	Control Control `yaml:"control"`
	Store   Store   `yaml:"store"`
}

type Host struct {
	ID string `yaml:"id"`
}

type Server struct {
	RemoteAddr string `yaml:"remote_addr"`
	LocalAddr  string `yaml:"local_addr"`
	HealthAddr string `yaml:"health_addr"`
}

type TLS struct {
	CertificateFile string `yaml:"cert_file"`
	KeyFile         string `yaml:"key_file"`
	CAFile          string `yaml:"ca_file"`
}

type Control struct {
	HostID  string `yaml:"host_id"`
	Target  string `yaml:"target"`
	Caller  string `yaml:"caller"`
	KeyID   string `yaml:"key_id"`
	KeyFile string `yaml:"key_file"`
}

type Store struct {
	Path string `yaml:"path"`
}

// Default uses the release directory layout. Relative paths are resolved from
// hostgateway/config/app.yaml; all hosts share the same path contract.
func Default(hostID, controlHostID, controlAddress, keyID string) Config {
	target := net.JoinHostPort(controlAddress, "11003")
	if hostID == controlHostID {
		target = "127.0.0.1:11112"
	}
	return Config{
		Host:    Host{ID: hostID},
		Server:  Server{RemoteAddr: "0.0.0.0:11003", LocalAddr: "127.0.0.1:11002", HealthAddr: "0.0.0.0:11012"},
		TLS:     TLS{CertificateFile: "../../certs/host-gateway/server.crt", KeyFile: "../../certs/host-gateway/server.key", CAFile: "../../certs/moox-ca.crt"},
		Control: Control{HostID: controlHostID, Target: target, Caller: "host-gateway@" + hostID, KeyID: keyID, KeyFile: "../../secrets/caller-host-gateway.key"},
		Store:   Store{Path: "../../data/host-gateway"},
	}
}

func endpoint(value string, listener bool) (string, int, error) {
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || value != strings.TrimSpace(value) || !servicecatalog.ValidHostAddress(host) {
		return "", 0, errors.New("requires a bare IP/DNS and port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 || strconv.Itoa(n) != port || listener && net.ParseIP(host) == nil {
		return "", 0, errors.New("requires a valid TCP endpoint; listeners use literal IPs")
	}
	return host, n, nil
}

func (c Config) Validate() error {
	if !servicecatalog.ValidHostID(c.Host.ID) || !servicecatalog.ValidHostID(c.Control.HostID) || c.Control.Caller != "host-gateway@"+c.Host.ID {
		return errors.New("host/control IDs must be canonical and control.caller must match this host")
	}
	ports := map[int]bool{}
	for _, listener := range []struct{ name, address string }{
		{"remote_addr", c.Server.RemoteAddr}, {"local_addr", c.Server.LocalAddr}, {"health_addr", c.Server.HealthAddr},
	} {
		host, port, err := endpoint(listener.address, true)
		if err != nil {
			return fmt.Errorf("server.%s: %w", listener.name, err)
		}
		if listener.name == "local_addr" && !net.ParseIP(host).IsLoopback() {
			return errors.New("server.local_addr must bind a loopback IP")
		}
		if ports[port] {
			return errors.New("host gateway listener ports must be distinct")
		}
		ports[port] = true
	}
	host, port, err := endpoint(c.Control.Target, false)
	if err != nil {
		return fmt.Errorf("control.target: %w", err)
	}
	if c.Host.ID == c.Control.HostID && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || port != 11112) {
		return errors.New("control host must call GatewayControl directly on loopback port 11112")
	}
	if c.Control.KeyID == "" || len(c.Control.KeyID) > 128 || strings.ContainsAny(c.Control.KeyID, "/\\") || strings.ContainsFunc(c.Control.KeyID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return errors.New("control.key_id is required and must be a bounded credential identifier")
	}
	for _, path := range []string{c.TLS.CertificateFile, c.TLS.KeyFile, c.TLS.CAFile, c.Control.KeyFile, c.Store.Path} {
		if path == "" || path != strings.TrimSpace(path) || strings.ContainsAny(path, "\x00\r\n") {
			return errors.New("TLS, control key and store paths are required")
		}
	}
	return nil
}

// Decode validates only configuration structure. Runtime TLS validation must
// also verify the file contents, private CA and this host's certificate SAN.
func Decode(reader io.Reader) (Config, error) {
	var config Config
	raw, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return Config{}, errors.New("host gateway configuration cannot be read or exceeds 1 MiB")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, errors.New("invalid host gateway configuration YAML or unknown fields")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Config{}, errors.New("host gateway configuration requires exactly one YAML document")
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	config, err := Decode(file)
	if err != nil {
		return Config{}, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Config{}, err
	}
	for _, value := range []*string{&config.TLS.CertificateFile, &config.TLS.KeyFile, &config.TLS.CAFile, &config.Control.KeyFile, &config.Store.Path} {
		if !filepath.IsAbs(*value) {
			*value = filepath.Join(filepath.Dir(abs), *value)
		}
	}
	return config, nil
}

func Encode(config Config) ([]byte, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(config)
}
