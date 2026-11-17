package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const monitorCheckTimerService = "trpc.moox.monitor.check_schedule.timer"

func registerMonitorScheduleTimers(s *server.Server, cfg *config.Config, runtime *Runtime, hostChecks, marketCanary func(context.Context) error) error {
	if s == nil || cfg == nil || runtime == nil || runtime.Repositories == nil {
		return fmt.Errorf("monitor schedule timers require server, config, and repositories")
	}
	checkJob, err := timerjob.New("monitor_check_schedule", 30*time.Second, checkRound(runtime, hostChecks, marketCanary))
	if err != nil {
		return err
	}
	return registerMonitorTimerJob(s, monitorCheckTimerService, checkJob)
}

// checkRound 是检查定时器的一轮，顺序固定：主机类检查 → 行情探针与业务评估（含组件的上报检查）。
// 主机类检查排在最前、只读本地库、有自己的时限：后面读 Storage 超时不会推迟或挡住主机失联与资源告警。
// 两步互不短路，错误合并返回。主机类检查成功落库时记下这一轮的时间，Monitor 的就绪检查据此判断检查评估是否停滞。
func checkRound(runtime *Runtime, hostChecks, watchdog func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		var hostErr, watchdogErr error
		if hostChecks != nil {
			hostErr = hostChecks(ctx)
		}
		if hostErr == nil && runtime != nil {
			runtime.checkRoundAt.Store(time.Now().UnixNano())
		}
		if watchdog != nil {
			watchdogErr = watchdog(ctx)
		}
		return errors.Join(hostErr, watchdogErr)
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
