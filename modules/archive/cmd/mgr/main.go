// Command moox-archive-mgr 是归档管理端：保存任务、运行与分区登记，生成运行并分配给 worker。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/archive/internal/bootstrap"
	"github.com/mooyang-code/moox/modules/archive/internal/config"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
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
	if len(os.Args) > 1 && os.Args[1] == "health" {
		return health(os.Args[2:])
	}
	app := flag.String("config", "config/app.yaml", "archive application config")
	framework := flag.String("conf", "config/trpc_go.yaml", "archive tRPC config")
	flag.Parse()
	cfg, err := config.Load(*app)
	if err != nil {
		return err
	}
	trpc.ServerConfigPath = *framework
	s := trpc.NewServer()
	trpclog.InstallServiceName("archive")
	ctx, cancel := context.WithCancel(trpc.BackgroundContext())
	defer cancel()
	runtime, err := bootstrap.Initialize(ctx, s, cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runtime.Close()) }()
	return s.Serve()
}
