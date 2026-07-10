package main

import (
	"os"

	"github.com/mooyang-code/moox/modules/eventbus/internal/bootstrap"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()
	server, err := bootstrap.Initialize(ctx, s)
	if err != nil {
		log.Errorf("eventbus bootstrap failed: %v", err)
		os.Exit(1)
	}
	if err := server.Serve(); err != nil {
		log.Errorf("eventbus server failed: %v", err)
		os.Exit(1)
	}
}
