package bootstrap

import (
	"context"
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
	startMetricsDedupeCleaner(ctx, runtime, repo)
	runtime.Go(func() {
		for {
			if err := ctx.Err(); err != nil {
				return
			}
			urls := strings.Split(cfg.Metrics.EventBusURL, ",")
			js, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv(urls, "moox-monitor-metrics"))
			if err != nil {
				log.WarnContextf(ctx, "metrics eventbus unavailable; ingestion degraded: %v", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(30 * time.Second):
				}
				continue
			}
			err = monmetrics.RunWhenReady(ctx, monmetrics.ConsumerOptions{Client: js, Storage: storage, MessageStore: repo, Authorizer: monmetrics.CheckProducerAuthorizer{Checks: runtime.Repositories.Checks}, DLQ: monmetrics.JetStreamDLQ(js, "moox-monitor", cfg.Instance.InstanceID), Config: cfg.Metrics, ServiceName: "moox-monitor", InstanceID: cfg.Instance.InstanceID})
			_ = js.Close()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.WarnContextf(ctx, "metrics ingestion stopped; retrying: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	})
}

func startMetricsDedupeCleaner(ctx context.Context, runtime *Runtime, repo *monmetrics.MetricMessageStore) {
	if repo == nil {
		return
	}
	runtime.Go(func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			if _, err := repo.PruneDedupe(ctx, time.Now().UTC()); err != nil {
				log.WarnContextf(ctx, "metrics dedupe prune failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}
