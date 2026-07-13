package main

import (
	"github.com/mooyang-code/moox/modules/hostagent/internal/app"
	"github.com/mooyang-code/moox/modules/hostagent/internal/config"
	"github.com/mooyang-code/moox/modules/hostagent/internal/rpc"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"os"
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
	go a.Run(ctx)
	if err := s.Serve(); err != nil {
		log.Errorf("host-agent server failed: %v", err)
		os.Exit(1)
	}
	_ = a.Close()
}
