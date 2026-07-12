package main

import (
	"github.com/mooyang-code/moox/modules/collector/internal/bootstrap"
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
		log.Fatalf("moox-collector 初始化失败: %v", err)
	}

	log.Info("启动 moox-collector tRPC 服务器...")
	if err := server.Serve(); err != nil {
		log.Fatalf("moox-collector 服务器出错: %v", err)
	}
}
