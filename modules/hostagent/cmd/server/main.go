package main

import (
	"context"
	"os"

	"github.com/mooyang-code/moox/modules/hostagent/internal/app"
	"github.com/mooyang-code/moox/modules/hostagent/internal/config"
	"github.com/mooyang-code/moox/modules/hostagent/internal/policysync"
	"github.com/mooyang-code/moox/modules/hostagent/internal/rpc"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

var Version = "dev"

func main() {
	ctx := trpc.BackgroundContext()
	cfg, err := config.Load("./config/app.yaml")
	if err != nil {
		log.Errorf("host-agent config failed: %v", err)
		os.Exit(1)
	}
	a, err := app.New(ctx, cfg, Version)
	if err != nil {
		log.Errorf("host-agent startup failed: %v", err)
		os.Exit(1)
	}
	s := trpc.NewServer()
	trpclog.InstallServiceName("hostagent")
	if err := rpc.Register(s, a); err != nil {
		log.Errorf("host-agent register failed: %v", err)
		os.Exit(1)
	}
	if err := app.RegisterHealth(s.Service("trpc.moox.hostagent.Health"), a); err != nil {
		log.Errorf("host-agent health register failed: %v", err)
		os.Exit(1)
	}
	if err := app.RegisterSampleTimer(s, a); err != nil {
		log.Errorf("host-agent sample timer register failed: %v", err)
		os.Exit(1)
	}
	stopSync := startPolicySync(ctx, cfg)
	if err := s.Serve(); err != nil {
		log.Errorf("host-agent server failed: %v", err)
		os.Exit(1)
	}
	stopSync()
	_ = a.Close()
}

// startPolicySync 在部署配置了主机策略缓存时，定期经网关从 Monitor 拉取主机策略。
// 打不开网关客户端只记录错误、不影响采样：主机守护会继续使用部署时写入的种子。
func startPolicySync(ctx context.Context, cfg *config.Config) func() {
	if !cfg.PolicySyncEnabled() {
		return func() {}
	}
	gateway, err := cfg.OpenGateway(func(err error) { log.Warnf("host-agent gateway directory refresh failed: %v", err) })
	if err != nil {
		log.Errorf("host-agent host policy sync disabled: %v", err)
		return func() {}
	}
	syncer, err := policysync.New(cfg.HostPolicyPath, func(ctx context.Context) (*monitorpb.GetHostPolicyRsp, error) {
		rsp := &monitorpb.GetHostPolicyRsp{}
		err := gateway.Invoke(ctx, "trpc.moox.monitor.MonitorMgr", "GetHostPolicy", &monitorpb.GetHostPolicyReq{}, rsp)
		return rsp, err
	})
	if err != nil {
		_ = gateway.Close()
		log.Errorf("host-agent host policy sync disabled: %v", err)
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		syncer.Run(ctx, policysync.Interval, func(err error) { log.Warnf("host-agent host policy sync failed: %v", err) })
	}()
	return func() {
		cancel()
		<-done
		_ = gateway.Close()
	}
}
