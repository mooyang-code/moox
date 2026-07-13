package main

import (
	"github.com/mooyang-code/moox/modules/factor/internal/bootstrap"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/transinfo-blocker"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"

	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()

	server, err := bootstrap.Initialize(ctx, s)
	if err != nil {
		log.Fatalf("moox-factor 初始化失败: %v", err)
	}

	log.Info("启动 moox-factor tRPC 服务器...")
	if err := server.Serve(); err != nil {
		log.Fatalf("moox-factor 服务器出错: %v", err)
	}
}
