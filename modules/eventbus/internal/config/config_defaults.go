// Package config owns the explicit, validated EventBus configuration.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/packages/messagepb"
	"gopkg.in/yaml.v3"
)

func Default() *Config {
	return &Config{
		Broker: BrokerConfig{Host: "127.0.0.1", Port: 4222, ServerName: "eventbus-dev-1", StoreDir: "./data/eventbus/jetstream", StartupTimeout: 10 * time.Second, MaxPayloadBytes: 8 * 1024 * 1024, Cluster: ClusterConfig{Name: "MOOX_EVENTBUS", Host: "127.0.0.1", Port: 6222}},
		Health: HealthConfig{Addr: "127.0.0.1:11419"},
		Streams: []StreamConfig{
			{Name: "MOOX_STORAGE", Subjects: []string{"moox.storage.>"}, Retention: "limits", Storage: "file", Replicas: 1, MaxAge: 72 * time.Hour, MaxBytes: 4294967296},
			{Name: "MOOX_METRICS", Subjects: []string{"moox.metrics.>"}, Retention: "limits", Storage: "file", Replicas: 1, MaxAge: 24 * time.Hour, MaxBytes: 2147483648},
			{Name: "MOOX_CLOUDNODE_EXEC", Subjects: []string{"moox.cloudnode.exec.v1.>"}, Retention: "work_queue", Storage: "file", Replicas: 1, MaxAge: 72 * time.Hour, MaxBytes: 1073741824},
			{Name: "MOOX_DLQ", Subjects: []string{"moox.dlq.>"}, Retention: "limits", Storage: "file", Replicas: 1, MaxAge: 720 * time.Hour, MaxBytes: 536870912},
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
	if v := os.Getenv("MOOX_EVENTBUS_STREAM_MAX_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			for i := range c.Streams {
				c.Streams[i].MaxBytes = n
			}
		}
	}
}
