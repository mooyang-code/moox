package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/health"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerHealth(ctx context.Context, s *server.Server, cfg *config.Config, runtime *Runtime) error {
	if cfg == nil {
		return nil
	}
	state := health.New("monitor", cfg.Instance.InstanceID, "", "")
	state.SetReady(true)
	state.SnapshotFunc = monitorHealthSnapshot(cfg, runtime)
	var life *lifecycle.Controller
	if runtime != nil && runtime.Store != nil {
		var err error
		if life, err = newMonitorLifecycle(cfg.Database.Path, runtime.onLifecycleChange); err != nil {
			return fmt.Errorf("monitor lifecycle state failed to open: %w", err)
		}
		runtime.life.Store(life)
		state.SnapshotFunc = withMonitorLifecycle(state.SnapshotFunc, life)
		runtime.Go(func() { life.Run(ctx, monitorLifecycleRefresh) })
	}
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

func monitorHealthSnapshot(cfg *config.Config, runtime *Runtime) healthz.SnapshotFunc {
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
			"database":           map[bool]string{true: "ok", false: "error"}[databaseReady],
			"scheduler_ok":       schedulerReady,
			"active_checks":      activeChecks,
			"deployment_enabled": cfg.Deployment.Enabled,
		}
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
		ready = databaseReady && schedulerReady && ingestReady && reporterReady && evaluationReady
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

// evaluationStaleAfter 内没有一次完整成功的评估（检查结果写库失败、调度卡住，或业务评估一直失败），就判为未就绪：
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
	return businessEvaluationFresh(runtime, now)
}

// businessEvaluationFresh 判断业务评估（数据集、网关、上报方等检查）最近是否成功过：它连续失败时这些检查都不产生结果、
// 不告警，同样要让守护发现。没有业务评估时不判断。
func businessEvaluationFresh(runtime *Runtime, now time.Time) (bool, string) {
	if !runtime.businessEvaluation {
		return true, ""
	}
	nanos := runtime.businessEvaluatedAt.Load()
	if nanos == 0 {
		return false, "no business evaluation has completed since startup"
	}
	if age := now.Sub(time.Unix(0, nanos)); age > evaluationStaleAfter {
		return false, fmt.Sprintf("last complete business evaluation was %s ago", age.Truncate(time.Second))
	}
	return true, ""
}
