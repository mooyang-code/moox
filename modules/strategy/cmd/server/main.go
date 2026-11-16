package main

import (
	"flag"

	"github.com/mooyang-code/moox/modules/strategy/internal/bootstrap"
	_ "github.com/mooyang-code/moox/packages/clsreporter/logcls"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

func main() {
	// 业务配置的位置；要在 trpc.NewServer 解析命令行之前定义。
	configPath := flag.String("config", "config/app.yaml", "strategy 业务配置")
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()
	trpclog.InstallServiceName("strategy")
	cfg, err := bootstrap.Load(*configPath)
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
