package report

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultSpace  = "moox_system"
	DefaultBusURL = "nats://127.0.0.1:4222"
)

// Config controls one process-local metrics reporter. The timer schedule is
// owned by tRPC; this config only controls gathering and publication.
type Config struct {
	Module         string
	ServiceName    string
	InstanceID     string
	NodeID         string
	BootID         string
	Version        string
	EventBusURL    string
	CredentialFile string
	SpaceID        string
	Interval       time.Duration

	MaxUncompressedBytes int
	MaxCompressedBytes   int
	MaxMetricFamilies    int
	MaxSamples           int
	MaxLabelsPerSample   int
	MaxLabelNameBytes    int
	MaxLabelValueBytes   int
	GzipLevel            int
	IncludeRegex         string
	ExcludeRegex         string
}

func DefaultConfig(module, serviceName string) Config {
	c := Config{
		Module:               module,
		ServiceName:          serviceName,
		InstanceID:           firstEnv("MOOX_INSTANCE_ID"),
		NodeID:               firstEnv("MOOX_NODE_ID"),
		BootID:               firstEnv("MOOX_BOOT_ID"),
		Version:              firstEnv("MOOX_VERSION", "MOOX_SERVICE_VERSION"),
		EventBusURL:          firstEnv("MOOX_METRICS_EVENTBUS_URL", "MOOX_EVENTBUS_URL", "NATS_URL"),
		CredentialFile:       firstEnv("MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE"),
		SpaceID:              DefaultSpace,
		Interval:             30 * time.Second,
		MaxUncompressedBytes: 4 * 1024 * 1024,
		MaxCompressedBytes:   1 * 1024 * 1024,
		MaxMetricFamilies:    2000,
		MaxSamples:           20000,
		MaxLabelsPerSample:   20,
		MaxLabelNameBytes:    128,
		MaxLabelValueBytes:   512,
		GzipLevel:            1,
		IncludeRegex:         `^.*$`,
		ExcludeRegex:         `^(go_gc_.*debug.*)$`,
	}
	c.MaxUncompressedBytes = envInt("MOOX_METRICS_MAX_UNCOMPRESSED_BYTES", c.MaxUncompressedBytes)
	c.MaxCompressedBytes = envInt("MOOX_METRICS_MAX_COMPRESSED_BYTES", c.MaxCompressedBytes)
	c.MaxMetricFamilies = envInt("MOOX_METRICS_MAX_FAMILIES", c.MaxMetricFamilies)
	c.MaxSamples = envInt("MOOX_METRICS_MAX_SAMPLES", c.MaxSamples)
	c.MaxLabelsPerSample = envInt("MOOX_METRICS_MAX_LABELS_PER_SAMPLE", c.MaxLabelsPerSample)
	c.MaxLabelNameBytes = envInt("MOOX_METRICS_MAX_LABEL_NAME_BYTES", c.MaxLabelNameBytes)
	c.MaxLabelValueBytes = envInt("MOOX_METRICS_MAX_LABEL_VALUE_BYTES", c.MaxLabelValueBytes)
	c.GzipLevel = envInt("MOOX_METRICS_GZIP_LEVEL", c.GzipLevel)
	if value := firstEnv("MOOX_METRICS_INCLUDE_REGEX"); value != "" {
		c.IncludeRegex = value
	}
	if value := firstEnv("MOOX_METRICS_EXCLUDE_REGEX"); value != "" {
		c.ExcludeRegex = value
	}
	if c.EventBusURL == "" {
		c.EventBusURL = DefaultBusURL
	}
	if c.SpaceID == "" {
		c.SpaceID = DefaultSpace
	}
	if c.Version == "" {
		c.Version = "dev"
	}
	return c
}

func (c Config) withDefaults() Config {
	d := DefaultConfig(c.Module, c.ServiceName)
	if c.InstanceID == "" {
		c.InstanceID = d.InstanceID
	}
	if c.BootID == "" {
		c.BootID = d.BootID
	}
	if c.NodeID == "" {
		c.NodeID = d.NodeID
	}
	if c.Version == "" {
		c.Version = d.Version
	}
	if c.EventBusURL == "" {
		c.EventBusURL = d.EventBusURL
	}
	if c.CredentialFile == "" {
		c.CredentialFile = d.CredentialFile
	}
	if c.SpaceID == "" {
		c.SpaceID = d.SpaceID
	}
	if c.Interval <= 0 {
		c.Interval = d.Interval
	}
	if c.MaxUncompressedBytes <= 0 {
		c.MaxUncompressedBytes = d.MaxUncompressedBytes
	}
	if c.MaxCompressedBytes <= 0 {
		c.MaxCompressedBytes = d.MaxCompressedBytes
	}
	if c.MaxMetricFamilies <= 0 {
		c.MaxMetricFamilies = d.MaxMetricFamilies
	}
	if c.MaxSamples <= 0 {
		c.MaxSamples = d.MaxSamples
	}
	if c.MaxLabelsPerSample <= 0 {
		c.MaxLabelsPerSample = d.MaxLabelsPerSample
	}
	if c.MaxLabelNameBytes <= 0 {
		c.MaxLabelNameBytes = d.MaxLabelNameBytes
	}
	if c.MaxLabelValueBytes <= 0 {
		c.MaxLabelValueBytes = d.MaxLabelValueBytes
	}
	if c.GzipLevel == 0 {
		c.GzipLevel = d.GzipLevel
	}
	if c.IncludeRegex == "" {
		c.IncludeRegex = d.IncludeRegex
	}
	if c.ExcludeRegex == "" {
		c.ExcludeRegex = d.ExcludeRegex
	}
	return c
}

func (c Config) validateIdentity() error {
	for _, identity := range []struct{ name, value string }{
		{name: "MOOX_INSTANCE_ID", value: c.InstanceID},
		{name: "MOOX_NODE_ID", value: c.NodeID},
		{name: "MOOX_BOOT_ID", value: c.BootID},
	} {
		if strings.TrimSpace(identity.value) == "" {
			return fmt.Errorf("metrics reporter identity requires %s", identity.name)
		}
	}
	return nil
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
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
