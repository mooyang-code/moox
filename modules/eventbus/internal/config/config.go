// Package config owns the explicit, validated EventBus configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/messagepb"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Broker  BrokerConfig   `yaml:"broker"`
	Health  HealthConfig   `yaml:"health"`
	Streams []StreamConfig `yaml:"streams"`
	Topics  []TopicConfig  `yaml:"topics"`
	KV      []KVConfig     `yaml:"kv"`
}

type BrokerConfig struct {
	Host            string        `yaml:"host"`
	Port            int           `yaml:"port"`
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
	Enabled  bool   `yaml:"enabled"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
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

type KVConfig struct {
	Bucket      string        `yaml:"bucket"`
	MaxAge      time.Duration `yaml:"max_age"`
	History     int           `yaml:"history"`
	Storage     string        `yaml:"storage"`
	Replicas    int           `yaml:"replicas"`
	Description string        `yaml:"description"`
}

func Default() *Config {
	return &Config{
		Broker: BrokerConfig{Host: "0.0.0.0", Port: 4222, ServerName: "eventbus-dev-1", StoreDir: "./data/eventbus/jetstream", StartupTimeout: 10 * time.Second, MaxPayloadBytes: 8 * 1024 * 1024, Cluster: ClusterConfig{Name: "MOOX_EVENTBUS", Host: "0.0.0.0", Port: 6222}},
		Health: HealthConfig{Addr: ":11419"},
		Streams: []StreamConfig{
			{Name: "MOOX_STORAGE", Subjects: []string{"moox.storage.>"}, Retention: "limits", Storage: "file", Replicas: 1, MaxAge: 72 * time.Hour, MaxBytes: 21474836480},
			{Name: "MOOX_METRICS", Subjects: []string{"moox.metrics.>"}, Retention: "limits", Storage: "file", Replicas: 1, MaxAge: 24 * time.Hour, MaxBytes: 10737418240},
			{Name: "MOOX_CLOUDNODE_EXEC", Subjects: []string{"moox.cloudnode.job.requested.v1"}, Retention: "work_queue", Storage: "file", Replicas: 1, MaxAge: 72 * time.Hour, MaxBytes: 10737418240},
			{Name: "MOOX_DLQ", Subjects: []string{"moox.dlq.>"}, Retention: "limits", Storage: "file", Replicas: 1, MaxAge: 720 * time.Hour, MaxBytes: 2147483648},
		},
		Topics: []TopicConfig{
			{Topic: "moox.storage.time_series.rows_updated.v1", Stream: "MOOX_STORAGE", Kind: messagepb.MessageKind_MESSAGE_KIND_EVENT, PayloadContentType: "application/x-protobuf", PayloadVersion: 1, Enabled: true},
			{Topic: "moox.storage.record.rows_updated.v1", Stream: "MOOX_STORAGE", Kind: messagepb.MessageKind_MESSAGE_KIND_EVENT, PayloadContentType: "application/x-protobuf", PayloadVersion: 1, Enabled: true},
			{Topic: "moox.metrics.snapshot.reported.v1", Stream: "MOOX_METRICS", Kind: messagepb.MessageKind_MESSAGE_KIND_SNAPSHOT, PayloadContentType: "application/x-protobuf", PayloadVersion: 1, Enabled: true},
			{Topic: "moox.cloudnode.job.requested.v1", Stream: "MOOX_CLOUDNODE_EXEC", Kind: messagepb.MessageKind_MESSAGE_KIND_COMMAND, PayloadContentType: "application/x-protobuf", PayloadVersion: 1, Enabled: true},
			{Topic: "moox.dlq.message.rejected.v1", Stream: "MOOX_DLQ", Kind: messagepb.MessageKind_MESSAGE_KIND_EVENT, PayloadContentType: "application/x-protobuf", PayloadVersion: 1, Enabled: true},
		},
		KV: []KVConfig{{Bucket: "MOOX_CLOUDNODE_JOB_ACTIVE", MaxAge: 48 * time.Hour, History: 1, Storage: "file", Replicas: 1}},
	}
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.applyDefaults()
	cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	d := Default()
	if c.Broker.Host == "" {
		c.Broker.Host = d.Broker.Host
	}
	if c.Broker.Port == 0 {
		c.Broker.Port = d.Broker.Port
	}
	if c.Broker.ServerName == "" {
		c.Broker.ServerName = d.Broker.ServerName
	}
	if c.Broker.StoreDir == "" {
		c.Broker.StoreDir = d.Broker.StoreDir
	}
	if c.Broker.StartupTimeout == 0 {
		c.Broker.StartupTimeout = d.Broker.StartupTimeout
	}
	if c.Broker.MaxPayloadBytes == 0 {
		c.Broker.MaxPayloadBytes = d.Broker.MaxPayloadBytes
	}
	if c.Broker.Cluster.Name == "" {
		c.Broker.Cluster.Name = d.Broker.Cluster.Name
	}
	if c.Broker.Cluster.Host == "" {
		c.Broker.Cluster.Host = d.Broker.Cluster.Host
	}
	if c.Broker.Cluster.Port == 0 {
		c.Broker.Cluster.Port = d.Broker.Cluster.Port
	}
	if c.Health.Addr == "" {
		c.Health.Addr = d.Health.Addr
	}
	for i := range c.Streams {
		normalizeStream(&c.Streams[i])
	}
	for i := range c.Topics {
		if c.Topics[i].PayloadVersion == 0 {
			c.Topics[i].PayloadVersion = 1
		}
		if c.Topics[i].PayloadContentType == "" {
			c.Topics[i].PayloadContentType = "application/x-protobuf"
		}
	}
	for i := range c.KV {
		normalizeKV(&c.KV[i])
	}
}

func normalizeStream(s *StreamConfig) {
	if s.Retention == "" {
		s.Retention = "limits"
	}
	if s.Storage == "" {
		s.Storage = "file"
	}
	if s.Replicas == 0 {
		s.Replicas = 1
	}
}
func normalizeKV(k *KVConfig) {
	if k.Storage == "" {
		k.Storage = "file"
	}
	if k.History == 0 {
		k.History = 1
	}
	if k.Replicas == 0 {
		k.Replicas = 1
	}
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_EVENTBUS_STORE_DIR"); v != "" {
		c.Broker.StoreDir = v
	}
	if v := os.Getenv("MOOX_EVENTBUS_HOST"); v != "" {
		c.Broker.Host = v
	}
	if v := os.Getenv("MOOX_EVENTBUS_SERVER_NAME"); v != "" {
		c.Broker.ServerName = v
	}
	if v := os.Getenv("MOOX_EVENTBUS_HEALTH_ADDR"); v != "" {
		c.Health.Addr = v
	}
	if v := os.Getenv("MOOX_EVENTBUS_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Broker.Port = n
		}
	}
	if v := os.Getenv("MOOX_EVENTBUS_MAX_PAYLOAD_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Broker.MaxPayloadBytes = n
		}
	}
}

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
	if c.Broker.Cluster.Enabled && c.Broker.ServerName == "eventbus-dev-1" {
		return fmt.Errorf("broker.server_name must be unique when cluster is enabled")
	}
	if c.Broker.MaxPayloadBytes <= 0 || c.Broker.MaxPayloadBytes > 64*1024*1024 {
		return fmt.Errorf("broker.max_payload_bytes must be between 1 and 67108864")
	}
	if unsafeStoreDir(c.Broker.StoreDir) {
		return fmt.Errorf("broker.store_dir %q is unsafe", c.Broker.StoreDir)
	}
	if c.Broker.Auth.Enabled && (strings.TrimSpace(c.Broker.Auth.Username) == "" || c.Broker.Auth.Password == "") {
		return fmt.Errorf("broker.auth requires username and password")
	}
	if c.Broker.TLS.Enabled && (strings.TrimSpace(c.Broker.TLS.CertFile) == "" || strings.TrimSpace(c.Broker.TLS.KeyFile) == "") {
		return fmt.Errorf("broker.tls requires cert_file and key_file")
	}
	seenStreams := map[string]struct{}{}
	for i := range c.Streams {
		s := &c.Streams[i]
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("streams[%d].name is required", i)
		}
		if _, ok := seenStreams[s.Name]; ok {
			return fmt.Errorf("duplicate stream name %q", s.Name)
		}
		seenStreams[s.Name] = struct{}{}
		if s.Retention != "limits" && s.Retention != "work_queue" {
			return fmt.Errorf("stream %q retention %q is invalid", s.Name, s.Retention)
		}
		if s.Storage != "file" && s.Storage != "memory" {
			return fmt.Errorf("stream %q storage %q is invalid", s.Name, s.Storage)
		}
		if s.Replicas < 1 {
			return fmt.Errorf("stream %q replicas must be positive", s.Name)
		}
		if s.Replicas > 1 && (!c.Broker.Cluster.Enabled || s.Replicas > len(c.Broker.Cluster.Routes)+1) {
			return fmt.Errorf("stream %q replicas=%d exceed reachable cluster size", s.Name, s.Replicas)
		}
		if len(s.Subjects) == 0 {
			return fmt.Errorf("stream %q must have subjects", s.Name)
		}
		for _, subject := range s.Subjects {
			if err := validateSubject(subject, true); err != nil {
				return fmt.Errorf("stream %q subject: %w", s.Name, err)
			}
		}
	}
	for i := range c.Streams {
		for j := i + 1; j < len(c.Streams); j++ {
			for _, left := range c.Streams[i].Subjects {
				for _, right := range c.Streams[j].Subjects {
					if patternsOverlap(left, right) {
						return fmt.Errorf("stream subjects %q and %q overlap", left, right)
					}
				}
			}
		}
	}
	seenTopics := map[string]struct{}{}
	for i := range c.Topics {
		t := &c.Topics[i]
		if !t.Enabled {
			continue
		}
		if err := validateSubject(t.Topic, false); err != nil {
			return fmt.Errorf("topics[%d]: %w", i, err)
		}
		if t.PayloadVersion == 0 {
			return fmt.Errorf("topic %q payload_version must be positive", t.Topic)
		}
		if version, err := topicVersion(t.Topic); err != nil || version != t.PayloadVersion {
			return fmt.Errorf("topic %q must end in .v<major> matching payload_version=%d", t.Topic, t.PayloadVersion)
		}
		if _, ok := seenTopics[t.Topic]; ok {
			return fmt.Errorf("duplicate topic %q", t.Topic)
		}
		seenTopics[t.Topic] = struct{}{}
		matches := 0
		matchedStream := ""
		for _, s := range c.Streams {
			for _, subject := range s.Subjects {
				if subjectMatches(subject, t.Topic) {
					matches++
					matchedStream = s.Name
					if t.Stream != "" && t.Stream != s.Name {
						return fmt.Errorf("topic %q stream %q does not cover subject", t.Topic, t.Stream)
					}
					break
				}
			}
		}
		if matches != 1 {
			return fmt.Errorf("topic %q must be covered by exactly one stream, got %d", t.Topic, matches)
		}
		if t.Stream == "" {
			t.Stream = matchedStream
		}
	}
	seenKV := map[string]struct{}{}
	for i := range c.KV {
		k := &c.KV[i]
		if strings.TrimSpace(k.Bucket) == "" {
			return fmt.Errorf("kv[%d].bucket is required", i)
		}
		if _, ok := seenKV[k.Bucket]; ok {
			return fmt.Errorf("duplicate kv bucket %q", k.Bucket)
		}
		seenKV[k.Bucket] = struct{}{}
		if k.Replicas < 1 || k.History < 1 {
			return fmt.Errorf("kv %q replicas/history must be positive", k.Bucket)
		}
		if k.Replicas > 1 && (!c.Broker.Cluster.Enabled || k.Replicas > len(c.Broker.Cluster.Routes)+1) {
			return fmt.Errorf("kv %q replicas=%d exceed reachable cluster size", k.Bucket, k.Replicas)
		}
	}
	return nil
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

func validateSubject(subject string, wildcard bool) error {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return fmt.Errorf("subject is empty")
	}
	tokens := strings.Split(subject, ".")
	for i, token := range tokens {
		if token == "" || strings.ContainsAny(token, " \t\r\n") {
			return fmt.Errorf("subject %q contains invalid token", subject)
		}
		if token == ">" && (!wildcard || i != len(tokens)-1) {
			return fmt.Errorf("subject %q has invalid > wildcard", subject)
		}
		if token == "*" && !wildcard {
			return fmt.Errorf("subject %q must be concrete", subject)
		}
	}
	return nil
}

func topicVersion(topic string) (uint32, error) {
	idx := strings.LastIndex(topic, ".v")
	if idx < 0 || idx+2 >= len(topic) {
		return 0, fmt.Errorf("missing version suffix")
	}
	value, err := strconv.ParseUint(topic[idx+2:], 10, 32)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("invalid version suffix")
	}
	return uint32(value), nil
}

func subjectMatches(pattern, subject string) bool {
	p, s := strings.Split(pattern, "."), strings.Split(subject, ".")
	for i := 0; i < len(p); i++ {
		if p[i] == ">" {
			return i < len(s)
		}
		if i >= len(s) || (p[i] != "*" && p[i] != s[i]) {
			return false
		}
	}
	return len(p) == len(s)
}

func patternsOverlap(a, b string) bool {
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; ; i++ {
		if i >= len(aa) || i >= len(bb) {
			return i >= len(aa) && i >= len(bb)
		}
		if aa[i] == ">" || bb[i] == ">" {
			return true
		}
		if aa[i] != "*" && bb[i] != "*" && aa[i] != bb[i] {
			return false
		}
	}
}
