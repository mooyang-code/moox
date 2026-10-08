package main

import (
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/bootstrap"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/config"
	trpc "trpc.group/trpc-go/trpc-go"
	_ "trpc.group/trpc-go/trpc-log-cls"
)

// 由构建脚本经 -ldflags 注入。
var (
	Version   = "dev"
	GitCommit = ""
)

func main() {
	configPath := flag.String("config", "config/app.yaml", "主机网关配置文件")
	frameworkConfigPath := flag.String("conf", "config/trpc_go.yaml", "tRPC 框架配置文件")
	flag.Parse()
	trpc.ServerConfigPath = *frameworkConfigPath
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载主机网关配置: %v", err)
	}
	ctx, stop := signal.NotifyContext(trpc.BackgroundContext(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := bootstrap.Run(ctx, cfg, version()); err != nil {
		log.Fatalf("运行主机网关: %v", err)
	}
}

func version() string {
	if commit := strings.TrimSpace(GitCommit); commit != "" {
		return Version + "+" + commit
	}
	return Version
}
