package bootstrap

import (
	"github.com/mooyang-code/moox/modules/collector/internal/dnsproxy"
	"github.com/mooyang-code/moox/modules/collector/internal/executor"
	"github.com/mooyang-code/moox/modules/collector/internal/heartbeat"
	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterTRPCServices 注册所有TRPC服务并启动服务
// 包括：心跳定时器服务、采集执行定时器服务、DNS获取定时器服务
func RegisterTRPCServices() error {
	log.Info("正在初始化TRPC服务...")

	// 创建TRPC服务器
	s := trpc.NewServer()

	// 注册心跳定时器
	log.Info("注册心跳定时器...")
	timer.RegisterScheduler("heartbeatSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.heartbeat.timer"), heartbeat.ScheduledHeartbeat)

	// 注册采集执行定时器（每分钟整点触发）
	log.Info("注册采集执行定时器...")
	timer.RegisterScheduler("collectExecSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.collectexec.timer"), executor.ScheduledExecute)

	// 注册 DNS 解析定时器
	log.Info("注册 DNS 解析定时器...")
	timer.RegisterScheduler("dnsResolveSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.dnsresolve.timer"), dnsproxy.ScheduledResolveDNS)

	// 启动TRPC服务（用go协程包裹）
	go func() {
		log.Info("启动TRPC服务器...")
		if err := s.Serve(); err != nil {
			log.Errorf("TRPC服务器启动失败: %v", err)
		}
	}()
	return nil
}
