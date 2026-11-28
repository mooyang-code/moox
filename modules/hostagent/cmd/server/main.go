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
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

var Version = "dev"

// monitorService 是拉取主机策略、接收主机快照的服务。
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
	// 主机快照与告警渠道经网关通过 MonitorMgr.SyncHostAgent 同步。打不开网关客户端就退出：常见于首装时本机网关还没监听，
	// 交给 systemd 拉起重试，否则告警渠道永远不同步（控制机不可达与 Monitor 反复重启推送发不出），主机快照也报不出去。
	gateway, syncer, err := openPolicySync(cfg)
	if err != nil {
		log.Errorf("host-agent host policy sync failed to start: %v", err)
		os.Exit(1)
	}
	defer gateway.Close()
	a, err := app.New(ctx, cfg, Version, func(ctx context.Context, report *monitorpb.HostReport) error {
		_, err := syncer.SyncOnce(ctx, report)
		return err
	})
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
	// 采样定时器启动时跑一次、之后每分钟一次：采集后随拉取主机策略上报。
	if err := app.RegisterSampleTimer(s, a); err != nil {
		log.Errorf("host-agent sample timer register failed: %v", err)
		os.Exit(1)
	}
	if err := s.Serve(); err != nil {
		log.Errorf("host-agent server failed: %v", err)
		os.Exit(1)
	}
}

// openPolicySync 打开经网关访问 Monitor 的客户端与主机策略同步器。
func openPolicySync(cfg *config.Config) (*gatewayclient.Client, *policysync.Syncer, error) {
	gateway, err := cfg.OpenGateway(func(err error) { log.Warnf("host-agent gateway directory refresh failed: %v", err) })
	if err != nil {
		return nil, nil, fmt.Errorf("open gateway client: %w", err)
	}
	syncer, err := policysync.New(cfg.HostPolicyPath, func(ctx context.Context, req *monitorpb.SyncHostAgentReq) (*monitorpb.SyncHostAgentRsp, error) {
		rsp := &monitorpb.SyncHostAgentRsp{}
		err := gateway.Invoke(ctx, monitorService, "SyncHostAgent", req, rsp)
		return rsp, err
	})
	if err != nil {
		_ = gateway.Close()
		return nil, nil, err
	}
	// 控制机或 Monitor 不可达、Monitor 反复重启这两条由非控制机推送：本机部署了 Monitor 时不推（按网关目录判断）。
	syncer.SetWatch(policysync.NewWatch(gateway.LocalHostID(), cfg.HostPolicyPath, func() bool {
		return slices.Contains(gateway.Directory().Services[monitorService], gateway.LocalHostID())
	}, log.Warnf))
	return gateway, syncer, nil
}
