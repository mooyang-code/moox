package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"

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
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

var Version = "dev"

// monitorService 是拉取主机策略的服务。
const monitorService = "trpc.moox.monitor.MonitorMgr"

func main() {
	// 业务配置的位置；要在 trpc.NewServer 解析命令行之前定义，读配置放在 NewServer 之后。
	configPath := flag.String("config", "config/app.yaml", "host-agent 业务配置")
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()
	trpclog.InstallServiceName("hostagent")
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Errorf("host-agent config failed: %v", err)
		os.Exit(1)
	}
	a, err := app.New(ctx, cfg, Version)
	if err != nil {
		log.Errorf("host-agent startup failed: %v", err)
		os.Exit(1)
	}
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
	stopSync, err := startPolicySync(ctx, cfg)
	if err != nil {
		// 常见于首装：本机网关还没监听，取不到目录。退出让 systemd 拉起重试，否则主机策略永远不同步（证书巡检、控制机不可达推送都发不出）。
		log.Errorf("host-agent host policy sync failed to start: %v", err)
		os.Exit(1)
	}
	if err := s.Serve(); err != nil {
		log.Errorf("host-agent server failed: %v", err)
		os.Exit(1)
	}
	stopSync()
	_ = a.Close()
}

// startPolicySync 定期经网关从 Monitor 拉取主机策略。
// 打不开网关客户端就返回错误，由调用方退出进程：不在这里重试，交给 systemd 拉起。
func startPolicySync(ctx context.Context, cfg *config.Config) (func(), error) {
	gateway, err := cfg.OpenGateway(func(err error) { log.Warnf("host-agent gateway directory refresh failed: %v", err) })
	if err != nil {
		return nil, fmt.Errorf("open gateway client: %w", err)
	}
	syncer, err := policysync.New(cfg.HostPolicyPath, func(ctx context.Context) (*monitorpb.GetHostPolicyRsp, error) {
		rsp := &monitorpb.GetHostPolicyRsp{}
		err := gateway.Invoke(ctx, monitorService, "GetHostPolicy", &monitorpb.GetHostPolicyReq{}, rsp)
		return rsp, err
	})
	if err != nil {
		_ = gateway.Close()
		return nil, err
	}
	// 控制机或 Monitor 不可达、Monitor 反复重启这两条由非控制机推送：本机部署了 Monitor 时不推（按网关目录判断）。
	syncer.SetWatch(policysync.NewWatch(gateway.LocalHostID(), cfg.HostPolicyPath, func() bool {
		return slices.Contains(gateway.Directory().Services[monitorService], gateway.LocalHostID())
	}, log.Warnf))
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
	}, nil
}
