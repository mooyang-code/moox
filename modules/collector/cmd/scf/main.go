package main

import (
	"context"
	"flag"
	"fmt"
	"os"
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

const defaultOnceTimeout = 120 * time.Second
const residentRunnerRetryDelay = time.Second

var Version string

type onceOptions struct {
	ServiceGatewayTarget    string
	NodeID                  string
	StorageRPCGatewayTarget string
	Timeout                 time.Duration
}

var (
	registerCloudFunction = serverless.RegisterCloudFunction
	runTaskRunner         = taskrunner.Run
	runConfiguredRunner   = taskrunner.RunConfigured
)

func main() {
	once := flag.Bool("once", false, "diagnostically fetch and execute available CloudNode JobItems, then exit")
	resident := flag.Bool("resident", false, "diagnostically consume CloudNode JobItems until interrupted")
	opts := onceOptionsFromEnv()
	flag.StringVar(&opts.ServiceGatewayTarget, "service-gateway-target", opts.ServiceGatewayTarget, "service gateway target for CloudRuntime callbacks")
	flag.StringVar(&opts.NodeID, "node-id", opts.NodeID, "runtime node id")
	flag.StringVar(&opts.StorageRPCGatewayTarget, "storage-rpc-gateway-target", opts.StorageRPCGatewayTarget, "storage tRPC gateway target")
	flag.DurationVar(&opts.Timeout, "timeout", opts.Timeout, "one-shot execution timeout")
	flag.Parse()
	if *once && *resident {
		panic("-once and -resident cannot be used together")
	}

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
		if err := initializeRuntime(ctx, cfg); err != nil {
			panic("failed to initialize one-shot collector runtime: " + err.Error())
		}
		if err := runOnce(ctx, opts); err != nil {
			panic("failed to run one-shot collector runtime: " + err.Error())
		}
		return
	}
	if *resident {
		ctx := trpc.BackgroundContext()
		if err := initializeRuntime(ctx, cfg); err != nil {
			panic("failed to initialize resident collector runtime: " + err.Error())
		}
		configureResidentRuntime(opts)
		if err := runConfiguredRunner(ctx); err != nil {
			panic("failed to run resident collector runtime: " + err.Error())
		}
		return
	}

	if err := startProductionRuntime(trpc.BackgroundContext(), cfg); err != nil {
		panic("failed to initialize bootstrap: " + err.Error())
	}

	log.Info("数据采集器 SCF runtime 启动完成")
	select {}
}

func configureResidentRuntime(opts onceOptions) {
	if strings.TrimSpace(opts.ServiceGatewayTarget) != "" {
		runtimeapp.UpdateServiceGatewayTarget(opts.ServiceGatewayTarget)
	}
	if strings.TrimSpace(opts.NodeID) != "" {
		_, version := runtimeapp.GetNodeInfo()
		runtimeapp.UpdateNodeInfo(opts.NodeID, version)
	}
	if strings.TrimSpace(opts.StorageRPCGatewayTarget) != "" {
		runtimeapp.UpdateStorageRPCGatewayTarget(opts.StorageRPCGatewayTarget)
	}
}

func initializeRuntime(ctx context.Context, cfg *runtimeapp.AppConfig) error {
	if _, err := runtimeapp.LoadConfigs(cfg); err != nil {
		return err
	}
	if _, err := runtimebootstrap.StartBackgroundServices(ctx); err != nil {
		return err
	}
	return nil
}

func startProductionRuntime(ctx context.Context, cfg *runtimeapp.AppConfig) error {
	if strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")) == "" {
		return fmt.Errorf("MOOX_SPACE_ID is required")
	}
	if err := initializeRuntime(ctx, cfg); err != nil {
		return err
	}
	registerCloudFunction()
	go func() {
		runResidentTaskRunner(ctx, runTaskRunner, residentRunnerRetryDelay)
	}()
	return nil
}

func runResidentTaskRunner(ctx context.Context, run func(context.Context) error, retryDelay time.Duration) {
	for ctx.Err() == nil {
		err := run(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Errorf("resident CloudNode JobItem taskrunner stopped: %v", err)
		} else {
			log.Error("resident CloudNode JobItem taskrunner stopped unexpectedly")
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func onceOptionsFromEnv() onceOptions {
	opts := onceOptions{
		ServiceGatewayTarget:    strings.TrimSpace(os.Getenv("MOOX_SERVICE_GATEWAY_TARGET")),
		NodeID:                  strings.TrimSpace(os.Getenv("MOOX_RUNTIME_NODE_ID")),
		StorageRPCGatewayTarget: strings.TrimSpace(os.Getenv("MOOX_STORAGE_RPC_GATEWAY_TARGET")),
		Timeout:                 durationEnv("MOOX_RUNTIME_ONCE_TIMEOUT", defaultOnceTimeout),
	}
	return opts
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
	runtimeapp.UpdateStorageRPCGatewayTarget(opts.StorageRPCGatewayTarget)
	return taskrunner.RunOnce(ctx)
}
