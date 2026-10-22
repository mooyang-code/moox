package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/health"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerHealth(ctx context.Context, s *server.Server, cfg *config.Config, runtime *Runtime, hostStore *hostmetrics.Store) error {
	if cfg == nil {
		return nil
	}
	state := health.New("monitor", cfg.Instance.InstanceID, "", "")
	state.SetReady(true)
	state.SnapshotFunc = monitorHealthSnapshot(cfg, runtime, hostStore)
	var life *lifecycle.Controller
	if runtime != nil && runtime.Store != nil {
		var err error
		if life, err = newMonitorLifecycle(runtime.Store, cfg.Database.Path); err != nil {
			return fmt.Errorf("monitor lifecycle state failed to open: %w", err)
		}
		runtime.life.Store(life)
		state.SnapshotFunc = withMonitorLifecycle(state.SnapshotFunc, life)
		runtime.Go(func() {
			life.Recover(ctx)
			life.Run(ctx, monitorLifecycleRefresh)
		})
	}
	healthAuth, err := healthz.NewAuthenticator(healthz.AuthConfig{
		Version: cfg.HealthAuth.Version, AccessKey: cfg.HealthAuth.AccessKey, SecretKey: cfg.HealthAuth.SecretKey,
		ResetAccessKey: cfg.HealthAuth.ResetAccessKey, ResetSecretKey: cfg.HealthAuth.ResetSecretKey,
		ClockSkew: time.Minute, NonceTTL: 2 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("monitor health authentication is invalid: %w", err)
	}
	handler := healthz.NewMux()
	handler.Handle("/healthz", healthAuth.Wrap(healthz.LivenessHandler(state.Snapshot)))
	handler.Handle("/readyz", healthAuth.Wrap(healthz.ReadinessHandler(state.Snapshot)))
	handler.Handle("/metrics", healthAuth.Wrap(promhttp.Handler()))
	if life != nil {
		// 前缀路由不限方法：生命周期命令与重置都是 POST。
		handler.HandlePrefix(lifecycle.StatusPath, healthAuth.Wrap(life.StatusHandler()))
		handler.HandlePrefix(lifecycle.CommandPath, healthAuth.Wrap(life.CommandHandler()))
		handler.HandlePrefix(lifecycle.CatchupPath, healthAuth.Wrap(life.CatchupHandler()))
		handler.HandlePrefix(lifecycle.ResetPrefix, healthAuth.Wrap(life.ResetHandler()))
	}
	if s == nil {
		return fmt.Errorf("monitor health service is unavailable")
	}
	if err := healthz.RegisterNoProtocolServiceMux(s.Service("trpc.moox.monitor.Health"), handler); err != nil {
		return fmt.Errorf("monitor health server failed to start: %w", err)
	}
	return nil
}

func monitorHealthSnapshot(cfg *config.Config, runtime *Runtime, hostStore *hostmetrics.Store) healthz.SnapshotFunc {
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
			"placement_enabled": cfg.Placement.Enabled,
		}
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
		evaluationReady, evaluationReason := evaluationFresh(runtime, time.Now().UTC())
		rsp.Details["evaluation_ready"] = evaluationReady
		if evaluationReason != "" {
			rsp.Details["evaluation_error"] = evaluationReason
		}
		ready = databaseReady && schedulerReady && hostStorageReady && ingestReady && reporterReady && evaluationReady
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

// evaluationStaleAfter 内没有一次完整成功的评估（检查结果写库失败、调度卡住），就判为未就绪：
// 这时 Monitor 进程活着、/healthz 正常，但所有告警都会静默失效，主机守护据此推送“组件持续未就绪”。
// 检查调度每 30 秒一轮，3 分钟相当于连续 6 轮失败；刚启动的 3 分钟内不判断。
const evaluationStaleAfter = 3 * time.Minute

func evaluationFresh(runtime *Runtime, now time.Time) (bool, string) {
	if runtime == nil || runtime.Scheduler == nil {
		return false, "scheduler is not running"
	}
	if now.Sub(runtime.StartedAt) < evaluationStaleAfter {
		return true, ""
	}
	last := runtime.Scheduler.LastSuccessfulRound()
	if last.IsZero() {
		return false, "no check evaluation has completed since startup"
	}
	if age := now.Sub(last); age > evaluationStaleAfter {
		return false, fmt.Sprintf("last complete check evaluation was %s ago", age.Truncate(time.Second))
	}
	return true, ""
}
