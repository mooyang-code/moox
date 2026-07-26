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

	"gopkg.in/yaml.v3"
)

const (
	ServiceAddress                   = "127.0.0.1:11002"
	NativeServiceAddress             = "127.0.0.1:11003"
	PublicNativeServiceAddress       = "0.0.0.0:11003"
	HealthAddress                    = "127.0.0.1:11012"
	DefaultMaxBodyBytes        int64 = 4 << 20
)

type Config struct {
	Node struct {
		ID string `yaml:"id"`
	} `yaml:"node"`
	Server struct {
		ServiceAddr string `yaml:"service_addr"`
		NativeAddr  string `yaml:"native_addr"`
		HealthAddr  string `yaml:"health_addr"`
	} `yaml:"server"`
	ControlPlane struct {
		BaseURL     string `yaml:"base_url"`
		HMACKeyFile string `yaml:"hmac_key_file"`
		CAFile      string `yaml:"ca_file"`
	} `yaml:"control_plane"`
	Auth struct {
		HMACKeyFile     string `yaml:"hmac_key_file"`
		Caller          string `yaml:"caller"`
		CredentialsFile string `yaml:"credentials_file"`
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
		NativeAddr  string `yaml:"native_addr"`
		HealthAddr  string `yaml:"health_addr"`
	} `yaml:"server"`
	ControlPlane struct {
		BaseURL     string `yaml:"base_url"`
		HMACKeyFile string `yaml:"hmac_key_file"`
		CAFile      string `yaml:"ca_file"`
	} `yaml:"control_plane"`
	Auth struct {
		HMACKeyFile     string `yaml:"hmac_key_file"`
		Caller          string `yaml:"caller"`
		CredentialsFile string `yaml:"credentials_file"`
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
	cfg.Server.NativeAddr = strings.TrimSpace(raw.Server.NativeAddr)
	if cfg.Server.NativeAddr == "" {
		cfg.Server.NativeAddr = NativeServiceAddress
	}
	cfg.Server.HealthAddr = strings.TrimSpace(raw.Server.HealthAddr)
	cfg.ControlPlane.BaseURL = strings.TrimRight(strings.TrimSpace(raw.ControlPlane.BaseURL), "/")
	cfg.ControlPlane.HMACKeyFile = resolvePath(path, raw.ControlPlane.HMACKeyFile)
	cfg.ControlPlane.CAFile = resolvePath(path, raw.ControlPlane.CAFile)
	cfg.Auth.HMACKeyFile = resolvePath(path, raw.Auth.HMACKeyFile)
	cfg.Auth.Caller = strings.TrimSpace(raw.Auth.Caller)
	cfg.Auth.CredentialsFile = resolvePath(path, raw.Auth.CredentialsFile)
	cfg.Store.Path = resolvePath(path, raw.Store.Path)
	cfg.Proxy.MaxBodyBytes = raw.Proxy.MaxBodyBytes
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
	if cfg.Server.NativeAddr != NativeServiceAddress && cfg.Server.NativeAddr != PublicNativeServiceAddress {
		return fmt.Errorf("server.native_addr must be %s or %s", NativeServiceAddress, PublicNativeServiceAddress)
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
	if err := ValidateKeyFile(cfg.ControlPlane.HMACKeyFile); err != nil {
		return fmt.Errorf("control_plane.hmac_key_file: %w", err)
	}
	if cfg.Auth.CredentialsFile != "" {
		if info, err := os.Lstat(cfg.Auth.CredentialsFile); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return errors.New("auth.credentials_file must be a regular 0600 file")
		}
	} else if err := ValidateKeyFile(cfg.Auth.HMACKeyFile); err != nil {
		return fmt.Errorf("auth.hmac_key_file: %w", err)
	} else if cfg.Auth.Caller == "" {
		return errors.New("auth.caller is required when auth.credentials_file is not configured")
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
	file, err := openSecretFile(path)
	if err != nil {
		return "", fmt.Errorf("open key file: %w", err)
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
