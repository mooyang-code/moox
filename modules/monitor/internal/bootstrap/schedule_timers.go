package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/probe"
	"github.com/mooyang-code/moox/modules/monitor/internal/scheduler"
	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	monitorCheckTimerService      = "trpc.moox.monitor.check_schedule.timer"
	monitorMetricRuleTimerService = "trpc.moox.monitor.metric_rule.timer"
	monitorPeerSyncTimerService   = "trpc.moox.monitor.peer_sync.timer"
)

type peerSyncer interface {
	PullOnce(context.Context) error
	MarkStale(context.Context, time.Time, time.Duration) error
}

func pullPeersOnce(ctx context.Context, syncer peerSyncer, now time.Time, timeout time.Duration) error {
	if syncer == nil {
		return nil
	}
	pullErr := syncer.PullOnce(ctx)
	staleErr := syncer.MarkStale(ctx, now, timeout)
	return errors.Join(pullErr, staleErr)
}

func registerMonitorScheduleTimers(s *server.Server, cfg *config.Config, runtime *Runtime, runner probe.Runner, hook func(context.Context, domain.Check, domain.CheckResult), evaluator *monmetrics.MetricEvaluator, rules *monmetrics.MetricRuleStore) error {
	if s == nil || cfg == nil || runtime == nil || runtime.Repositories == nil {
		return fmt.Errorf("monitor schedule timers require server, config, and repositories")
	}
	runtime.Scheduler = scheduler.New(runtime.Repositories, scheduler.Options{
		InstanceID: cfg.Instance.InstanceID, MaxConcurrency: cfg.Scheduler.MaxConcurrency, Runner: runner, OnResult: hook,
	})
	checkJob, err := timerjob.New("monitor_check_schedule", 30*time.Second, func(ctx context.Context) error {
		count, err := runtime.Scheduler.RunDueOnce(ctx)
		if count > 0 {
			log.InfoContextf(ctx, "monitor scheduled checks processed=%d", count)
		}
		return err
	})
	if err != nil {
		return err
	}
	if err := registerMonitorTimerJob(s, monitorCheckTimerService, checkJob); err != nil {
		return err
	}

	var metricHandle func(context.Context) error = func(context.Context) error { return nil }
	if evaluator != nil && rules != nil {
		runtime.MetricScheduler = monmetrics.NewRuleScheduler(monmetrics.SchedulerOptions{
			Evaluator: evaluator, Rules: rules, InstanceID: cfg.Instance.InstanceID,
			ActiveInstances: func(ctx context.Context) ([]string, error) {
				return activeMonitorInstanceIDs(ctx, cfg.Instance.InstanceID, runtime.Repositories.Peers, 3*time.Duration(cfg.Peer.TimeoutSeconds)*time.Second), nil
			},
		})
		metricHandle = runtime.MetricScheduler.EvaluateDueOnce
	}
	metricJob, err := timerjob.New("monitor_metric_rule", 30*time.Second, metricHandle)
	if err != nil {
		return err
	}
	if err := registerMonitorTimerJob(s, monitorMetricRuleTimerService, metricJob); err != nil {
		return err
	}

	var peerHandle func(context.Context) error = func(context.Context) error { return nil }
	if cfg.Peer.Enabled && len(cfg.Peer.Peers) > 0 {
		puller, err := buildPeerPuller(cfg, runtime)
		if err != nil {
			return err
		}
		peerHandle = func(ctx context.Context) error {
			return pullPeersOnce(ctx, puller, time.Now().UTC(), time.Duration(cfg.Peer.TimeoutSeconds)*time.Second)
		}
	}
	peerJob, err := timerjob.New("monitor_peer_sync", 10*time.Second, peerHandle)
	if err != nil {
		return err
	}
	return registerMonitorTimerJob(s, monitorPeerSyncTimerService, peerJob)
}

func registerMonitorTimerJob(s *server.Server, name string, job *timerjob.Job) error {
	service := s.Service(name)
	if service == nil {
		return fmt.Errorf("monitor timer service %q is not configured", name)
	}
	timer.RegisterHandlerService(service, job.Handle)
	return nil
}
