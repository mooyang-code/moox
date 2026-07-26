package bootstrap

import (
	"context"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/reporter"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	heartbeatTimerService  = "trpc.heartbeat.timer"
	dnsResolveTimerService = "trpc.dnsresolve.timer"
)

// RegisterTRPCServices registers heartbeat and DNS timers and starts the server.
func RegisterTRPCServices() error {
	log.Info("正在初始化TRPC服务...")

	// 创建TRPC服务器
	s := trpc.NewServer()
	trpclog.InstallServiceName("collector")

	// 注册心跳定时器
	log.Info("注册心跳定时器...")
	timer.RegisterScheduler("heartbeatSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service(heartbeatTimerService), func(ctx context.Context) error {
		return reporter.ScheduledHeartbeat(ctx, "")
	})

	// 注册 DNS 解析定时器
	log.Info("注册 DNS 解析定时器...")
	timer.RegisterScheduler("dnsResolveSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service(dnsResolveTimerService), func(ctx context.Context) error {
		return httpclient.ScheduledResolveDNS(ctx, "")
	})

	// 启动TRPC服务（用go协程包裹）
	go func() {
		log.Info("启动TRPC服务器...")
		if err := s.Serve(); err != nil {
			log.Errorf("TRPC服务器启动失败: %v", err)
		}
	}()
	return nil
}
