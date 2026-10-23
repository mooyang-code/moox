package main

import (
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"

	"github.com/mooyang-code/moox/modules/cloudfunction/internal/bootstrap"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()
	trpclog.InstallServiceName("cloudfunction")

	server, err := bootstrap.Initialize(ctx, s)
	if err != nil {
		log.Fatalf("moox-cloudfunction 初始化失败: %v", err)
	}

	log.Info("启动 moox-cloudfunction tRPC 服务器...")
	if err := server.Serve(); err != nil {
		log.Fatalf("moox-cloudfunction 服务器出错: %v", err)
	}
}
