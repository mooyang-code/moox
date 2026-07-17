package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/serverless"
	runtimebootstrap "github.com/mooyang-code/moox/modules/collector/internal/serverless/bootstrap"
	"github.com/mooyang-code/moox/modules/collector/internal/taskrunner"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	_ "trpc.group/trpc-go/trpc-log-cls"
)

var Version string

type onceOptions struct {
	ServiceGatewayTarget  string
	NodeID                string
	StorageMetadataTarget string
	StorageAccessTarget   string
	Timeout               time.Duration
}

func main() {
	once := flag.Bool("once", false, "poll and execute CloudNode JobItems once, then exit")
	opts := onceOptionsFromEnv()
	flag.StringVar(&opts.ServiceGatewayTarget, "service-gateway-target", opts.ServiceGatewayTarget, "service gateway target for CloudRuntime callbacks")
	flag.StringVar(&opts.NodeID, "node-id", opts.NodeID, "runtime node id")
	flag.StringVar(&opts.StorageMetadataTarget, "storage-metadata-target", opts.StorageMetadataTarget, "storage metadata tRPC target")
	flag.StringVar(&opts.StorageAccessTarget, "storage-access-target", opts.StorageAccessTarget, "storage access tRPC target")
	flag.DurationVar(&opts.Timeout, "timeout", opts.Timeout, "one-shot execution timeout")
	flag.Parse()

	cfg := runtimeapp.DefaultConfig()
	if Version != "" {
		cfg.System.Version = Version
		runtimeapp.UpdateNodeInfo("", Version)
	}

	if *once {
		ctx := trpc.BackgroundContext()
		if opts.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
			defer cancel()
		}
		if err := initializeRuntime(ctx, cfg, false); err != nil {
			panic("failed to initialize one-shot collector runtime: " + err.Error())
		}
		if err := runOnce(ctx, opts); err != nil {
			panic("failed to run one-shot collector runtime: " + err.Error())
		}
		return
	}

	if err := initializeServerlessRuntime(trpc.BackgroundContext(), cfg); err != nil {
		panic("failed to initialize bootstrap: " + err.Error())
	}
	serverless.RegisterCloudFunction()

	log.Info("数据采集器 SCF runtime 启动完成")
	select {}
}

func initializeRuntime(ctx context.Context, cfg *runtimeapp.AppConfig, startTRPC bool) error {
	if _, err := runtimeapp.LoadConfigs(cfg); err != nil {
		return err
	}
	if _, err := runtimebootstrap.StartBackgroundServices(ctx); err != nil {
		return err
	}
	if startTRPC {
		return runtimebootstrap.RegisterTRPCServices()
	}
	return nil
}

func initializeServerlessRuntime(ctx context.Context, cfg *runtimeapp.AppConfig) error {
	return initializeRuntime(ctx, cfg, false)
}

func onceOptionsFromEnv() onceOptions {
	opts := onceOptions{
		ServiceGatewayTarget:  strings.TrimSpace(os.Getenv("MOOX_SERVICE_GATEWAY_TARGET")),
		NodeID:                strings.TrimSpace(os.Getenv("MOOX_RUNTIME_NODE_ID")),
		StorageMetadataTarget: strings.TrimSpace(os.Getenv("MOOX_STORAGE_METADATA_TARGET")),
		StorageAccessTarget:   strings.TrimSpace(os.Getenv("MOOX_STORAGE_ACCESS_TARGET")),
		Timeout:               durationEnv("MOOX_RUNTIME_ONCE_TIMEOUT", 90*time.Second),
	}
	if opts.ServiceGatewayTarget == "" {
		serverIP := strings.TrimSpace(os.Getenv("MOOX_RUNTIME_SERVER_IP"))
		serverPort := intEnv("MOOX_RUNTIME_SERVER_PORT")
		if serverIP != "" && serverPort > 0 {
			opts.ServiceGatewayTarget = fmt.Sprintf("http://%s:%d", serverIP, serverPort)
		}
	}
	return opts
}

func intEnv(key string) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func runOnce(ctx context.Context, opts onceOptions) error {
	if strings.TrimSpace(opts.ServiceGatewayTarget) == "" {
		return fmt.Errorf("service-gateway-target is required")
	}
	if strings.TrimSpace(opts.NodeID) == "" {
		return fmt.Errorf("node-id is required")
	}
	_, version := runtimeapp.GetNodeInfo()
	runtimeapp.UpdateServiceGatewayTarget(opts.ServiceGatewayTarget)
	runtimeapp.UpdateNodeInfo(opts.NodeID, version)
	runtimeapp.UpdateStorageTargets(opts.StorageMetadataTarget, opts.StorageAccessTarget)
	return taskrunner.PollAndExecuteJobItems(ctx)
}
