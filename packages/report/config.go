package report

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/report/logsink"
)

const (
	DefaultSpace  = events.SystemSpaceID
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
	MaxMetricFamilies    int
	MaxSamples           int
	MaxLabelsPerSample   int
	MaxLabelNameBytes    int
	MaxLabelValueBytes   int
}

// BuildVersion 是构建时写进二进制的版本号：scripts/build/build.sh 用
// -ldflags "-X github.com/mooyang-code/moox/packages/report.BuildVersion=vX.Y.Z" 注入。各组件上报的版本取自这里
// （环境变量 MOOX_VERSION 优先）：tRPC 服务的 main 包里没有 Version 变量，只注入 main.Version 时它们一律上报 dev。
var BuildVersion string

func DefaultConfig(module, serviceName string) Config {
	c := Config{
		Module:               module,
		ServiceName:          serviceName,
		InstanceID:           firstEnv("MOOX_INSTANCE_ID"),
		NodeID:               firstEnv("MOOX_NODE_ID"),
		BootID:               processBootID(),
		Version:              firstEnv("MOOX_VERSION", "MOOX_SERVICE_VERSION"),
		EventBusURL:          firstEnv("MOOX_METRICS_EVENTBUS_URL", "MOOX_EVENTBUS_URL", "NATS_URL"),
		CredentialFile:       firstEnv("MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE"),
		SpaceID:              DefaultSpace,
		Interval:             30 * time.Second,
		MaxUncompressedBytes: 16 * 1024 * 1024,
		MaxMetricFamilies:    2000,
		MaxSamples:           100000,
		MaxLabelsPerSample:   20,
		MaxLabelNameBytes:    128,
		MaxLabelValueBytes:   512,
	}
	c.MaxUncompressedBytes = envInt("MOOX_METRICS_MAX_UNCOMPRESSED_BYTES", c.MaxUncompressedBytes)
	c.MaxMetricFamilies = envInt("MOOX_METRICS_MAX_FAMILIES", c.MaxMetricFamilies)
	c.MaxSamples = envInt("MOOX_METRICS_MAX_SAMPLES", c.MaxSamples)
	c.MaxLabelsPerSample = envInt("MOOX_METRICS_MAX_LABELS_PER_SAMPLE", c.MaxLabelsPerSample)
	c.MaxLabelNameBytes = envInt("MOOX_METRICS_MAX_LABEL_NAME_BYTES", c.MaxLabelNameBytes)
	c.MaxLabelValueBytes = envInt("MOOX_METRICS_MAX_LABEL_VALUE_BYTES", c.MaxLabelValueBytes)
	if c.EventBusURL == "" {
		c.EventBusURL = DefaultBusURL
	}
	if c.SpaceID == "" {
		c.SpaceID = DefaultSpace
	}
	if c.Version == "" {
		c.Version = BuildVersion
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
	return c
}

func (c Config) validateIdentity() error {
	for _, identity := range []struct{ name, value string }{
		{name: "MOOX_INSTANCE_ID", value: c.InstanceID},
		{name: "MOOX_NODE_ID", value: c.NodeID},
	} {
		if strings.TrimSpace(identity.value) == "" {
			return fmt.Errorf("metrics reporter identity requires %s", identity.name)
		}
	}
	return nil
}

// processBootID 是本进程的启动标识：部署注入了 MOOX_BOOT_ID 就用它，否则在进程内生成一次随机值。
// 同一进程里的指标上报、日志上报与健康端口共用这一个值，Monitor 按它的变化数启动次数（“反复重启”检查）。
func processBootID() string { return logsink.ProcessBootID() }

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
