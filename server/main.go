package main

import (
	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
	_ "github.com/mooyang-code/moox/server/internal/gateway"
	_ "github.com/mooyang-code/moox/server/internal/service/cloudnode/provider/tencent"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	_ "trpc.group/trpc-go/trpc-log-cls"

	"github.com/mooyang-code/moox/server/internal/bootstrap"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()

	// 初始化应用（加载配置、启动后台服务、注册 trpc 服务）
	server, err := bootstrap.Initialize(ctx, s)
	if err != nil {
		log.Fatalf("应用初始化失败: %v", err)
	}

	// 启动trpc服务器
	log.Info("启动TRPC服务器...")
	if err := server.Serve(); err != nil {
		log.Fatalf("TRPC服务器出错: %v", err)
	}
}
