package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	observabilityconsumer "github.com/mooyang-code/moox/modules/monitor/internal/observability/eventconsumer"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerMetricsReporter(s *server.Server, runtime *Runtime) *report.ModuleMetrics {
	if s == nil {
		if runtime != nil {
			runtime.setMetricsReporterState(false, fmt.Errorf("monitor server is unavailable"))
		}
		return nil
	}
	moduleMetrics, err := report.NewModuleMetrics(prometheus.DefaultRegisterer, "monitor", report.HealthCheckIDsForModule("monitor"))
	if err != nil {
		if runtime != nil {
			runtime.setMetricsReporterState(false, err)
		}
		return nil
	}
	h, err := report.NewHandler(report.DefaultConfig("monitor", "moox_monitor"))
	if err != nil {
		if runtime != nil {
			runtime.setMetricsReporterState(false, err)
		}
		log.WarnContextf(trpc.BackgroundContext(), "monitor metrics reporter disabled: %v", err)
		return moduleMetrics
	}
	service := s.Service("trpc.moox.monitor.metrics.timer")
	if service == nil {
		if runtime != nil {
			runtime.setMetricsReporterState(false, fmt.Errorf("metrics timer service is not configured"))
		}
		log.Warn("monitor metrics timer service is not configured, skip register")
		return moduleMetrics
	}
	timer.RegisterHandlerService(service, func(ctx context.Context) error {
		err := h.Handle(ctx)
		if runtime != nil {
			runtime.setMetricsReporterState(err == nil, err)
		}
		return err
	})
	return moduleMetrics
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func startMetricsStorageGate(ctx context.Context, cfg *config.Config, runtime *Runtime, storage *monmetrics.StorageAdapter) {
	if cfg == nil || runtime == nil || storage == nil || !cfg.Metrics.Enabled {
		return
	}
	interval := cfg.Metrics.Storage.MetadataValidationInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	check := func() {
		checkCtx, cancel := context.WithTimeout(ctx, interval)
		defer cancel()
		if err := storage.ValidateSchema(checkCtx); err != nil {
			log.WarnContextf(ctx, "metrics storage schema check failed: %v", err)
		}
	}
	runtime.Go(func() {
		check()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check()
			}
		}
	})
}

func startObservabilityConsumer(
	ctx context.Context,
	cfg *config.Config,
	runtime *Runtime,
	storage *monmetrics.StorageAdapter,
	hostStore *hostmetrics.Store,
) {
	if cfg == nil || !cfg.Observability.Enabled || runtime == nil || runtime.Store == nil ||
		runtime.Store.Ping(ctx) != nil || runtime.Repositories == nil || hostStore == nil {
		return
	}
	if cfg.Metrics.Enabled && storage == nil {
		storage = monmetrics.NewStorageAdapterFromConfig(cfg.Metrics.Storage)
	}
	var messageStore *monmetrics.MetricMessageStore
	if runtime.MetricStores != nil {
		messageStore = runtime.MetricStores.Messages
	}
	if cfg.Metrics.Enabled && messageStore == nil {
		return
	}
	routes := observabilityconsumer.Routes{
		Metrics: metricsObservabilityRoute(storage, messageStore, monmetrics.CheckProducerAuthorizer{
			Checks: runtime.Repositories.Checks,
			ExternalProducers: map[string]struct{}{
				"moox_collector_scf": {},
			},
		}, runtime.ModuleMetrics, runtime, cfg.Metrics.Enabled),
		Host: hostObservabilityRoute(hostStore, runtime, cfg.Metrics.HostStorage.Enabled),
		Health: func(routeCtx context.Context, message *eventpb.EventMessage, health *observabilitypb.HealthCheckReport) error {
			if runtime.ObservabilityHealthRoute == nil {
				return nil
			}
			return runtime.ObservabilityHealthRoute(routeCtx, message, health)
		},
	}
	runtime.Go(func() {
		for ctx.Err() == nil {
			runtime.setObservabilityIngestState(false, errors.New("observability consumer is connecting"))
			jc := jetstream.ConfigFromEnv(cfg.Observability.EventBusURLs, "moox-monitor-observability")
			if path := strings.TrimSpace(cfg.Observability.CredentialFile); path != "" {
				if err := jc.ApplyCredentialFile(jetstream.ExpandCredentialPath(path)); err != nil {
					runtime.setObservabilityIngestState(false, fmt.Errorf("observability consumer credential: %w", err))
					log.WarnContextf(ctx, "observability consumer credential unavailable")
					if !waitObservabilityRetry(ctx) {
						return
					}
					continue
				}
			}
			js, err := jetstream.Connect(ctx, jc)
			if err != nil {
				runtime.setObservabilityIngestState(false, err)
				log.WarnContextf(ctx, "observability eventbus unavailable; ingestion degraded")
				if !waitObservabilityRetry(ctx) {
					return
				}
				continue
			}
			registry, registryErr := events.DefaultRegistry()
			if registryErr != nil {
				_ = js.Close()
				runtime.setObservabilityIngestState(false, registryErr)
				if !waitObservabilityRetry(ctx) {
					return
				}
				continue
			}
			consumerCfg := observabilityconsumer.DefaultConfig()
			consumer, bindErr := observabilityconsumer.NewConsumer(ctx, js, registry, consumerCfg, routes)
			if bindErr != nil {
				_ = js.Close()
				runtime.setObservabilityIngestState(false, bindErr)
				log.WarnContextf(ctx, "observability durable unavailable: %v", bindErr)
				if !waitObservabilityRetry(ctx) {
					return
				}
				continue
			}
			err = consumer.Run(ctx, func() { runtime.setObservabilityIngestState(true, nil) })
			_ = consumer.Close()
			_ = js.Close()
			runtime.setObservabilityIngestState(false, err)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.WarnContextf(ctx, "observability ingestion stopped; retrying")
			}
			if !waitObservabilityRetry(ctx) {
				return
			}
		}
	})
}

func metricsObservabilityRoute(
	storage *monmetrics.StorageAdapter,
	messageStore *monmetrics.MetricMessageStore,
	authorizer monmetrics.ProducerAuthorizer,
	moduleMetrics *report.ModuleMetrics,
	runtime *Runtime,
	enabled bool,
) func(context.Context, *eventpb.EventMessage, *metricspb.MetricReport) error {
	return func(ctx context.Context, message *eventpb.EventMessage, metricReport *metricspb.MetricReport) error {
		if !enabled {
			return nil
		}
		if storage == nil || messageStore == nil {
			return errors.New("metrics storage is unavailable")
		}
		if message.GetSpaceId() != monmetrics.InternalMetricSpaceID {
			return observabilityconsumer.Permanent(fmt.Errorf("unsupported metric space %q", message.GetSpaceId()))
		}
		if authorizer != nil {
			registered, err := authorizer.IsRegistered(ctx, metricReport.GetServiceName(), metricReport.GetNodeId())
			if err != nil {
				return fmt.Errorf("authorize metric producer: %w", err)
			}
			if !registered {
				return observabilityconsumer.Permanent(fmt.Errorf("unregistered metric producer %s/%s", metricReport.GetServiceName(), metricReport.GetNodeId()))
			}
		}
		observed := message.GetOccurredAt().AsTime()
		samples, err := monmetrics.ParseSnapshot(metricReport.GetSnapshot(), monmetrics.Envelope{
			ServiceName: metricReport.GetServiceName(), InstanceID: metricReport.GetInstanceId(),
			MessageID: message.GetEventId(), ProducerNodeID: metricReport.GetNodeId(),
			ProducerVersion: metricReport.GetServiceVersion(), ObservedAt: observed,
		}, monmetrics.DefaultLimits())
		if err != nil {
			monmetrics.RecordIngest(moduleMetrics, "rejected", time.Time{})
			return observabilityconsumer.Permanent(err)
		}
		duplicate, err := messageStore.IsDuplicate(ctx, message.GetEventId())
		if err != nil || duplicate {
			return err
		}
		if err := storage.WriteSamples(ctx, samples); err != nil {
			monmetrics.RecordIngest(moduleMetrics, "error", time.Time{})
			runtime.recordObservabilityWriteFailure(err)
			return err
		}
		duplicate, err = messageStore.CommitIngest(ctx, message, metricReport, samples)
		if err != nil || duplicate {
			if err != nil {
				runtime.recordObservabilityWriteFailure(err)
			}
			return err
		}
		runtime.recordObservabilityWriteSuccess()
		monmetrics.RecordIngest(moduleMetrics, "success", observed)
		return nil
	}
}

func hostObservabilityRoute(store *hostmetrics.Store, runtime *Runtime, enabled bool) func(context.Context, *eventpb.EventMessage, *hostmetricpb.HostMetric) error {
	return func(ctx context.Context, message *eventpb.EventMessage, _ *hostmetricpb.HostMetric) error {
		if !enabled {
			return nil
		}
		metric, err := hostmetrics.ValidateMessage(message)
		if err != nil {
			return observabilityconsumer.Permanent(err)
		}
		if store == nil || !store.StorageReady() {
			err := errors.New("host metrics storage is unavailable")
			runtime.recordHostWriteFailure(err)
			return err
		}
		err = store.Persist(ctx, message, metric)
		if err != nil {
			runtime.recordHostWriteFailure(err)
		} else {
			runtime.recordHostWriteSuccess()
		}
		return err
	}
}

func waitObservabilityRetry(ctx context.Context) bool {
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
