package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/health"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerHealth(s *server.Server, cfg *config.Config, runtime *Runtime, metricsStorage *monmetrics.StorageAdapter, hostStore *hostmetrics.Store) error {
	if cfg == nil {
		return nil
	}
	state := health.New("monitor", cfg.Instance.InstanceID, "", "")
	state.SetReady(true)
	state.SnapshotFunc = monitorHealthSnapshot(cfg, runtime, metricsStorage, hostStore)
	healthAuth, err := healthz.NewAuthenticator(healthz.AuthConfig{
		Version: cfg.HealthAuth.Version, AccessKey: cfg.HealthAuth.AccessKey, SecretKey: cfg.HealthAuth.SecretKey,
		ClockSkew: time.Minute, NonceTTL: 2 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("monitor health authentication is invalid: %w", err)
	}
	handler := healthz.NewMux()
	handler.Handle("/healthz", healthAuth.Wrap(healthz.LivenessHandler(state.Snapshot)))
	handler.Handle("/readyz", healthAuth.Wrap(healthz.ReadinessHandler(state.Snapshot)))
	handler.Handle("/metrics", healthAuth.Wrap(promhttp.Handler()))
	if s == nil {
		return fmt.Errorf("monitor health service is unavailable")
	}
	if err := healthz.RegisterNoProtocolServiceMux(s.Service("trpc.moox.monitor.Health"), handler); err != nil {
		return fmt.Errorf("monitor health server failed to start: %w", err)
	}
	return nil
}

func monitorHealthSnapshot(cfg *config.Config, runtime *Runtime, metricsStorage *monmetrics.StorageAdapter, hostStore *hostmetrics.Store) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		var activeChecks int64
		var checksErr error
		if runtime != nil && runtime.Store != nil && runtime.Store.Ping(ctx) == nil && runtime.Repositories != nil {
			activeChecks, checksErr = runtime.Repositories.Checks.CountEnabled(ctx)
		}
		databaseReady := runtime != nil && runtime.Store != nil && runtime.Store.Ping(ctx) == nil && runtime.Repositories != nil && checksErr == nil
		schedulerReady := runtime != nil && runtime.Scheduler != nil
		ready := databaseReady && schedulerReady
		startedAt := time.Time{}
		if runtime != nil {
			startedAt = runtime.StartedAt
		}
		rsp := healthz.Base("monitor", cfg.Instance.InstanceID, "", "", startedAt, ready)
		rsp.Details = map[string]any{
			"database":          map[bool]string{true: "ok", false: "error"}[databaseReady],
			"scheduler_ok":      schedulerReady,
			"active_checks":     activeChecks,
			"sysdeploy_enabled": cfg.SysDeploy.Enabled,
		}
		metricsReady := !cfg.Metrics.Enabled
		metricsReason := "metrics ingestion disabled"
		if cfg.Metrics.Enabled {
			metricsReason = "metrics schema has not been checked"
			if metricsStorage != nil {
				status := metricsStorage.SchemaStatus()
				metricsReady = status.Valid
				if status.Error != "" {
					metricsReason = status.Error
				}
			}
		}
		rsp.Details["metrics_schema_ready"] = metricsReady
		rsp.Details["metrics_schema_reason"] = metricsReason
		hostStorageReady := !cfg.Metrics.HostStorage.Enabled || (hostStore != nil && hostStore.StorageReady())
		rsp.Details["host_storage_schema_ready"] = hostStorageReady
		reporterReady := !cfg.Metrics.Enabled || (runtime != nil && runtime.MetricsReporterReady.Load())
		rsp.Details["metrics_reporter_ready"] = reporterReady
		if runtime != nil && runtime.metricsReporterErrorMessage() != "" {
			rsp.Details["metrics_reporter_error"] = runtime.metricsReporterErrorMessage()
		}
		ingestReady := !cfg.Observability.Enabled || (runtime != nil && runtime.ObservabilityIngestReady.Load())
		writeReady, writeReason := true, ""
		if cfg.Observability.Enabled {
			writeReady, writeReason = runtime.observabilityWriteReady(time.Now().UTC())
		}
		ingestReady = ingestReady && writeReady
		rsp.Details["observability_ingest_ready"] = ingestReady
		rsp.Details["observability_storage_write_ready"] = writeReady
		if writeReason != "" {
			rsp.Details["observability_storage_write_error"] = writeReason
		}
		if runtime != nil && runtime.observabilityIngestErrorMessage() != "" {
			rsp.Details["observability_ingest_error"] = runtime.observabilityIngestErrorMessage()
		}
		ready = databaseReady && schedulerReady && metricsReady && hostStorageReady && ingestReady && reporterReady
		rsp.Ready = ready
		if ready {
			rsp.Status = "ok"
		} else {
			rsp.Status = "degraded"
		}
		rsp.Details["database_ready"] = databaseReady
		if checksErr != nil {
			rsp.Details["database_checks_error"] = checksErr.Error()
		}
		rsp.Details["scheduler_ready"] = schedulerReady
		return rsp
	}
}
