package marketfetch

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

// InvocationMetrics 是云函数一次调用的指标上报。每次调用用一个新的注册表，
// 热启动的函数不会把上一次调用的计数带出去。
type InvocationMetrics struct {
	Reporter MetricsReporter
}

// NewInvocationMetrics 在配置了观测用的 EventBus 时创建一次性的上报器（Monitor 消费）；
// 没有配置时不上报，也不会隐式连接本机的 EventBus。
func NewInvocationMetrics(functionName, spaceID string) (*InvocationMetrics, error) {
	if !metricsEventBusConfigured() {
		return &InvocationMetrics{}, nil
	}
	registry := prometheus.NewRegistry()
	functionName = strings.TrimSpace(functionName)
	if functionName == "" {
		return nil, fmt.Errorf("SCF function name is required for metrics identity")
	}
	cfg := report.DefaultConfig("collector", "scf-collector")
	cfg.SpaceID = strings.TrimSpace(spaceID)
	cfg.InstanceID = firstNonEmptyString(os.Getenv("MOOX_INSTANCE_ID"), functionName)
	cfg.NodeID = firstNonEmptyString(os.Getenv("MOOX_NODE_ID"), functionName)
	cfg.BootID = firstNonEmptyString(os.Getenv("MOOX_BOOT_ID"), invocationBootID(functionName))
	if url := firstNonEmptyString(os.Getenv("MOOX_METRICS_EVENTBUS_URL"), os.Getenv("MOOX_EVENTBUS_URL"), os.Getenv("NATS_URL")); url != "" {
		cfg.EventBusURL = url
	}
	reporter, err := report.NewHandlerWithRegistry(cfg, registry)
	if err != nil {
		return nil, fmt.Errorf("create SCF metrics reporter: %w", err)
	}
	return &InvocationMetrics{Reporter: reporter}, nil
}

func metricsEventBusConfigured() bool {
	return firstNonEmptyString(os.Getenv("MOOX_METRICS_EVENTBUS_URL"), os.Getenv("MOOX_EVENTBUS_URL"), os.Getenv("NATS_URL")) != ""
}

func invocationBootID(functionName string) string {
	seed := functionName + "\x00" + time.Now().UTC().Format(time.RFC3339Nano)
	digest := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("scf-%x", digest[:8])
}
