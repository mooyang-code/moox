// Package config owns the explicit, validated EventBus configuration.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/messagepb"
	"gopkg.in/yaml.v3"
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

func Default() *Config {
	return &Config{
		Broker: BrokerConfig{Host: "127.0.0.1", Port: 4222, ServerName: "eventbus-dev-1", StoreDir: "./data/eventbus/jetstream", StartupTimeout: 10 * time.Second, MaxPayloadBytes: 8 * 1024 * 1024, Cluster: ClusterConfig{Name: "MOOX_EVENTBUS", Host: "127.0.0.1", Port: 6222}},
		Health: HealthConfig{Addr: "127.0.0.1:11419"},
		Streams: []StreamConfig{
			{Name: "MOOX_STORAGE", Subjects: []string{"moox.storage.>"}, Retention: "limits", Storage: "file", Replicas: 1, MaxAge: 72 * time.Hour, MaxBytes: 21474836480},
			{Name: "MOOX_METRICS", Subjects: []string{"moox.metrics.>"}, Retention: "limits", Storage: "file", Replicas: 1, MaxAge: 24 * time.Hour, MaxBytes: 10737418240},
			{Name: "MOOX_CLOUDNODE_EXEC", Subjects: []string{"moox.cloudnode.exec.v1.>"}, Retention: "work_queue", Storage: "file", Replicas: 1, MaxAge: 72 * time.Hour, MaxBytes: 10737418240},
			{Name: "MOOX_DLQ", Subjects: []string{"moox.dlq.>"}, Retention: "limits", Storage: "file", Replicas: 1, MaxAge: 720 * time.Hour, MaxBytes: 2147483648},
		},
		Topics: []TopicConfig{
			{Topic: "moox.storage.time_series.rows_updated.v1", Stream: "MOOX_STORAGE", Kind: messagepb.MessageKind_MESSAGE_KIND_EVENT, PayloadContentType: "application/x-protobuf; message=trpc.moox.storage.TimeSeriesRowsUpdated", PayloadVersion: 1, Enabled: true},
			{Topic: "moox.storage.record.rows_updated.v1", Stream: "MOOX_STORAGE", Kind: messagepb.MessageKind_MESSAGE_KIND_EVENT, PayloadContentType: "application/x-protobuf; message=trpc.moox.storage.RecordRowsUpdated", PayloadVersion: 1, Enabled: true},
			{Topic: "moox.metrics.host.reported.v1", Stream: "MOOX_METRICS", Kind: messagepb.MessageKind_MESSAGE_KIND_SNAPSHOT, PayloadContentType: "application/x-protobuf; message=trpc.moox.hostagent.HostMetric", PayloadVersion: 1, Enabled: true},
			{Topic: "moox.dlq.message.rejected.v1", Stream: "MOOX_DLQ", Kind: messagepb.MessageKind_MESSAGE_KIND_EVENT, PayloadContentType: "application/x-protobuf; message=trpc.moox.message.RejectedMessage", PayloadVersion: 1, Enabled: true},
		},
		TopicFamilies: []TopicFamilyConfig{{Pattern: "moox.cloudnode.exec.v1.jobitem.s.*.pkg.*.type.*", Stream: "MOOX_CLOUDNODE_EXEC", Kind: messagepb.MessageKind_MESSAGE_KIND_COMMAND, PayloadContentType: "application/x-protobuf; message=trpc.moox.cloudnode.JobItem", PayloadVersion: 1, Enabled: true}},
			Consumers: []ConsumerConfig{
			{Stream: "MOOX_METRICS", Durable: "monitor_hostmetrics_ingest_v1", FilterSubject: "moox.metrics.host.reported.v1", AckPolicy: "explicit", DeliverPolicy: "all", ReplayPolicy: "instant", AckWait: 60 * time.Second, MaxAckPending: 256, MaxDeliver: 3},
			{Stream: "MOOX_STORAGE", Durable: "storage_view_builder_time_series_rows_updated_v1", FilterSubject: "moox.storage.time_series.rows_updated.v1", AckPolicy: "explicit", DeliverPolicy: "all", ReplayPolicy: "instant", AckWait: 120 * time.Second, MaxAckPending: 128, MaxDeliver: -1},
			{Stream: "MOOX_STORAGE", Durable: "storage_view_builder_record_rows_updated_v1", FilterSubject: "moox.storage.record.rows_updated.v1", AckPolicy: "explicit", DeliverPolicy: "all", ReplayPolicy: "instant", AckWait: 120 * time.Second, MaxAckPending: 128, MaxDeliver: -1},
				{Stream: "MOOX_STORAGE", Durable: "factor_calc", FilterSubject: "moox.storage.time_series.rows_updated.v1", AckPolicy: "explicit", DeliverPolicy: "new", ReplayPolicy: "instant", AckWait: 60 * time.Second, MaxAckPending: 1000, MaxDeliver: 5},
				{Stream: "MOOX_STORAGE", Durable: "moox_archive_kline_v1", FilterSubject: "moox.storage.time_series.rows_updated.v1", AckPolicy: "explicit", DeliverPolicy: "all", ReplayPolicy: "instant", AckWait: 5 * time.Minute, MaxAckPending: 256, MaxDeliver: -1},
		},
		ConsumerTemplates: []ConsumerTemplateConfig{{Stream: "MOOX_CLOUDNODE_EXEC", DurablePrefix: "cn_exec_", FilterPattern: "moox.cloudnode.exec.v1.jobitem.s.*.pkg.*.type.*", AckPolicy: "explicit", DeliverPolicy: "all", ReplayPolicy: "instant", AckWait: 60 * time.Second, MaxAckPending: 256, MaxDeliver: -1}},
		KV:                []KVConfig{{Bucket: "MOOX_CLOUDNODE_JOB_ACTIVE", MaxAge: 48 * time.Hour, History: 1, Storage: "file", Replicas: 1}},
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
	if c.Broker.Cluster.Enabled && (len(c.Broker.Cluster.Routes) > 0 || !isLoopback(c.Broker.Cluster.Host)) {
		return fmt.Errorf("non-loopback cluster routes are not supported in V1")
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
		if s.Replicas > 1 {
			return fmt.Errorf("stream %q replicas=%d are not supported in V1 standalone mode", s.Name, s.Replicas)
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
		if !validPayloadContentType(t.PayloadContentType) {
			return fmt.Errorf("topic %q payload_content_type must name a protobuf message", t.Topic)
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
	seenFamilies := map[string]struct{}{}
	for i := range c.TopicFamilies {
		f := &c.TopicFamilies[i]
		if !f.Enabled {
			continue
		}
		if err := validateSubject(f.Pattern, true); err != nil {
			return fmt.Errorf("topic_families[%d]: %w", i, err)
		}
		if strings.HasPrefix(f.Pattern, "moox.cloudnode.exec.v1.jobitem.") && !validCloudNodeFamily(f.Pattern) {
			return fmt.Errorf("topic family %q has invalid CloudNode route shape", f.Pattern)
		}
		if f.PayloadVersion == 0 {
			return fmt.Errorf("topic family %q payload_version must be positive", f.Pattern)
		}
		if !validPayloadContentType(f.PayloadContentType) {
			return fmt.Errorf("topic family %q payload_content_type must name a protobuf message", f.Pattern)
		}
		if _, ok := seenFamilies[f.Pattern]; ok {
			return fmt.Errorf("duplicate topic family %q", f.Pattern)
		}
		seenFamilies[f.Pattern] = struct{}{}
		matches := 0
		for _, s := range c.Streams {
			for _, subject := range s.Subjects {
				if patternsOverlap(subject, f.Pattern) {
					matches++
					if f.Stream != "" && f.Stream != s.Name {
						return fmt.Errorf("topic family %q stream %q does not cover subject", f.Pattern, f.Stream)
					}
				}
			}
		}
		if matches != 1 {
			return fmt.Errorf("topic family %q must be covered by exactly one stream, got %d", f.Pattern, matches)
		}
	}
	for i := range c.TopicFamilies {
		for j := i + 1; j < len(c.TopicFamilies); j++ {
			if c.TopicFamilies[i].Enabled && c.TopicFamilies[j].Enabled && patternsOverlap(c.TopicFamilies[i].Pattern, c.TopicFamilies[j].Pattern) {
				return fmt.Errorf("topic families %q and %q overlap", c.TopicFamilies[i].Pattern, c.TopicFamilies[j].Pattern)
			}
		}
	}
	for i := range c.Consumers {
		consumer := &c.Consumers[i]
		if err := validateConsumer(consumer, c); err != nil {
			return err
		}
	}
	for i := range c.ConsumerTemplates {
		template := &c.ConsumerTemplates[i]
		if err := validateConsumerTemplate(template, c); err != nil {
			return err
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
		if k.Replicas > 1 {
			return fmt.Errorf("kv %q replicas=%d are not supported in V1 standalone mode", k.Bucket, k.Replicas)
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

func validateConsumer(c *ConsumerConfig, cfg *Config) error {
	if strings.TrimSpace(c.Stream) == "" || strings.TrimSpace(c.Durable) == "" || strings.TrimSpace(c.FilterSubject) == "" {
		return fmt.Errorf("consumer stream, durable, and filter_subject are required")
	}
	if c.AckPolicy == "" {
		c.AckPolicy = "explicit"
	}
	if c.DeliverPolicy == "" {
		c.DeliverPolicy = "all"
	}
	if c.ReplayPolicy == "" {
		c.ReplayPolicy = "instant"
	}
	if c.AckPolicy != "explicit" || (c.DeliverPolicy != "all" && c.DeliverPolicy != "new") || c.ReplayPolicy != "instant" {
		return fmt.Errorf("consumer %q has unsupported policy", c.Durable)
	}
	if c.AckWait <= 0 || c.MaxAckPending <= 0 || c.MaxDeliver == 0 {
		return fmt.Errorf("consumer %q has invalid ack/max settings", c.Durable)
	}
	if _, ok := findStream(cfg, c.Stream); !ok {
		return fmt.Errorf("consumer %q references unknown stream %q", c.Durable, c.Stream)
	}
	if err := validateSubject(c.FilterSubject, false); err != nil {
		return fmt.Errorf("consumer %q filter: %w", c.Durable, err)
	}
	covered := false
	for _, t := range cfg.Topics {
		if t.Enabled && t.Topic == c.FilterSubject && t.Stream == c.Stream {
			covered = true
		}
	}
	if !covered {
		for _, f := range cfg.TopicFamilies {
			if f.Enabled && f.Pattern == c.FilterSubject && f.Stream == c.Stream {
				covered = true
			}
		}
	}
	if !covered {
		return fmt.Errorf("consumer %q filter %q is not registered", c.Durable, c.FilterSubject)
	}
	return nil
}

func validPayloadContentType(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "application/x-protobuf; message=") && len(strings.TrimPrefix(value, "application/x-protobuf; message=")) > 0
}

func validCloudNodeFamily(pattern string) bool {
	parts := strings.Split(pattern, ".")
	if len(parts) != 11 || parts[0] != "moox" || parts[1] != "cloudnode" || parts[2] != "exec" || parts[3] != "v1" || parts[4] != "jobitem" || parts[5] != "s" || parts[7] != "pkg" || parts[9] != "type" {
		return false
	}
	return parts[6] == "*" && parts[8] == "*" && parts[10] == "*"
}

func validateConsumerTemplate(c *ConsumerTemplateConfig, cfg *Config) error {
	if strings.TrimSpace(c.Stream) == "" || strings.TrimSpace(c.DurablePrefix) == "" || strings.TrimSpace(c.FilterPattern) == "" {
		return fmt.Errorf("consumer template stream, durable_prefix, and filter_pattern are required")
	}
	if _, ok := findStream(cfg, c.Stream); !ok {
		return fmt.Errorf("consumer template references unknown stream %q", c.Stream)
	}
	if err := validateSubject(c.FilterPattern, true); err != nil {
		return fmt.Errorf("consumer template filter: %w", err)
	}
	if c.AckPolicy == "" {
		c.AckPolicy = "explicit"
	}
	if c.DeliverPolicy == "" {
		c.DeliverPolicy = "all"
	}
	if c.ReplayPolicy == "" {
		c.ReplayPolicy = "instant"
	}
	if c.AckPolicy != "explicit" || c.DeliverPolicy != "all" || c.ReplayPolicy != "instant" || c.AckWait <= 0 || c.MaxAckPending <= 0 || c.MaxDeliver == 0 {
		return fmt.Errorf("consumer template %q has invalid policy or limits", c.DurablePrefix)
	}
	return nil
}

func findStream(c *Config, name string) (StreamConfig, bool) {
	for _, stream := range c.Streams {
		if stream.Name == name {
			return stream, true
		}
	}
	return StreamConfig{}, false
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
