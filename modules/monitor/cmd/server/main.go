package main

import (
	"os"

	"github.com/mooyang-code/moox/modules/monitor/internal/bootstrap"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

func main() {
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()
	server, err := bootstrap.Initialize(ctx, s)
	if err != nil {
		log.Errorf("monitor bootstrap failed: %v", err)
		os.Exit(1)
	}
	if err := server.Serve(); err != nil {
		log.Errorf("monitor server failed: %v", err)
		os.Exit(1)
	}
}
