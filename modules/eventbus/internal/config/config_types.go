// Package config owns the explicit, validated EventBus configuration.
package config

import (
	"time"

	"github.com/mooyang-code/moox/packages/messagepb"
)

type Config struct {
	Broker            BrokerConfig             `yaml:"broker"`
	InternalClient    InternalClientConfig     `yaml:"internal_client"`
	Health            HealthConfig             `yaml:"health"`
	Streams           []StreamConfig           `yaml:"streams"`
	Topics            []TopicConfig            `yaml:"topics"`
	TopicFamilies     []TopicFamilyConfig      `yaml:"topic_families"`
	Consumers         []ConsumerConfig         `yaml:"consumers"`
	ConsumerTemplates []ConsumerTemplateConfig `yaml:"consumer_templates"`
	KV                []KVConfig               `yaml:"kv"`
}

type BrokerConfig struct {
	Host            string        `yaml:"host"`
	Port            int           `yaml:"port"`
	ClientAdvertise string        `yaml:"client_advertise"`
	ServerName      string        `yaml:"server_name"`
	StoreDir        string        `yaml:"store_dir"`
	StartupTimeout  time.Duration `yaml:"startup_timeout"`
	MaxPayloadBytes int           `yaml:"max_payload_bytes"`
	Cluster         ClusterConfig `yaml:"cluster"`
	Auth            AuthConfig    `yaml:"auth"`
	TLS             TLSConfig     `yaml:"tls"`
}

type ClusterConfig struct {
	Enabled bool     `yaml:"enabled"`
	Name    string   `yaml:"name"`
	Host    string   `yaml:"host"`
	Port    int      `yaml:"port"`
	Routes  []string `yaml:"routes"`
}

type AuthConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Username  string `yaml:"username"`
	Password  string `yaml:"password"`
	UsersFile string `yaml:"users_file"`
}

type InternalClientConfig struct {
	CredentialFile string `yaml:"credential_file"`
	TLSCAFile      string `yaml:"tls_ca_file"`
}

type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	CAFile   string `yaml:"ca_file"`
}

type HealthConfig struct {
	Addr string `yaml:"addr"`
}

type StreamConfig struct {
	Name        string        `yaml:"name"`
	Subjects    []string      `yaml:"subjects"`
	Retention   string        `yaml:"retention"`
	Storage     string        `yaml:"storage"`
	Replicas    int           `yaml:"replicas"`
	MaxAge      time.Duration `yaml:"max_age"`
	MaxBytes    int64         `yaml:"max_bytes"`
	MaxMsgs     int64         `yaml:"max_msgs"`
	Description string        `yaml:"description"`
}

type TopicConfig struct {
	Topic              string                `yaml:"topic"`
	Stream             string                `yaml:"stream"`
	Kind               messagepb.MessageKind `yaml:"kind"`
	PayloadContentType string                `yaml:"payload_content_type"`
	PayloadVersion     uint32                `yaml:"payload_version"`
	Enabled            bool                  `yaml:"enabled"`
}

type TopicFamilyConfig struct {
	Pattern            string                `yaml:"pattern"`
	Stream             string                `yaml:"stream"`
	Kind               messagepb.MessageKind `yaml:"kind"`
	PayloadContentType string                `yaml:"payload_content_type"`
	PayloadVersion     uint32                `yaml:"payload_version"`
	Enabled            bool                  `yaml:"enabled"`
}

type ConsumerConfig struct {
	Stream        string        `yaml:"stream"`
	Durable       string        `yaml:"durable"`
	FilterSubject string        `yaml:"filter_subject"`
	AckPolicy     string        `yaml:"ack_policy"`
	DeliverPolicy string        `yaml:"deliver_policy"`
	ReplayPolicy  string        `yaml:"replay_policy"`
	AckWait       time.Duration `yaml:"ack_wait"`
	MaxAckPending int           `yaml:"max_ack_pending"`
	MaxDeliver    int           `yaml:"max_deliver"`
}

type ConsumerTemplateConfig struct {
	Stream        string        `yaml:"stream"`
	DurablePrefix string        `yaml:"durable_prefix"`
	FilterPattern string        `yaml:"filter_pattern"`
	AckPolicy     string        `yaml:"ack_policy"`
	DeliverPolicy string        `yaml:"deliver_policy"`
	ReplayPolicy  string        `yaml:"replay_policy"`
	AckWait       time.Duration `yaml:"ack_wait"`
	MaxAckPending int           `yaml:"max_ack_pending"`
	MaxDeliver    int           `yaml:"max_deliver"`
}

type KVConfig struct {
	Bucket      string        `yaml:"bucket"`
	MaxAge      time.Duration `yaml:"max_age"`
	History     int           `yaml:"history"`
	Storage     string        `yaml:"storage"`
	Replicas    int           `yaml:"replicas"`
	Description string        `yaml:"description"`
}
