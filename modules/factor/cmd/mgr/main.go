package main

import (
	"context"
	"errors"
	"flag"

	"github.com/mooyang-code/moox/modules/factor/internal/bootstrap"
	_ "github.com/mooyang-code/moox/packages/clsreporter/logcls"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"

	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	if err := run(); err != nil {
		// Fatalf 写完会先把日志队列发出去再退出，这最后一条错误才进得了重要日志。
		log.Fatalf("factor-mgr exited: %v", err)
	}
}

func run() (err error) {
	app := flag.String("config", "config/app.yaml", "factor application config")
	framework := flag.String("conf", "config/trpc_go.yaml", "factor tRPC config")
	flag.Parse()
	cfg, err := bootstrap.Load(*app)
	if err != nil {
		return err
	}
	trpc.ServerConfigPath = *framework
	s := trpc.NewServer()
	trpclog.InstallServiceName("factor")
	ctx, cancel := context.WithCancel(trpc.BackgroundContext())
	defer cancel()
	runtime, err := bootstrap.Initialize(ctx, s, cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runtime.Close()) }()
	return s.Serve()
}
