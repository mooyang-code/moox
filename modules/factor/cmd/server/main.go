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
	_ "trpc.group/trpc-go/trpc-filter/transinfo-blocker"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"

	trpc "trpc.group/trpc-go/trpc-go"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (err error) {
	app := flag.String("config", "config/app.yaml", "control application config")
	framework := flag.String("conf", "config/trpc_go.yaml", "control tRPC config")
	flag.Parse()
	cfg, err := bootstrap.LoadControlConfig(*app)
	if err != nil {
		return err
	}
	trpc.ServerConfigPath = *framework
	s := trpc.NewServer()
	trpclog.InstallServiceName("factor")
	ctx, cancel := context.WithCancel(trpc.BackgroundContext())
	defer cancel()
	runtime, err := bootstrap.InitializeControl(ctx, s, cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runtime.Close()) }()
	return s.Serve()
}
