package main

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/app/control"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()
	cfg, err := control.Load("./config/app.yaml")
	if err != nil {
		log.Fatalf("load strategy config: %v", err)
	}
	server, closeFn, err := control.Initialize(ctx, s, cfg)
	if err != nil {
		log.Fatalf("initialize strategy: %v", err)
	}
	defer closeFn()
	if err := server.Serve(); err != nil {
		log.Fatalf("strategy server error: %v", err)
	}
}
