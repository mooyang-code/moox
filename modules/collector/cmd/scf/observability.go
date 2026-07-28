package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/serverless"
	"github.com/mooyang-code/moox/modules/collector/internal/taskrunner"
	"github.com/mooyang-code/moox/packages/msgbox"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const scfObservabilityService = "trpc.moox.collector.scf_observability.timer"

var scfBootID = fmt.Sprintf("scf-%d", time.Now().UTC().UnixNano())

type sentinelFactory struct {
	once    sync.Once
	handler *serverless.WatchdogHandler
	err     error
}

func (f *sentinelFactory) Handle(ctx context.Context) error {
	if !envBool("MOOX_SCF_WATCHDOG_ENABLED") {
		return nil
	}
	if !runtimeapp.IsReady() {
		return nil
	}
	f.once.Do(func() {
		f.handler, f.err = buildSentinel(ctx)
	})
	if f.err != nil {
		return f.err
	}
	return f.handler.Handle(ctx)
}

func buildSentinel(ctx context.Context) (*serverless.WatchdogHandler, error) {
	nodeID, version := runtimeapp.GetNodeInfo()
	registry := prometheus.NewRegistry()
	pipelines, err := report.ValidatePipelineEnvironment()
	if err != nil {
		return nil, err
	}
	moduleMetrics, err := report.NewModuleMetrics(registry, "collector", pipelines.IDsForModule("collector"))
	if err != nil {
		return nil, err
	}
	taskrunner.SetModuleMetrics(moduleMetrics)

	cfg := report.DefaultConfig("collector", "moox_collector_scf")
	cfg.InstanceID = firstNonEmpty(os.Getenv("MOOX_INSTANCE_ID"), nodeID)
	cfg.NodeID = nodeID
	cfg.BootID = firstNonEmpty(os.Getenv("MOOX_BOOT_ID"), scfBootID)
	cfg.Version = version
	reporter, err := report.NewHandlerWithRegistry(cfg, registry)
	if err != nil {
		return nil, err
	}
	events, err := reporter.EventReporter(ctx)
	if err != nil {
		return nil, err
	}

	monitorURL := strings.TrimSpace(os.Getenv("MOOX_MONITOR_READY_URL"))
	gatewayURL := strings.TrimSpace(os.Getenv("MOOX_GATEWAY_READY_URL"))
	if gatewayURL == "" {
		gatewayURL = strings.TrimRight(runtimeapp.GetServiceGatewayTarget(), "/") + "/readyz"
	}
	healthAuth := serverless.HealthAuth{
		Version:   firstNonEmpty(os.Getenv("MOOX_HEALTH_HMAC_VERSION"), "moox-health-v1"),
		AccessKey: os.Getenv("MOOX_HEALTH_HMAC_KEY_ID"),
		SecretKey: os.Getenv("MOOX_HEALTH_HMAC_SECRET"),
	}
	checks := []serverless.WatchdogCheck{
		serverless.SignedHTTPReadyCheck("monitor_ready", monitorURL, nil, healthAuth),
		serverless.SignedHTTPReadyCheck("gateway_ready", gatewayURL, nil, healthAuth),
	}
	if canaryURL := strings.TrimSpace(os.Getenv("MOOX_SCF_CANARY_URL")); canaryURL != "" {
		checks = append(checks, serverless.HTTPReadyCheck("market_canary", canaryURL, nil))
	}

	var sender msgbox.Sender
	if webhook := strings.TrimSpace(os.Getenv("MOOX_MSGBOX_WECOM_WEBHOOK")); webhook != "" {
		sender, err = msgbox.NewWeComSender(webhook)
		if err != nil {
			return nil, err
		}
	}
	return serverless.NewWatchdogHandler(serverless.WatchdogOptions{
		Enabled: true, ObserverID: "scf_sentinel", SpaceID: firstNonEmpty(os.Getenv("MOOX_SPACE_ID"), report.DefaultSpace),
		Ready: runtimeapp.IsReady, Checks: checks, Events: events, Metrics: reporter, DirectSender: sender,
	})
}

func startSCFObservabilityServer(ctx context.Context) (*server.Server, error) {
	s := trpc.NewServer()
	service := s.Service(scfObservabilityService)
	if service == nil {
		return nil, fmt.Errorf("%s is not configured", scfObservabilityService)
	}
	timer.RegisterScheduler("scfObservability", &timer.DefaultScheduler{})
	factory := &sentinelFactory{}
	timer.RegisterHandlerService(service, factory.Handle)
	go func() {
		if err := s.Serve(); err != nil && ctx.Err() == nil {
			log.Errorf("SCF observability timer stopped: %v", err)
		}
	}()
	return s, nil
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
