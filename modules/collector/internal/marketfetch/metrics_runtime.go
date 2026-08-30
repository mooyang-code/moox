package marketfetch

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

// InvocationMetrics is the shared short-lived metrics composition used by
// stock_cn and crypto_market SCF handlers. Each invocation owns a fresh
// registry, so a warm SCF cannot publish stale counters from an earlier task.
type InvocationMetrics struct {
	Metrics  *Metrics
	Reporter MetricsReporter
}

// NewInvocationMetrics creates the local market metrics and, when the
// observability EventBus is configured, a one-shot reporter consumed by
// Monitor. No implicit localhost EventBus connection is attempted in SCF.
func NewInvocationMetrics(functionName, spaceID string) (*InvocationMetrics, error) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	if !metricsEventBusConfigured() {
		return &InvocationMetrics{Metrics: metrics}, nil
	}
	functionName = strings.TrimSpace(functionName)
	if functionName == "" {
		return nil, fmt.Errorf("SCF function name is required for metrics identity")
	}
	cfg := report.DefaultConfig("collector", "moox_collector_scf")
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
	return &InvocationMetrics{Metrics: metrics, Reporter: reporter}, nil
}

func metricsEventBusConfigured() bool {
	return firstNonEmptyString(os.Getenv("MOOX_METRICS_EVENTBUS_URL"), os.Getenv("MOOX_EVENTBUS_URL"), os.Getenv("NATS_URL")) != ""
}

func invocationBootID(functionName string) string {
	seed := functionName + "\x00" + time.Now().UTC().Format(time.RFC3339Nano)
	digest := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("scf-%x", digest[:8])
}

// ReportNow is a small testable wrapper for one-shot reporting. Runtime
// handlers normally call Handler.reportMetrics so the same deadline reserve is
// applied to all market actions.
func (m *InvocationMetrics) ReportNow(ctx context.Context) error {
	if m == nil || m.Reporter == nil {
		return nil
	}
	return m.Reporter.Handle(ctx)
}

// ReportInvocationMetrics is used by SCF actions that build an
// InstrumentPipeline directly rather than going through Handler. Reporting is
// best effort and leaves a small response reserve for the cloud runtime.
func ReportInvocationMetrics(parent context.Context, metrics *InvocationMetrics) {
	if metrics == nil || metrics.Reporter == nil {
		return
	}
	timeout := time.Duration(envInt("MOOX_METRICS_REPORT_TIMEOUT_MS", 750)) * time.Millisecond
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline) - metricsResponseReserve
		if remaining <= 0 {
			return
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	_ = metrics.ReportNow(ctx)
}
