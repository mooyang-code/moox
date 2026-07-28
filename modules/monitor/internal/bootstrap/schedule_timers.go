package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/probe"
	"github.com/mooyang-code/moox/modules/monitor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/monitor/internal/watchdog"
	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	monitorCheckTimerService       = "trpc.moox.monitor.check_schedule.timer"
	monitorMetricRuleTimerService  = "trpc.moox.monitor.metric_rule.timer"
	monitorHostSilenceTimerService = "trpc.moox.monitor.host_silence.timer"
)

func registerMonitorScheduleTimers(s *server.Server, cfg *config.Config, runtime *Runtime, runner probe.Runner, hook func(context.Context, domain.Check, domain.CheckResult), evaluator *monmetrics.MetricEvaluator, rules *monmetrics.MetricRuleStore) error {
	if s == nil || cfg == nil || runtime == nil || runtime.Repositories == nil {
		return fmt.Errorf("monitor schedule timers require server, config, and repositories")
	}
	watchdogMetrics, err := watchdog.DefaultMetrics()
	if err != nil {
		return fmt.Errorf("register monitor watchdog metrics: %w", err)
	}
	runtime.Scheduler = scheduler.New(runtime.Repositories, scheduler.Options{
		InstanceID: cfg.Instance.InstanceID, MaxConcurrency: cfg.Scheduler.MaxConcurrency, Runner: runner, OnResult: hook,
		Watchdog: watchdogMetrics,
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
			Evaluator: evaluator, Rules: rules,
		})
		metricHandle = runtime.MetricScheduler.EvaluateDueOnce
	}
	metricJob, err := timerjob.New("monitor_metric_rule", 30*time.Second, metricHandle)
	if err != nil {
		return err
	}
	return registerMonitorTimerJob(s, monitorMetricRuleTimerService, metricJob)
}

func registerMonitorHostSilenceTimer(s *server.Server, scanner *hostmetrics.SilenceScanner) error {
	if s == nil || scanner == nil {
		return fmt.Errorf("monitor host silence timer requires server and scanner")
	}
	job, err := timerjob.New("monitor_host_silence", 30*time.Second, func(ctx context.Context) error {
		return scanner.Scan(ctx, time.Now().UTC())
	})
	if err != nil {
		return err
	}
	return registerMonitorTimerJob(s, monitorHostSilenceTimerService, job)
}

func registerMonitorTimerJob(s *server.Server, name string, job *timerjob.Job) error {
	service := s.Service(name)
	if service == nil {
		return fmt.Errorf("monitor timer service %q is not configured", name)
	}
	timer.RegisterHandlerService(service, job.Handle)
	return nil
}
