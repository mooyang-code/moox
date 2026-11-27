package main

import (
	"flag"

	"github.com/mooyang-code/moox/modules/admin/internal/service/gatewaycontrol"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	trpc "trpc.group/trpc-go/trpc-go"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"

	"github.com/mooyang-code/moox/modules/admin/internal/bootstrap"
	_ "github.com/mooyang-code/moox/packages/clsreporter/logcls"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	// 业务配置 app.yaml 的位置；要在 trpc.NewServer 解析命令行之前定义。
	configPath := flag.String("config", "config/app.yaml", "admin 应用配置")
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer(gatewaycontrol.ServerOption())
	trpclog.InstallServiceName("admin")

	// 初始化应用（加载配置、启动后台服务、注册 trpc 服务）
	server, err := bootstrap.Initialize(ctx, s, *configPath)
	if err != nil {
		log.Fatalf("应用初始化失败: %v", err)
	}

	// 启动trpc服务器
	log.Info("启动TRPC服务器...")
	if err := server.Serve(); err != nil {
		log.Fatalf("TRPC服务器出错: %v", err)
	}
}
