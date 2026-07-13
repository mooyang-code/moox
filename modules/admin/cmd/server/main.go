package main

import (
	"context"
	"time"

	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
	_ "github.com/mooyang-code/moox/modules/admin/internal/gateway"
	"github.com/mooyang-code/moox/packages/healthz/trpcotel"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/masking"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"

	"github.com/mooyang-code/moox/modules/admin/internal/bootstrap"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	defer shutdownTracing()
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

func shutdownTracing() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := trpcotel.Shutdown(ctx); err != nil {
		log.Errorf("flush OpenTelemetry spans: %v", err)
	}
}
