// Package bootstrap wires the independent moox-monitor service process.
package bootstrap

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/repository"
	monitorrpc "github.com/mooyang-code/moox/modules/monitor/internal/rpc"
	"github.com/mooyang-code/moox/modules/monitor/internal/scheduler"
	monstorage "github.com/mooyang-code/moox/modules/monitor/internal/storage"
	monitorsysdeploy "github.com/mooyang-code/moox/modules/monitor/internal/sysdeploy"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/healthz"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

var (
	monitorStartedAt = time.Now()
	defaultManager   *monstorage.Manager
	defaultScheduler *scheduler.Scheduler
)

func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	log.InfoContextf(ctx, "开始初始化 moox-monitor...")

	cfg, err := config.Load("./config/app.yaml")
	if err != nil {
		log.ErrorContextf(ctx, "加载 monitor 配置失败: %v", err)
		return nil, err
	}
	mgr, err := monstorage.OpenFromConfig(cfg.Database)
	if err != nil {
		log.ErrorContextf(ctx, "初始化 monitor 数据库失败: %v", err)
		return nil, err
	}
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		_ = mgr.Close()
		log.ErrorContextf(ctx, "初始化 monitor schema 失败: %v", err)
		return nil, err
	}
	defaultManager = mgr
	startHealthServer(ctx, cfg, mgr)
	resultHook := monitorResultHook(cfg, mgr)
	syncSystem := monitorSyncFunc(ctx, cfg, mgr)
	registerMonitorService(s, cfg, mgr, resultHook, syncSystem)
	startScheduler(ctx, cfg, mgr, resultHook)

	log.InfoContextf(ctx, "moox-monitor 初始化完成")
	return s, nil
}

func Manager() *monstorage.Manager {
	return defaultManager
}

func startHealthServer(ctx context.Context, cfg *config.Config, mgr *monstorage.Manager) {
	if cfg == nil {
		return
	}
	if _, err := healthz.Start(ctx, cfg.Health.Addr, monitorHealthSnapshot(cfg, mgr)); err != nil {
		log.ErrorContextf(ctx, "monitor health server failed to start: %v", err)
	}
}

func registerMonitorService(s *server.Server, cfg *config.Config, mgr *monstorage.Manager, hook func(context.Context, domain.Check, domain.CheckResult), syncSystem func(context.Context) (int, error)) {
	service := s.Service("trpc.moox.monitor.MonitorMgr")
	if service == nil {
		log.Warn("MonitorMgr service is not configured, skip register")
		return
	}
	monitorpb.RegisterMonitorMgrService(service, monitorrpc.New(mgr.DB(), monitorrpc.Options{
		InstanceID: cfg.Instance.InstanceID,
		OnResult:   hook,
		SyncSystem: syncSystem,
	}))
}

func startScheduler(ctx context.Context, cfg *config.Config, mgr *monstorage.Manager, hook func(context.Context, domain.Check, domain.CheckResult)) {
	defaultScheduler = scheduler.New(mgr.DB(), scheduler.Options{
		InstanceID:     cfg.Instance.InstanceID,
		ReloadInterval: time.Duration(cfg.Scheduler.ReloadIntervalSeconds) * time.Second,
		MaxConcurrency: cfg.Scheduler.MaxConcurrency,
		OnResult:       hook,
	})
	defaultScheduler.Start(ctx)
}

func monitorResultHook(cfg *config.Config, mgr *monstorage.Manager) func(context.Context, domain.Check, domain.CheckResult) {
	evaluator := alerting.NewEvaluator(mgr.DB(), alerting.Options{InstanceID: cfg.Instance.InstanceID})
	return func(ctx context.Context, check domain.Check, result domain.CheckResult) {
		if err := evaluator.Evaluate(ctx, check, result, nil); err != nil {
			log.ErrorContextf(ctx, "monitor alert evaluation failed: %v", err)
		}
	}
}

func monitorSyncFunc(ctx context.Context, cfg *config.Config, mgr *monstorage.Manager) func(context.Context) (int, error) {
	if !cfg.SysDeploy.Enabled {
		return nil
	}
	syncer := monitorsysdeploy.NewSyncer(repository.NewCheckRepository(mgr.DB()), monitorsysdeploy.NewClientSource(cfg.SysDeploy.Target))
	go func() {
		ticker := time.NewTicker(time.Duration(cfg.SysDeploy.SyncIntervalSeconds) * time.Second)
		defer ticker.Stop()
		for {
			if n, err := syncer.Sync(ctx); err != nil {
				log.WarnContextf(ctx, "monitor sysdeploy sync failed: %v", err)
			} else if n > 0 {
				log.InfoContextf(ctx, "monitor sysdeploy sync updated %d checks", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return syncer.Sync
}

func monitorHealthSnapshot(cfg *config.Config, mgr *monstorage.Manager) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		var activeChecks int64
		if mgr != nil && mgr.DB() != nil {
			_ = mgr.DB().WithContext(ctx).Raw("SELECT COUNT(1) FROM t_monitor_checks WHERE c_is_deleted = 0 AND c_enabled = 1").Scan(&activeChecks).Error
		}
		rsp := healthz.Base("monitor", cfg.Instance.InstanceID, "", "", monitorStartedAt, true)
		rsp.Details = map[string]any{
			"database":          "ok",
			"scheduler_ok":      defaultScheduler != nil,
			"active_checks":     activeChecks,
			"peer_enabled":      cfg.Peer.Enabled,
			"sysdeploy_enabled": cfg.SysDeploy.Enabled,
		}
		return rsp
	}
}
