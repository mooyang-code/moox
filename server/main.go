package main

import (
	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
	"github.com/mooyang-code/moox/server/internal/bootstrap"
	_ "github.com/mooyang-code/moox/server/internal/middleware"
	_ "github.com/mooyang-code/moox/server/internal/service/cloudnode/provider/tencent"

	_ "trpc.group/trpc-go/trpc-filter/validation"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	ctx := trpc.BackgroundContext()

	// 初始化应用（加载配置、启动后台服务、注册TRPC服务）
	server, err := bootstrap.Initialize(ctx)
	if err != nil {
		log.Fatalf("应用初始化失败: %v", err)
	}

	// 启动trpc服务器
	log.Info("启动TRPC服务器...")
	if err := server.Serve(); err != nil {
		log.Fatalf("TRPC服务器出错: %v", err)
	}
}
