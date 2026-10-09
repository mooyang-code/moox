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
	"github.com/mooyang-code/moox/modules/egressproxy/internal/resolver"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	trpc "trpc.group/trpc-go/trpc-go"
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
	metrics, err := resolver.NewMetrics(nil)
	if err != nil {
		return err
	}
	handler, err := proxy.New(proxy.Options{Domains: cfg.Domains, Resolver: resolver.New(cfg.DNS.ResolverConfig(metrics))})
	if err != nil {
		return err
	}
	defer handler.Close()
	server := trpc.NewServer()
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
