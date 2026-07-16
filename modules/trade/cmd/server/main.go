package main

import (
	_ "github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/masking"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"

	"github.com/mooyang-code/moox/modules/trade/internal/bootstrap"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()
	trpclog.InstallServiceName("trade")

	server, err := bootstrap.Initialize(ctx, s)
	if err != nil {
		log.Fatalf("moox-trade 初始化失败: %v", err)
	}

	log.Info("启动 moox-trade tRPC 服务器...")
	if err := server.Serve(); err != nil {
		log.Fatalf("moox-trade 服务器出错: %v", err)
	}
}
