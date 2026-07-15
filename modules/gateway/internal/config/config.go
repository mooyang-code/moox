// Package config loads and validates the standalone gateway configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

const (
	ServiceAddress               = "127.0.0.1:11002"
	HealthAddress                = "127.0.0.1:11012"
	DefaultRefreshInterval       = 15 * time.Second
	DefaultMaxBodyBytes    int64 = 4 << 20
)

type Config struct {
	Node struct {
		ID string `yaml:"id"`
	} `yaml:"node"`
	Server struct {
		ServiceAddr string `yaml:"service_addr"`
		HealthAddr  string `yaml:"health_addr"`
	} `yaml:"server"`
	ControlPlane struct {
		BaseURL         string        `yaml:"base_url"`
		RefreshInterval time.Duration `yaml:"-"`
		HMACKeyFile     string        `yaml:"hmac_key_file"`
		CAFile          string        `yaml:"ca_file"`
	} `yaml:"control_plane"`
	Auth struct {
		HMACKeyFile string `yaml:"hmac_key_file"`
	} `yaml:"auth"`
	Store struct {
		Path string `yaml:"path"`
	} `yaml:"store"`
	Proxy struct {
		MaxBodyBytes int64 `yaml:"max_body_bytes"`
	} `yaml:"proxy"`
}

type fileConfig struct {
	Node struct {
		ID string `yaml:"id"`
	} `yaml:"node"`
	Server struct {
		ServiceAddr string `yaml:"service_addr"`
		HealthAddr  string `yaml:"health_addr"`
	} `yaml:"server"`
	ControlPlane struct {
		BaseURL         string `yaml:"base_url"`
		RefreshInterval string `yaml:"refresh_interval"`
		HMACKeyFile     string `yaml:"hmac_key_file"`
		CAFile          string `yaml:"ca_file"`
	} `yaml:"control_plane"`
	Auth struct {
		HMACKeyFile string `yaml:"hmac_key_file"`
	} `yaml:"auth"`
	Store struct {
		Path string `yaml:"path"`
	} `yaml:"store"`
	Proxy struct {
		MaxBodyBytes int64 `yaml:"max_body_bytes"`
	} `yaml:"proxy"`
}

func Load(path string) (Config, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read gateway config: %w", err)
	}
	var raw fileConfig
	decoder := yaml.NewDecoder(strings.NewReader(string(encoded)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode gateway config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Config{}, errors.New("decode gateway config: multiple YAML documents are not allowed")
		}
		return Config{}, fmt.Errorf("decode gateway config trailing content: %w", err)
	}
	var cfg Config
	cfg.Node.ID = strings.TrimSpace(raw.Node.ID)
	cfg.Server.ServiceAddr = strings.TrimSpace(raw.Server.ServiceAddr)
	cfg.Server.HealthAddr = strings.TrimSpace(raw.Server.HealthAddr)
	cfg.ControlPlane.BaseURL = strings.TrimRight(strings.TrimSpace(raw.ControlPlane.BaseURL), "/")
	cfg.ControlPlane.HMACKeyFile = resolvePath(path, raw.ControlPlane.HMACKeyFile)
	cfg.ControlPlane.CAFile = resolvePath(path, raw.ControlPlane.CAFile)
	cfg.Auth.HMACKeyFile = resolvePath(path, raw.Auth.HMACKeyFile)
	cfg.Store.Path = resolvePath(path, raw.Store.Path)
	cfg.Proxy.MaxBodyBytes = raw.Proxy.MaxBodyBytes
	if raw.ControlPlane.RefreshInterval == "" {
		cfg.ControlPlane.RefreshInterval = DefaultRefreshInterval
	} else {
		cfg.ControlPlane.RefreshInterval, err = time.ParseDuration(raw.ControlPlane.RefreshInterval)
		if err != nil {
			return Config{}, fmt.Errorf("parse control_plane.refresh_interval: %w", err)
		}
	}
	if cfg.Proxy.MaxBodyBytes == 0 {
		cfg.Proxy.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Validate(cfg Config) error {
	if cfg.Node.ID == "" {
		return errors.New("node.id is required")
	}
	if cfg.Server.ServiceAddr != ServiceAddress {
		return fmt.Errorf("server.service_addr must be %s", ServiceAddress)
	}
	if cfg.Server.HealthAddr != HealthAddress {
		return fmt.Errorf("server.health_addr must be %s", HealthAddress)
	}
	if cfg.ControlPlane.BaseURL == "" {
		return errors.New("control_plane.base_url is required")
	}
	if err := ValidateBaseURL(cfg.ControlPlane.BaseURL); err != nil {
		return fmt.Errorf("control_plane.base_url: %w", err)
	}
	if cfg.ControlPlane.RefreshInterval <= 0 {
		return errors.New("control_plane.refresh_interval must be positive")
	}
	if err := ValidateKeyFile(cfg.ControlPlane.HMACKeyFile); err != nil {
		return fmt.Errorf("control_plane.hmac_key_file: %w", err)
	}
	if err := ValidateKeyFile(cfg.Auth.HMACKeyFile); err != nil {
		return fmt.Errorf("auth.hmac_key_file: %w", err)
	}
	if cfg.ControlPlane.CAFile != "" {
		if info, err := os.Stat(cfg.ControlPlane.CAFile); err != nil || !info.Mode().IsRegular() {
			return errors.New("control_plane.ca_file must be a readable regular file")
		}
	}
	if cfg.Store.Path == "" {
		return errors.New("store.path is required")
	}
	if cfg.Proxy.MaxBodyBytes <= 0 {
		return errors.New("proxy.max_body_bytes must be positive")
	}
	return nil
}

func ValidateBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("must be an HTTP(S) origin")
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		return nil
	}
	if !strings.EqualFold(parsed.Scheme, "http") {
		return errors.New("must use HTTPS or loopback HTTP")
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("plaintext HTTP is allowed only for loopback hosts")
	}
	return nil
}

func ValidateKeyFile(path string) error {
	_, err := readSecretFile(path)
	return err
}

func ReadSecret(path string) (string, error) {
	return readSecretFile(path)
}

func readSecretFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is required")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open key without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return "", errors.New("open key file descriptor")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("permissions must not include group or world bits (got %04o)", info.Mode().Perm())
	}
	const maxSecretBytes = 64 << 10
	value, err := io.ReadAll(io.LimitReader(file, maxSecretBytes+1))
	if err != nil {
		return "", err
	}
	if len(value) > maxSecretBytes {
		return "", errors.New("file is too large")
	}
	secret := strings.TrimSpace(string(value))
	if secret == "" {
		return "", errors.New("file is empty")
	}
	return secret, nil
}

func resolvePath(configPath, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		return filepath.Join(filepath.Dir(configPath), value)
	}
	return filepath.Join(filepath.Dir(absConfig), value)
}
