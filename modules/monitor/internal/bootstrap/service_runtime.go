package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorpeer "github.com/mooyang-code/moox/modules/monitor/internal/peer"
	"github.com/mooyang-code/moox/modules/monitor/internal/probe"
	monitorrpc "github.com/mooyang-code/moox/modules/monitor/internal/rpc"
	"github.com/mooyang-code/moox/modules/monitor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorsysdeploy "github.com/mooyang-code/moox/modules/monitor/internal/sysdeploy"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerMonitorService(s *server.Server, cfg *config.Config, runtime *Runtime, hostStore *hostmetrics.Store, hostReader *hostmetrics.StorageReader, hostReady func() bool, runner probe.Runner, hook func(context.Context, domain.Check, domain.CheckResult), syncSystem func(context.Context) (int, error), metricsQuery *monmetrics.QueryService, metricRules *monmetrics.MetricRuleStore, metricEvaluator *monmetrics.MetricEvaluator) {
	service := s.Service("trpc.moox.monitor.MonitorMgr")
	if service == nil {
		log.Warn("MonitorMgr service is not configured, skip register")
		return
	}
	monitorpb.RegisterMonitorMgrService(service, monitorrpc.New(runtime.Repositories, monitorrpc.Options{
		InstanceID: cfg.Instance.InstanceID, BaseURL: cfg.Instance.BaseURL, Runner: runner, OnResult: hook,
		SyncSystem: syncSystem, MetricsQuery: metricsQuery, MetricRules: metricRules, MetricEvaluator: metricEvaluator,
		HostStore: hostStore, HostReader: hostReader, HostStorageReady: hostReady,
	}))
}

func startScheduler(ctx context.Context, cfg *config.Config, runtime *Runtime, runner probe.Runner, hook func(context.Context, domain.Check, domain.CheckResult)) *scheduler.Scheduler {
	s := scheduler.New(runtime.Repositories, scheduler.Options{InstanceID: cfg.Instance.InstanceID, ReloadInterval: time.Duration(cfg.Scheduler.ReloadIntervalSeconds) * time.Second, MaxConcurrency: cfg.Scheduler.MaxConcurrency, Runner: runner, OnResult: hook})
	s.Start(ctx)
	return s
}

func buildProbeRunner(cfg *config.Config) probe.MultiRunner {
	runner := probe.DefaultRunner()
	if cfg == nil {
		return runner
	}
	runner.HTTP.HealthSigner = &probe.HealthSigner{Version: cfg.HealthAuth.Version, AccessKey: cfg.HealthAuth.AccessKey, SecretKey: cfg.HealthAuth.SecretKey}
	return runner
}

func monitorResultHook(cfg *config.Config, runtime *Runtime) func(context.Context, domain.Check, domain.CheckResult) {
	evaluator := alerting.NewEvaluator(runtime.Repositories.Alerts, alerting.Options{InstanceID: cfg.Instance.InstanceID})
	peers := runtime.Repositories.Peers
	maxPeerAge := time.Duration(0)
	if cfg.Peer.Enabled && len(cfg.Peer.Peers) > 0 {
		maxPeerAge = 3 * time.Duration(cfg.Peer.TimeoutSeconds) * time.Second
	}
	return func(ctx context.Context, check domain.Check, result domain.CheckResult) {
		activeInstanceIDs := activeMonitorInstanceIDs(ctx, cfg.Instance.InstanceID, peers, maxPeerAge)
		if err := evaluator.Evaluate(ctx, check, result, activeInstanceIDs); err != nil {
			log.ErrorContextf(ctx, "monitor alert evaluation failed: %v", err)
		}
	}
}

func activeMonitorInstanceIDs(ctx context.Context, localID string, peers *store.PeerRepository, maxPeerAge time.Duration) []string {
	seen := map[string]struct{}{}
	var ids []string
	if localID != "" {
		ids = append(ids, localID)
		seen[localID] = struct{}{}
	}
	if peers == nil || maxPeerAge <= 0 {
		return ids
	}
	instances, err := peers.ListInstances(ctx)
	if err != nil {
		return ids
	}
	cutoff := time.Now().UTC().Add(-maxPeerAge)
	for _, instance := range instances {
		if instance.Status != domain.InstanceStatusActive || instance.InstanceID == "" || instance.LastSeenAt == nil || instance.LastSeenAt.Before(cutoff) {
			continue
		}
		if _, ok := seen[instance.InstanceID]; ok {
			continue
		}
		ids = append(ids, instance.InstanceID)
		seen[instance.InstanceID] = struct{}{}
	}
	return ids
}

func startRetentionCleaner(ctx context.Context, cfg *config.Config, runtime *Runtime) {
	retention := time.Duration(cfg.Scheduler.ResultRetentionDays) * 24 * time.Hour
	if retention <= 0 {
		return
	}
	runtime.Go(func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			if err := pruneMonitorHistory(ctx, runtime, retention); err != nil {
				log.WarnContextf(ctx, "monitor retention prune failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func pruneMonitorHistory(ctx context.Context, runtime *Runtime, retention time.Duration) error {
	if runtime == nil || runtime.Store == nil || runtime.Store.Ping(ctx) != nil || retention <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-retention)
	if _, err := runtime.Repositories.Results.DeleteOlderThan(ctx, cutoff); err != nil {
		return err
	}
	if _, err := runtime.Repositories.Alerts.DeleteEventsOlderThan(ctx, cutoff); err != nil {
		return err
	}
	return nil
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
	syncFunc := serializedMonitorSync(syncer.Sync)
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

func startPeerPuller(ctx context.Context, cfg *config.Config, runtime *Runtime) error {
	if !cfg.Peer.Enabled || len(cfg.Peer.Peers) == 0 {
		return nil
	}
	remotes := make([]monitorpeer.Remote, 0, len(cfg.Peer.Peers))
	for _, item := range cfg.Peer.Peers {
		remotes = append(remotes, monitorpeer.Remote{InstanceID: item.InstanceID, GatewayURL: item.GatewayURL, NodeID: item.NodeID})
	}
	puller, err := monitorpeer.NewPuller(runtime.Repositories.Peers, monitorpeer.PullerOptions{
		Peers: remotes, Timeout: time.Duration(cfg.Peer.TimeoutSeconds) * time.Second,
		Credentials: gatewayauth.Credentials{KeyID: cfg.Peer.ServiceAuth.KeyID, Secret: cfg.Peer.ServiceAuth.SecretKey},
		CAFile:      cfg.Peer.ServiceAuth.CAFile, Alerts: runtime.Repositories.Alerts, OwnerInstanceID: cfg.Instance.InstanceID,
	})
	if err != nil {
		return fmt.Errorf("monitor peer gateway client initialization failed: %w", err)
	}
	runtime.Go(func() {
		ticker := time.NewTicker(time.Duration(cfg.Peer.PullIntervalSeconds) * time.Second)
		defer ticker.Stop()
		for {
			if err := puller.PullOnce(ctx); err != nil {
				log.WarnContextf(ctx, "monitor peer pull failed: %v", err)
			}
			_ = puller.MarkStale(ctx, time.Now(), time.Duration(cfg.Peer.TimeoutSeconds)*time.Second)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
	return nil
}
