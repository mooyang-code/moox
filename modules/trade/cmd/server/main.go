package main

import (
	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
	_ "github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	_ "trpc.group/trpc-go/trpc-filter/validation"

	"github.com/mooyang-code/moox/modules/trade/internal/bootstrap"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()

	server, err := bootstrap.Initialize(ctx, s)
	if err != nil {
		log.Fatalf("moox-trade 初始化失败: %v", err)
	}

	log.Info("启动 moox-trade tRPC 服务器...")
	if err := server.Serve(); err != nil {
		log.Fatalf("moox-trade 服务器出错: %v", err)
	}
}
