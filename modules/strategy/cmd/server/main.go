package main

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/bootstrap"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()
	cfg, err := bootstrap.Load("./config/app.yaml")
	if err != nil {
		log.Fatalf("load strategy config: %v", err)
	}
	server, closeFn, err := bootstrap.Initialize(ctx, s, cfg)
	if err != nil {
		log.Fatalf("initialize strategy: %v", err)
	}
	defer closeFn()
	if err := server.Serve(); err != nil {
		log.Fatalf("strategy server error: %v", err)
	}
}
