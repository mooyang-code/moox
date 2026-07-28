package bootstrap

import (
	"context"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	monitordoctor "github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/probe"
	monitorrpc "github.com/mooyang-code/moox/modules/monitor/internal/rpc"
	monitorsysdeploy "github.com/mooyang-code/moox/modules/monitor/internal/sysdeploy"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerMonitorService(s *server.Server, cfg *config.Config, runtime *Runtime, hostStore *hostmetrics.Store, hostReader *hostmetrics.StorageReader, hostReady func() bool, runner probe.Runner, hook func(context.Context, domain.Check, domain.CheckResult), syncSystem func(context.Context) (int, error), metricsQuery *monmetrics.QueryService, metricRules *monmetrics.MetricRuleStore, metricEvaluator *monmetrics.MetricEvaluator, doctorContext *monitordoctor.Builder) {
	service := s.Service("trpc.moox.monitor.MonitorMgr")
	if service == nil {
		log.Warn("MonitorMgr service is not configured, skip register")
		return
	}
	monitorpb.RegisterMonitorMgrService(service, monitorrpc.New(runtime.Repositories, monitorrpc.Options{
		InstanceID: cfg.Instance.InstanceID, Runner: runner, OnResult: hook,
		SyncSystem: syncSystem, MetricsQuery: metricsQuery, MetricRules: metricRules, MetricEvaluator: metricEvaluator,
		HostStore: hostStore, HostReader: hostReader, HostStorageReady: hostReady,
		DoctorContext: doctorContext,
		ObservabilityOverview: &monitorobservability.Builder{
			Metrics: metricsQuery, Hosts: hostStore,
			Checks: runtime.Repositories.Checks, Results: runtime.Repositories.Results,
			Policy:                     doctorContext.Pipelines.RealtimeTimeSeries,
			BalanceDifferenceThreshold: cfg.Observability.BalanceDifferenceThreshold,
		},
	}))
}

func buildProbeRunner(cfg *config.Config) probe.MultiRunner {
	runner := probe.DefaultRunner()
	if cfg == nil {
		return runner
	}
	runner.HTTP.HealthSigner = &probe.HealthSigner{Version: cfg.HealthAuth.Version, AccessKey: cfg.HealthAuth.AccessKey, SecretKey: cfg.HealthAuth.SecretKey}
	return runner
}

func monitorResultHook(runtime *Runtime, notifier alerting.Notifier) func(context.Context, domain.Check, domain.CheckResult) {
	evaluator := alerting.NewEvaluator(runtime.Repositories.Alerts, alerting.Options{Notifier: notifier})
	return func(ctx context.Context, check domain.Check, result domain.CheckResult) {
		if err := evaluator.Evaluate(ctx, check, result); err != nil {
			log.ErrorContextf(ctx, "monitor alert evaluation failed: %v", err)
		}
	}
}

func monitorSyncFunc(ctx context.Context, s *server.Server, cfg *config.Config, runtime *Runtime) func(context.Context) (int, error) {
	if cfg == nil || runtime == nil {
		return nil
	}
	if !cfg.SysDeploy.Enabled {
		registerMonitorSyncTimer(s, nil)
		return nil
	}
	syncer := monitorsysdeploy.NewSyncer(runtime.Repositories.Checks, monitorsysdeploy.NewClientSource(cfg.SysDeploy.Target))
	syncFunc := serializedMonitorSync(func(syncCtx context.Context) (int, error) {
		count, err := syncer.Sync(syncCtx)
		if err != nil {
			return count, err
		}
		if err := ensureDefaultCheckAlertRules(syncCtx, runtime.Repositories); err != nil {
			return count, err
		}
		return count, nil
	})
	handler := monitorSyncHandler(syncFunc)
	registerMonitorSyncTimer(s, handler)

	// Preserve the previous startup behavior; subsequent runs are owned by the
	// tRPC timer service and receive a fresh framework context per invocation.
	runtime.Go(func() {
		_ = handler(ctx)
	})
	return syncFunc
}

func registerMonitorSyncTimer(s *server.Server, handler func(context.Context) error) {
	if s == nil {
		log.Warn("monitor sysdeploy timer server is unavailable, skip register")
		return
	}
	service := s.Service("trpc.moox.monitor.sysdeploy.timer")
	if service == nil {
		log.Warn("monitor sysdeploy timer service is not configured, skip register")
		return
	}
	if handler == nil {
		handler = func(context.Context) error { return nil }
	}
	timer.RegisterHandlerService(service, handler)
}

func serializedMonitorSync(syncFunc func(context.Context) (int, error)) func(context.Context) (int, error) {
	gate := make(chan struct{}, 1)
	return func(ctx context.Context) (int, error) {
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
			return syncFunc(ctx)
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

func monitorSyncHandler(syncFunc func(context.Context) (int, error)) func(context.Context) error {
	return func(ctx context.Context) error {
		n, err := syncFunc(ctx)
		if err != nil {
			log.WarnContextf(ctx, "monitor sysdeploy sync failed: %v", err)
			return err
		}
		if n > 0 {
			log.InfoContextf(ctx, "monitor sysdeploy sync updated %d checks", n)
		}
		return nil
	}
}
