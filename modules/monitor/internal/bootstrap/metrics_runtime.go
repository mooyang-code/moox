package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerMetricsReporter(s *server.Server) {
	if s == nil {
		return
	}
	h, err := report.NewHandler(report.DefaultConfig("moox_monitor"))
	if err != nil {
		log.WarnContextf(trpc.BackgroundContext(), "monitor metrics reporter disabled: %v", err)
		return
	}
	service := s.Service("trpc.moox.monitor.metrics.timer")
	if service == nil {
		log.Warn("monitor metrics timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, h.Handle)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func startMetricsConsumer(ctx context.Context, cfg *config.Config, runtime *Runtime, storage *monmetrics.StorageAdapter) {
	if cfg == nil || !cfg.Metrics.Enabled || runtime == nil || runtime.Store == nil || runtime.Store.Ping(ctx) != nil || runtime.MetricStores == nil {
		return
	}
	if storage == nil {
		storage = monmetrics.NewStorageAdapterFromConfig(cfg.Metrics.Storage)
	}
	repo := runtime.MetricStores.Messages
	runtime.Go(func() {
		for {
			if err := ctx.Err(); err != nil {
				return
			}
			urls := strings.Split(cfg.Metrics.EventBusURL, ",")
			jc := jetstream.ConfigFromEnv(urls, "moox-monitor-metrics")
			if path := strings.TrimSpace(cfg.Metrics.EventBusCredentialFile); path != "" {
				if err := jc.ApplyCredentialFile(jetstream.ExpandCredentialPath(path)); err != nil {
					runtime.setMetricsIngestState(false, fmt.Errorf("metrics consumer credential: %w", err))
					log.WarnContextf(ctx, "metrics consumer credential unavailable")
					if !waitMetricsRetry(ctx) {
						return
					}
					continue
				}
			}
			js, err := jetstream.Connect(ctx, jc)
			if err != nil {
				runtime.setMetricsIngestState(false, err)
				log.WarnContextf(ctx, "metrics eventbus unavailable; ingestion degraded")
				if !waitMetricsRetry(ctx) {
					return
				}
				continue
			}
			runtime.setMetricsIngestState(true, nil)
			err = monmetrics.RunWhenReady(ctx, monmetrics.ConsumerOptions{Client: js, Storage: storage, MessageStore: repo, Authorizer: monmetrics.CheckProducerAuthorizer{Checks: runtime.Repositories.Checks}, DLQ: monmetrics.JetStreamDLQ(js, "moox-monitor", cfg.Instance.InstanceID), Config: cfg.Metrics, ServiceName: "moox-monitor", InstanceID: cfg.Instance.InstanceID})
			_ = js.Close()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				runtime.setMetricsIngestState(false, err)
				log.WarnContextf(ctx, "metrics ingestion stopped; retrying")
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	})
}

func waitMetricsRetry(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(30 * time.Second):
		return true
	}
}

func sanitizedMetricsError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	for _, secretTerm := range []string{"credential", "authorization", "authentication", "password", "token"} {
		if strings.Contains(message, secretTerm) {
			return "eventbus authentication unavailable"
		}
	}
	return "eventbus connection unavailable"
}
