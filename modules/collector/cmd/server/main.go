package main

import (
	"flag"

	"github.com/mooyang-code/moox/modules/collector/internal/bootstrap"
	_ "github.com/mooyang-code/moox/packages/clsreporter/logcls"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"

	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func run() error {
	// 业务配置的位置；要在 trpc.NewServer 解析命令行之前定义。
	configPath := flag.String("config", "config/app.yaml", "collector 业务配置")
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()
	trpclog.InstallServiceName("collector")

	server, err := bootstrap.Initialize(ctx, s, *configPath)
	if err != nil {
		return err
	}

	defer server.Close()
	log.Info("启动 moox-collector tRPC 服务器...")
	if err := server.Serve(); err != nil {
		return err
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("moox-collector 运行失败: %v", err)
	}
}
