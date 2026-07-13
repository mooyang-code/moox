package main

import (
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/bootstrap"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()
	trpclog.InstallServiceName("cloudnode")

	server, err := bootstrap.Initialize(ctx, s)
	if err != nil {
		log.Fatalf("moox-cloudnode 初始化失败: %v", err)
	}

	log.Info("启动 moox-cloudnode tRPC 服务器...")
	if err := server.Serve(); err != nil {
		log.Fatalf("moox-cloudnode 服务器出错: %v", err)
	}
}
