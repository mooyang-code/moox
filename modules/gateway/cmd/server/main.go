package main

import (
	"flag"
	"log"
	"os/signal"
	"syscall"

	"github.com/mooyang-code/moox/modules/gateway/internal/bootstrap"
	"github.com/mooyang-code/moox/modules/gateway/internal/config"
	trpc "trpc.group/trpc-go/trpc-go"
)

func main() {
	configPath := flag.String("config", "config/app.yaml", "gateway configuration file")
	flag.Parse()
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load gateway configuration: %v", err)
	}
	ctx, stop := signal.NotifyContext(trpc.BackgroundContext(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := bootstrap.Run(ctx, cfg); err != nil {
		log.Fatalf("run gateway: %v", err)
	}
}
