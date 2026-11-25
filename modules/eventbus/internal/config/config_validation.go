// Package config owns the explicit, validated EventBus configuration.
package config

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
)

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	if strings.TrimSpace(c.Broker.Host) == "" || c.Broker.Port < 1 || c.Broker.Port > 65535 {
		return fmt.Errorf("broker host and port are invalid")
	}
	if strings.TrimSpace(c.Broker.ServerName) == "" {
		return fmt.Errorf("broker.server_name must not be empty")
	}
	if c.Broker.MaxPayloadBytes <= 0 || c.Broker.MaxPayloadBytes > 64*1024*1024 {
		return fmt.Errorf("broker.max_payload_bytes must be between 1 and 67108864")
	}
	if unsafeStoreDir(c.Broker.StoreDir) {
		return fmt.Errorf("broker.store_dir %q is unsafe", c.Broker.StoreDir)
	}
	if c.Broker.Auth.Enabled {
		if strings.TrimSpace(c.Broker.Auth.UsersFile) == "" && (strings.TrimSpace(c.Broker.Auth.Username) == "" || c.Broker.Auth.Password == "") {
			return fmt.Errorf("broker.auth requires users_file or username/password")
		}
		if c.Broker.Auth.UsersFile != "" && (c.Broker.Auth.Username != "" || c.Broker.Auth.Password != "") {
			return fmt.Errorf("broker.auth users_file cannot be combined with single username/password")
		}
		if c.Broker.Auth.UsersFile != "" && strings.TrimSpace(c.InternalClient.CredentialFile) == "" {
			return fmt.Errorf("internal_client.credential_file is required when broker.auth.users_file is enabled")
		}
	}
	if c.Broker.TLS.Enabled && (strings.TrimSpace(c.Broker.TLS.CertFile) == "" || strings.TrimSpace(c.Broker.TLS.KeyFile) == "") {
		return fmt.Errorf("broker.tls requires cert_file and key_file")
	}
	if publicHost(c.Broker.Host) || publicHost(strings.TrimSpace(c.Broker.ClientAdvertise)) {
		if !c.Broker.Auth.Enabled || !c.Broker.TLS.Enabled || strings.TrimSpace(c.Broker.TLS.CAFile) == "" {
			return fmt.Errorf("non-loopback broker requires authentication and TLS CA")
		}
	}
	return nil
}

func isLoopback(host string) bool {
	host = strings.TrimSpace(host)
	return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
}

func publicHost(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.Contains(value, ":") && !strings.Contains(value, "]") {
		if host, _, err := net.SplitHostPort(value); err == nil {
			value = host
		}
	}
	return value == "0.0.0.0" || value == "::" || !isLoopback(value)
}

func unsafeStoreDir(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" || dir == "." || dir == string(filepath.Separator) {
		return true
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return true
	}
	return abs == string(filepath.Separator) || abs == filepath.Dir(abs) && abs == "/"
}
