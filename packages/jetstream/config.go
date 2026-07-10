package jetstream

import "time"

const (
	// ProtocolVersion is the current outer MooX Message protocol version.
	ProtocolVersion uint32 = 1
	// OuterContentType describes the protobuf envelope carried in a NATS message body.
	OuterContentType    = "application/vnd.moox.message+protobuf"
	defaultMaxPayload   = 8 * 1024 * 1024
	maxBatchConcurrency = 256
)

// Config controls the connection to a central NATS JetStream service.
type Config struct {
	URLs           []string
	Name           string
	Username       string
	Password       string
	Credentials    string
	TLSCAFile      string
	TLSCertFile    string
	TLSKeyFile     string
	ConnectTimeout time.Duration
	ReconnectWait  time.Duration
	// MaxReconnects defaults to -1 (unlimited) when zero, so a central broker restart is recoverable.
	MaxReconnects int
	MaxPayload    int
	// BatchConcurrency bounds the number of simultaneous PublishBatch calls.
	BatchConcurrency int
}

func (cfg Config) normalized() Config {
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}
	if cfg.ReconnectWait <= 0 {
		cfg.ReconnectWait = 500 * time.Millisecond
	}
	if cfg.MaxReconnects == 0 {
		cfg.MaxReconnects = -1
	}
	if cfg.MaxPayload <= 0 {
		cfg.MaxPayload = defaultMaxPayload
	}
	if cfg.BatchConcurrency <= 0 {
		cfg.BatchConcurrency = 64
	} else if cfg.BatchConcurrency > maxBatchConcurrency {
		cfg.BatchConcurrency = maxBatchConcurrency
	}
	return cfg
}
