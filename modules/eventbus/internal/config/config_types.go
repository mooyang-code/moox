// Package config owns the explicit, validated EventBus configuration.
package config

import (
	"time"

	"github.com/mooyang-code/moox/packages/events"
)

type Config struct {
	Broker         BrokerConfig         `yaml:"broker"`
	InternalClient InternalClientConfig `yaml:"internal_client"`
	// Streams 不从配置文件读：由 packages/events 的拓扑声明推出（见 events.Streams）。
	Streams []events.StreamSpec `yaml:"-"`
}

type BrokerConfig struct {
	Host            string        `yaml:"host"`
	Port            int           `yaml:"port"`
	ClientAdvertise string        `yaml:"client_advertise"`
	ServerName      string        `yaml:"server_name"`
	StoreDir        string        `yaml:"store_dir"`
	StartupTimeout  time.Duration `yaml:"startup_timeout"`
	MaxPayloadBytes int           `yaml:"max_payload_bytes"`
	Auth            AuthConfig    `yaml:"auth"`
	TLS             TLSConfig     `yaml:"tls"`
}

type AuthConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Username  string `yaml:"username"`
	Password  string `yaml:"password"`
	UsersFile string `yaml:"users_file"`
}

type InternalClientConfig struct {
	CredentialFile string `yaml:"credential_file"`
}

type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	CAFile   string `yaml:"ca_file"`
}
