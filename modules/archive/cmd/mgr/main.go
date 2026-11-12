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
	if len(os.Args) > 1 && os.Args[1] == "health" {
		if err := health(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		// Fatalf 写完会先把日志队列发出去再退出，这最后一条错误才进得了重要日志。
		log.Fatalf("archive-mgr exited: %v", err)
	}
}

func run() (err error) {
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
