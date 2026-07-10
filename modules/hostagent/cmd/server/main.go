package main

import (
	"github.com/mooyang-code/moox/modules/hostagent/internal/app"
	"github.com/mooyang-code/moox/modules/hostagent/internal/config"
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
	if err := app.Register(s, a); err != nil {
		log.Errorf("host-agent register failed: %v", err)
		os.Exit(1)
	}
	_, _ = app.StartHealth(ctx, a, cfg.HealthAddr)
	go a.Run(ctx)
	if err := s.Serve(); err != nil {
		log.Errorf("host-agent server failed: %v", err)
		os.Exit(1)
	}
	_ = a.Close()
}
