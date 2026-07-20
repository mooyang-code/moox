package jetstream

import (
	"os"
	"strconv"
	"strings"
	"time"
)

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
	// ReconnectBufferBytes controls messages held by nats.go while reconnecting.
	// Zero disables the buffer, which is required for best-effort Host Agent publishing.
	ReconnectBufferBytes int
	// MaxReconnects defaults to -1 (unlimited) when zero, so a central broker restart is recoverable.
	MaxReconnects int
	MaxPayload    int
	// BatchConcurrency bounds the number of simultaneous PublishBatch calls.
	BatchConcurrency int
}

// ConfigFromEnv applies the deployment-wide EventBus connection contract to a
// module-owned URL/name pair. YAML still supplies the endpoint by default;
// credentials and TLS material stay out of checked-in module configs.
func ConfigFromEnv(urls []string, name string) Config {
	if value := firstEnv("MOOX_EVENTBUS_NATS_URL", "MOOX_EVENTBUS_URL", "NATS_URL"); value != "" {
		urls = strings.Split(value, ",")
	}
	return Config{
		URLs: urls, Name: name,
		Username:             firstEnv("MOOX_EVENTBUS_NATS_USERNAME", "MOOX_EVENTBUS_USERNAME"),
		Password:             firstEnv("MOOX_EVENTBUS_NATS_PASSWORD", "MOOX_EVENTBUS_PASSWORD"),
		Credentials:          firstEnv("MOOX_EVENTBUS_NATS_CREDENTIALS", "MOOX_EVENTBUS_CREDENTIALS"),
		TLSCAFile:            firstEnv("MOOX_EVENTBUS_NATS_TLS_CA_FILE", "MOOX_EVENTBUS_TLS_CA"),
		TLSCertFile:          firstEnv("MOOX_EVENTBUS_NATS_TLS_CERT_FILE", "MOOX_EVENTBUS_TLS_CERT"),
		TLSKeyFile:           firstEnv("MOOX_EVENTBUS_NATS_TLS_KEY_FILE", "MOOX_EVENTBUS_TLS_KEY"),
		ReconnectBufferBytes: envInt("MOOX_EVENTBUS_RECONNECT_BUFFER_BYTES", 0),
	}
}

func (cfg Config) MaxPayloadBytes() int {
	return cfg.normalized().MaxPayload
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
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
