package main

import (
	"flag"

	_ "github.com/mooyang-code/moox/packages/clsreporter/logcls"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/bootstrap"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	// 业务配置的位置；要在 trpc.NewServer 解析命令行之前定义。
	configPath := flag.String("config", "config/app.yaml", "callback-dispatcher 业务配置")
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()
	trpclog.InstallServiceName("callback-dispatcher")

	server, err := bootstrap.Initialize(ctx, s, *configPath)
	if err != nil {
		log.Fatalf("moox-callback-dispatcher 初始化失败: %v", err)
	}

	log.Info("启动 moox-callback-dispatcher tRPC 服务器...")
	if err := server.Serve(); err != nil {
		log.Fatalf("moox-callback-dispatcher 服务器出错: %v", err)
	}
}
