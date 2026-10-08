package main

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/bootstrap"
	_ "github.com/mooyang-code/moox/modules/strategy/internal/spacecontext"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

func main() {
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()
	trpclog.InstallServiceName("strategy")
	cfg, err := bootstrap.Load("./config/app.yaml")
	if err != nil {
		log.Fatalf("加载策略配置失败：%v", err)
	}
	server, closeFn, err := bootstrap.Initialize(ctx, s, cfg)
	if err != nil {
		log.Fatalf("初始化策略模块失败：%v", err)
	}
	defer closeFn()
	if err := server.Serve(); err != nil {
		log.Fatalf("策略服务运行失败：%v", err)
	}
}
