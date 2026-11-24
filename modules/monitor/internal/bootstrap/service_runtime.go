package bootstrap

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/packages/timerjob"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	monitordeployment "github.com/mooyang-code/moox/modules/monitor/internal/deployment"
	monitordoctor "github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/healthview"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monitorrpc "github.com/mooyang-code/moox/modules/monitor/internal/rpc"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerMonitorService(s *server.Server, cfg *config.Config, runtime *Runtime, hostStore *hostmetrics.Store, doctorContext *monitordoctor.Builder, health *healthview.Builder) {
	service := s.Service("trpc.moox.monitor.MonitorMgr")
	if service == nil {
		log.Warn("MonitorMgr service is not configured, skip register")
		return
	}
	monitorpb.RegisterMonitorMgrService(service, monitorrpc.New(runtime.Repositories, monitorrpc.Options{
		InstanceID:    cfg.Instance.InstanceID,
		StartedAt:     runtime.StartedAt,
		HostStore:     hostStore,
		DoctorContext: doctorContext,
		HealthView:    health,
		Lifecycle:     runtime.life.Load,
		EvaluationStale: func() string {
			_, reason := evaluationFresh(runtime, time.Now().UTC())
			return reason
		},
	}))
}

func monitorResultHook(runtime *Runtime) func(context.Context, domain.Check, domain.CheckResult) {
	evaluator := alerting.NewEvaluator(runtime.Repositories.Alerts, alerting.Options{
		Sender: runtime.alertSender,
	})
	return func(ctx context.Context, check domain.Check, result domain.CheckResult) {
		if err := evaluator.Evaluate(ctx, check, result); err != nil {
			log.ErrorContextf(ctx, "monitor alert evaluation failed: %v", err)
		}
	}
}

// monitorSyncFunc 注册部署拓扑的同步定时器（Admin 里登记的主机与部署、各主机网关状态），启动时先同步一次。
func monitorSyncFunc(ctx context.Context, s *server.Server, cfg *config.Config, runtime *Runtime) func(context.Context) error {
	if cfg == nil || runtime == nil {
		return nil
	}
	if !cfg.Deployment.Enabled {
		registerMonitorSyncTimer(s, nil)
		return nil
	}
	syncer := monitordeployment.NewSyncer(runtime.Repositories.Topology, monitordeployment.NewClientSource(runtime.Gateway))
	handler := monitorSyncHandler(serializedMonitorSync(syncer.Sync))
	registerMonitorSyncTimer(s, handler)

	// Preserve the previous startup behavior; subsequent runs are owned by the
	// tRPC timer service and receive a fresh framework context per invocation.
	runtime.Go(func() {
		_ = handler(ctx)
	})
	return handler
}

func registerMonitorSyncTimer(s *server.Server, handler func(context.Context) error) {
	if s == nil {
		log.Warn("monitor deployment timer server is unavailable, skip register")
		return
	}
	service := s.Service("trpc.moox.monitor.deployment.timer")
	if service == nil {
		log.Warn("monitor deployment timer service is not configured, skip register")
		return
	}
	if handler == nil {
		handler = func(context.Context) error { return nil }
	}
	timer.RegisterHandlerService(service, timerjob.KeepRunning("monitor_deployment_sync", handler))
}

func serializedMonitorSync(syncFunc func(context.Context) error) func(context.Context) error {
	gate := make(chan struct{}, 1)
	return func(ctx context.Context) error {
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
			return syncFunc(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func monitorSyncHandler(syncFunc func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := syncFunc(ctx); err != nil {
			log.WarnContextf(ctx, "monitor deployment sync failed: %v", err)
			return err
		}
		return nil
	}
}
