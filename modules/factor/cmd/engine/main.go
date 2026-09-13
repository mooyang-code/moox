package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/factor/internal/bootstrap"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	trpc "trpc.group/trpc-go/trpc-go"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (err error) {
	app := flag.String("config", "config/engine-app.yaml", "engine application config")
	framework := flag.String("conf", "config/engine-trpc.yaml", "engine tRPC config")
	flag.Parse()
	cfg, err := bootstrap.LoadEngineApplicationConfig(*app)
	if err != nil {
		return err
	}
	trpc.ServerConfigPath = *framework
	s := trpc.NewServer()
	trpclog.InstallServiceName("factor-engine")
	ctx, cancel := context.WithCancel(trpc.BackgroundContext())
	defer cancel()
	runtime, err := bootstrap.InitializeEngine(ctx, s, cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runtime.Close()) }()
	return s.Serve()
}
