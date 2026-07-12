package main

import (
	"github.com/mooyang-code/moox/modules/hostagent/internal/app"
	"github.com/mooyang-code/moox/modules/hostagent/internal/config"
	"github.com/mooyang-code/moox/modules/hostagent/internal/rpc"
	"os"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
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
