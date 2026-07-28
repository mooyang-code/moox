package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/serverless"
	"github.com/mooyang-code/moox/modules/collector/internal/taskrunner"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
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
	mu      sync.Mutex
	handler *serverless.WatchdogHandler
}

func (f *sentinelFactory) Handle(ctx context.Context) error {
	if !envBool("MOOX_SCF_WATCHDOG_ENABLED") {
		return nil
	}
	if !runtimeapp.IsReady() {
		return nil
	}
	f.mu.Lock()
	handler := f.handler
	if handler == nil {
		built, err := buildSentinel(ctx)
		if err != nil {
			f.mu.Unlock()
			return err
		}
		f.handler = built
		handler = built
	}
	f.mu.Unlock()
	return handler.Handle(ctx)
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
	if monitorURL == "" || gatewayURL == "" {
		return nil, fmt.Errorf("SCF watchdog requires MOOX_MONITOR_READY_URL and MOOX_GATEWAY_READY_URL")
	}
	healthAuth := serverless.HealthAuth{
		Version:   firstNonEmpty(os.Getenv("MOOX_HEALTH_HMAC_VERSION"), "moox-health-v1"),
		AccessKey: os.Getenv("MOOX_HEALTH_HMAC_KEY_ID"),
		SecretKey: os.Getenv("MOOX_HEALTH_HMAC_SECRET"),
	}
	if strings.TrimSpace(healthAuth.AccessKey) == "" || strings.TrimSpace(healthAuth.SecretKey) == "" {
		return nil, fmt.Errorf("SCF watchdog requires health HMAC credentials")
	}
	checks := []serverless.WatchdogCheck{
		serverless.SignedHTTPReadyCheck("monitor_ready", monitorURL, nil, healthAuth),
		serverless.SignedHTTPReadyCheck("gateway_ready", gatewayURL, nil, healthAuth),
	}
	if envDefaultTrue("MOOX_SCF_CANARY_ENABLED") {
		storageOptions := gatewayauth.NewTRPCClientOptions(
			runtimeapp.GetStorageRPCGatewayTarget(),
			strings.TrimSpace(os.Getenv("MOOX_GATEWAY_TARGET_NODE")),
			gatewayauth.CredentialsFromEnv(),
		)
		checks = append(checks, serverless.StorageMarketCanaryCheck(storagepb.NewPrimaryStoreClientProxy(storageOptions...), serverless.MarketCanaryConfig{
			SpaceID:              firstNonEmpty(os.Getenv("MOOX_SCF_CANARY_SPACE_ID"), "crypto"),
			DatasetID:            firstNonEmpty(os.Getenv("MOOX_SCF_CANARY_DATASET_ID"), "binance_spot_kline"),
			SubjectID:            firstNonEmpty(os.Getenv("MOOX_SCF_CANARY_SUBJECT_ID"), "BTC-USDT"),
			Frequency:            firstNonEmpty(os.Getenv("MOOX_SCF_CANARY_FREQUENCY"), "1m"),
			Freshness:            durationEnv("MOOX_SCF_CANARY_FRESHNESS", 3*time.Minute),
			ReturnThreshold:      envFloat("MOOX_SCF_CANARY_RETURN_THRESHOLD", 0.05),
			VolumeRatioThreshold: envFloat("MOOX_SCF_CANARY_VOLUME_RATIO_THRESHOLD", 5),
		}))
	}

	var sender msgbox.Sender
	if webhook := strings.TrimSpace(os.Getenv("MOOX_MSGBOX_WECOM_WEBHOOK")); webhook != "" {
		sender, err = msgbox.NewWeComSender(webhook)
		if err != nil {
			return nil, err
		}
	}
	return serverless.NewWatchdogHandler(serverless.WatchdogOptions{
		Enabled: true, ObserverID: "scf_sentinel", SpaceID: firstNonEmpty(os.Getenv("MOOX_SPACE_ID"), "crypto"),
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

func envDefaultTrue(name string) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return true
	}
	return envBool(name)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func envFloat(name string, fallback float64) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(name)), 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
