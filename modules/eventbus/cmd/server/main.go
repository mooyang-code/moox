package main

import (
	"os"

	"github.com/mooyang-code/moox/modules/eventbus/internal/bootstrap"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	_ "trpc.group/trpc-go/trpc-log-cls"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
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
