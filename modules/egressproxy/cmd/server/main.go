package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mooyang-code/moox/modules/egressproxy/internal/config"
	"github.com/mooyang-code/moox/modules/egressproxy/internal/proxy"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	trpc "trpc.group/trpc-go/trpc-go"
	trpcserver "trpc.group/trpc-go/trpc-go/server"
)

var Version = "dev"
var BuildTime, GitCommit string

func main() {
	path := flag.String("config", "config/app.yaml", "Egress application configuration")
	framework := flag.String("conf", "config/trpc_go.yaml", "tRPC framework configuration")
	flag.Parse()
	trpc.ServerConfigPath = *framework
	trpclog.InstallServiceName("egress-proxy")
	cfg, err := config.Load(*path)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(trpc.BackgroundContext(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, cfg config.Config) error {
	handler, err := proxy.New(proxy.Options{Domains: cfg.Domains})
	if err != nil {
		return err
	}
	defer handler.Close()
	drain := &healthz.DrainGate{}
	server := trpc.NewServer(trpcserver.WithFilter(drain.Filter()))
	service := server.Service("trpc.moox.egress.Proxy")
	if service == nil {
		return errors.New("egress native service is required")
	}
	if err := service.Register(&egresspb.ProxyServer_ServiceDesc, handler); err != nil {
		return err
	}
	instance := os.Getenv("MOOX_INSTANCE_ID")
	if instance == "" {
		instance = "egress-proxy"
	}
	state := healthz.NewState("egress-proxy", instance, Version, GitCommit)
	health, err := healthz.WrapFromEnv(healthz.StandardMux(state.Snapshot, promhttp.Handler()))
	if err != nil {
		return err
	}
	// 组件健康只看上报（设计 3.8）：每 30 秒经 EventBus 把指标快照与就绪判断（与 /readyz 同一个）报给 Monitor。
	// 部署总会注入运行身份（MOOX_INSTANCE_ID、MOOX_NODE_ID）；没有身份时（本机测试）只记一条日志，不上报。
	if reporter, err := report.NewHandler(report.DefaultConfig("egress", "egress-proxy")); err != nil {
		log.Printf("egress-proxy metrics reporter disabled: %v", err)
	} else {
		reporter.SetReadiness(healthz.Readiness(state.Snapshot))
		reportCtx, stopReport := context.WithCancel(ctx)
		defer stopReport()
		go reporter.Run(reportCtx)
	}
	if server.Service("trpc.moox.egress.Health") == nil {
		return errors.New("egress health service is required")
	}
	if err := healthz.RegisterNoProtocolServiceMux(server.Service("trpc.moox.egress.Health"), health); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	state.SetReady(true)
	select {
	case <-ctx.Done():
		state.SetReady(false)
		drain.Begin()
		return errors.Join(server.Close(nil), <-done)
	case err := <-done:
		state.SetReady(false)
		_ = server.Close(nil)
		if err == nil {
			err = errors.New("egress server exited unexpectedly")
		}
		return err
	}
}
