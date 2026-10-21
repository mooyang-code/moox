package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/streamapp"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	trpcserver "trpc.group/trpc-go/trpc-go/server"
)

var Version = "dev"
var BuildTime, GitCommit string

func main() {
	path := flag.String("config", "config/app.yaml", "collector-stream application configuration")
	framework := flag.String("conf", "config/trpc_go.yaml", "tRPC framework configuration")
	flag.Parse()
	trpc.ServerConfigPath = *framework
	trpclog.InstallServiceName("collector-stream")
	cfg, err := streamapp.Load(*path)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(trpc.BackgroundContext(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

// reportedVersion 是心跳上报的版本：构建没有指定版本号（dev）时带上提交号，Collector 与运行状态页据此认出线上二进制。
func reportedVersion() string {
	if Version == "dev" && GitCommit != "" && GitCommit != "unknown" {
		return "dev-" + GitCommit
	}
	return Version
}

func run(ctx context.Context, cfg *streamapp.Config) error {
	app, err := streamapp.New(cfg, reportedVersion(), prometheus.DefaultRegisterer)
	if err != nil {
		return err
	}
	defer app.Close()
	drain := &healthz.DrainGate{}
	server := trpc.NewServer(trpcserver.WithFilter(drain.Filter()))
	executor := server.Service("trpc.moox.collector.MarketFetchExecutor")
	if executor == nil {
		return errors.New("trpc_go.yaml 缺少 trpc.moox.collector.MarketFetchExecutor 服务")
	}
	if err := executor.Register(&collectorpb.MarketFetchExecutorServer_ServiceDesc, app.Executor); err != nil {
		return err
	}
	state := healthz.NewState("collector-stream", cfg.InstanceID, Version, GitCommit)
	health, err := healthz.WrapFromEnv(healthz.StandardMux(state.Snapshot, promhttp.Handler()))
	if err != nil {
		return err
	}
	healthService := server.Service("trpc.moox.collector.stream.Health")
	if healthService == nil {
		return errors.New("trpc_go.yaml 缺少 trpc.moox.collector.stream.Health 服务")
	}
	if err := healthz.RegisterNoProtocolServiceMux(healthService, health); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	app.Start(runCtx)
	// 运行指标经消息总线上报 Monitor（服务目录 transport: reporter）：没有上报时 Monitor 判定该组件“没有收到运行指标”。
	reporter, err := report.NewHandler(report.DefaultConfig("collector", "collector-stream"))
	if err != nil {
		return fmt.Errorf("create collector-stream metrics reporter: %w", err)
	}
	go reportMetrics(runCtx, reporter)
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	// /readyz：已应用 Collector 要求的计划版本，且全部市场的连接都已订阅完成。
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			state.SetReady(false)
			drain.Begin()
			cancel()
			return errors.Join(server.Close(nil), <-done)
		case err := <-done:
			state.SetReady(false)
			_ = server.Close(nil)
			if err == nil {
				err = errors.New("collector-stream server exited unexpectedly")
			}
			return err
		case <-ticker.C:
			state.SetReady(app.Ready())
		}
	}
}

// metricsReportInterval 是运行指标上报间隔，与其他组件的 metrics 定时器一致。
const metricsReportInterval = 30 * time.Second

func reportMetrics(ctx context.Context, reporter *report.Handler) {
	ticker := time.NewTicker(metricsReportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := reporter.Handle(callCtx); err != nil {
				log.Warnf("collector-stream metrics report failed: %v", err)
			}
			cancel()
		}
	}
}

func init() {
	if os.Getenv("MOOX_INSTANCE_ID") == "" {
		_ = os.Setenv("MOOX_INSTANCE_ID", "collector-stream")
	}
}
