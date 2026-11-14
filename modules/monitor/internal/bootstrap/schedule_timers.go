package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/probe"
	"github.com/mooyang-code/moox/modules/monitor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/monitor/internal/watchdog"
	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const monitorCheckTimerService = "trpc.moox.monitor.check_schedule.timer"

func registerMonitorScheduleTimers(s *server.Server, cfg *config.Config, runtime *Runtime, runner probe.Runner, hook func(context.Context, domain.Check, domain.CheckResult), hostChecks, marketCanary func(context.Context) error) error {
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
	checkJob, err := timerjob.New("monitor_check_schedule", 30*time.Second, checkRound(hostChecks, runtime.Scheduler.RunDueOnce, marketCanary))
	if err != nil {
		return err
	}
	if err := registerMonitorTimerJob(s, monitorCheckTimerService, checkJob); err != nil {
		return err
	}

	return nil
}

// checkRound 是检查定时器的一轮，顺序固定：主机类检查 → 到期的部署探测 → 行情探针与业务评估。
// 主机类检查排在最前、只读本地库、有自己的时限：某台主机宕机让部署探测等到超时，或后面读 Storage 超时，都不会推迟或挡住主机失联与资源告警。
// 各步互不短路，错误合并返回。
func checkRound(hostChecks func(context.Context) error, schedule func(context.Context) (int, error), watchdog func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		var hostErr, watchdogErr error
		if hostChecks != nil {
			hostErr = hostChecks(ctx)
		}
		count, scheduleErr := schedule(ctx)
		if count > 0 {
			log.InfoContextf(ctx, "monitor scheduled checks processed=%d", count)
		}
		if watchdog != nil {
			watchdogErr = watchdog(ctx)
		}
		return errors.Join(hostErr, scheduleErr, watchdogErr)
	}
}

func registerMonitorTimerJob(s *server.Server, name string, job *timerjob.Job) error {
	service := s.Service(name)
	if service == nil {
		return fmt.Errorf("monitor timer service %q is not configured", name)
	}
	timer.RegisterHandlerService(service, timerjob.KeepRunning(name, job.Handle))
	return nil
}
