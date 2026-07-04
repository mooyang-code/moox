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
	runtimeboot "github.com/mooyang-code/moox/modules/collector/internal/app/runtimeboot"
	"github.com/mooyang-code/moox/modules/collector/internal/serverless"
	"github.com/mooyang-code/moox/modules/collector/internal/taskrunner"
	"trpc.group/trpc-go/trpc-go/log"
	_ "trpc.group/trpc-go/trpc-log-cls"
)

var Version string

type onceOptions struct {
	ServerIP              string
	ServerPort            int
	NodeID                string
	StorageMetadataTarget string
	StorageAccessTarget   string
	Timeout               time.Duration
}

func main() {
	once := flag.Bool("once", false, "poll and execute CloudNode JobItems once, then exit")
	opts := onceOptionsFromEnv()
	flag.StringVar(&opts.ServerIP, "server-ip", opts.ServerIP, "admin gateway host for CloudRuntime callbacks")
	flag.IntVar(&opts.ServerPort, "server-port", opts.ServerPort, "admin gateway port for CloudRuntime callbacks")
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
		ctx := context.Background()
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

	if err := initializeServerlessRuntime(context.Background(), cfg); err != nil {
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
	if _, err := runtimeboot.StartBackgroundServices(ctx); err != nil {
		return err
	}
	if startTRPC {
		return runtimeboot.RegisterTRPCServices()
	}
	return nil
}

func initializeServerlessRuntime(ctx context.Context, cfg *runtimeapp.AppConfig) error {
	return initializeRuntime(ctx, cfg, false)
}

func onceOptionsFromEnv() onceOptions {
	return onceOptions{
		ServerIP:              strings.TrimSpace(os.Getenv("MOOX_RUNTIME_SERVER_IP")),
		ServerPort:            intEnv("MOOX_RUNTIME_SERVER_PORT"),
		NodeID:                strings.TrimSpace(os.Getenv("MOOX_RUNTIME_NODE_ID")),
		StorageMetadataTarget: strings.TrimSpace(os.Getenv("MOOX_STORAGE_METADATA_TARGET")),
		StorageAccessTarget:   strings.TrimSpace(os.Getenv("MOOX_STORAGE_ACCESS_TARGET")),
		Timeout:               durationEnv("MOOX_RUNTIME_ONCE_TIMEOUT", 90*time.Second),
	}
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
	if strings.TrimSpace(opts.ServerIP) == "" || opts.ServerPort <= 0 {
		return fmt.Errorf("server-ip and server-port are required")
	}
	if strings.TrimSpace(opts.NodeID) == "" {
		return fmt.Errorf("node-id is required")
	}
	_, version := runtimeapp.GetNodeInfo()
	runtimeapp.UpdateServerInfo(opts.ServerIP, opts.ServerPort)
	runtimeapp.UpdateNodeInfo(opts.NodeID, version)
	runtimeapp.UpdateStorageTargets(opts.StorageMetadataTarget, opts.StorageAccessTarget)
	return taskrunner.PollAndExecuteJobItems(ctx)
}
