package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	bootstrapeventbus "github.com/mooyang-code/moox/modules/storage/internal/bootstrap/eventbus"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	storagesvc "github.com/mooyang-code/moox/modules/storage/internal/service/access"
	primarysvc "github.com/mooyang-code/moox/modules/storage/internal/service/primary"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	"github.com/mooyang-code/moox/packages/healthz/trpcotel"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-database/timer"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

var storageStartedAt = time.Now()

func init() {
	registerStorageFlags(flag.CommandLine)
}

func registerStorageFlags(flags *flag.FlagSet) {
	if flags == nil || flags.Lookup("storage-conf") != nil {
		return
	}
	flags.String("storage-conf", "", "storage business config path")
}

func main() {
	defer shutdownTracing()
	// 清除unix域套接字文件，避免内部使用unix域套接字的服务启动失败
	clearSocketFiles()

	// 创建trpc服务器
	s := trpc.NewServer()
	trpclog.InstallServiceName("storage")

	frameworkConfigPath := configPathFromArgs(os.Args)
	storageConfigPath := storageConfigPathFromArgs(os.Args, frameworkConfigPath)
	cfg := loadRuntimeConfig(storageConfigPath)
	if root := os.Getenv("MOOX_STORAGE_HOME"); root != "" {
		cfg.Storage.ApplyHomeRoot(root)
	}
	opts := storageOptionsFromConfig(cfg.Storage)
	if err := validateStorageDeployment(cfg.Storage); err != nil {
		exitWithStartupError("storage deployment config invalid", err)
	}
	var rowsCommittedBus coreeventbus.Bus
	if needsRowsCommittedBus(cfg.Storage) {
		var err error
		rowsCommittedBus, err = bootstrapeventbus.NewRowsCommittedBus(trpc.BackgroundContext(), cfg.Storage.EventBus)
		if err != nil {
			exitWithStartupError("初始化 storage eventbus 失败", err)
		}
		defer func() {
			if err := rowsCommittedBus.Close(); err != nil {
				log.Errorf("关闭 storage eventbus 失败: %v", err)
			}
		}()
		opts.Events = rowsCommittedBus
	}

	var storageService *storagesvc.Service
	if shouldCreateStorageService(cfg.Storage) {
		storageService = storagesvc.NewServiceWithOptions(opts)
		defer func() {
			if err := storageService.Close(); err != nil {
				log.Errorf("关闭 storage service 失败: %v", err)
			}
		}()
	}
	accessReader := accessReaderForRuntime(cfg.Storage, storageService)

	if cfg.Storage.HasRole("access") {
		if storageService == nil {
			exitWithStartupError("access role requires storage service", nil)
		}
		pb.RegisterMetadataService(s, storageService)
		pb.RegisterAccessService(s, storageService)
		registerAccessScanService(s, storageService)
	}
	if err := registerHostMetricsCleanupTimer(s, storageService, cfg.Storage); err != nil {
		exitWithStartupError("register host metrics cleanup timer", err)
	}

	var runtimeView *viewRuntime
	if shouldRegisterViewQueryRole(cfg.Storage) || shouldStartViewBuilderRole(cfg.Storage) || shouldStartViewIndexRole(cfg.Storage) {
		var err error
		runtimeView, err = registerViewRole(s, cfg.Storage, rowsCommittedBus, storageService, accessReader)
		if err != nil {
			exitWithStartupError("初始化 ViewService 失败", err)
		}
		log.Infof("View role initialized")
		defer func() {
			if err := runtimeView.Close(); err != nil {
				log.Errorf("关闭 view runtime 失败: %v", err)
			}
		}()
	} else {
		registerNoopViewTimers(s)
	}

	if shouldCreatePrimaryService(cfg.Storage) {
		var messagePublisher primarysvc.MessagePublisher
		if candidate, ok := rowsCommittedBus.(primarysvc.MessagePublisher); ok {
			messagePublisher = candidate
		}
		primaryService := primarysvc.NewService(primarysvc.Options{
			Root:       opts.Root,
			PebblePath: opts.PebblePath,
			ShardID:    opts.ShardID,
			Publisher:  messagePublisher,
			Outbox: primarysvc.OutboxConfig{
				FlushBatchSize: cfg.Storage.Primary.Outbox.FlushBatchSize,
				FlushMaxBytes:  cfg.Storage.Primary.Outbox.FlushMaxBytes,
				FlushInterval:  time.Duration(cfg.Storage.Primary.Outbox.FlushIntervalMS) * time.Millisecond,
				MaxRows:        cfg.Storage.Primary.Outbox.MaxRows,
				MaxBytes:       cfg.Storage.Primary.Outbox.MaxBytes,
				MaxAge:         time.Duration(cfg.Storage.Primary.Outbox.MaxAgeHours) * time.Hour,
				BackoffBase:    time.Duration(cfg.Storage.Primary.Outbox.BackoffBaseMS) * time.Millisecond,
				BackoffMax:     time.Duration(cfg.Storage.Primary.Outbox.BackoffMaxMS) * time.Millisecond,
			},
		})
		defer func() {
			if err := primaryService.Close(); err != nil {
				log.Errorf("关闭 primary service 失败: %v", err)
			}
		}()
		pb.RegisterPrimaryStoreService(s, primaryService)
	}
	if err := registerStorageHealth(s, cfg.Storage, storageHealthDependencies{
		eventbus: rowsCommittedBus,
		view:     viewRuntimeReadiness{runtime: runtimeView, storage: cfg.Storage},
	}); err != nil {
		exitWithStartupError("register storage health service", err)
	}
	registerStorageMetricsReporter(s, cfg.Storage)
	// 启动trpc服务器
	log.Infof("Storage roles %v serving", cfg.Storage.Roles)
	if err := s.Serve(); err != nil {
		log.Errorf("trpc服务器出错: %v", err)
	}
	log.Warnf("Storage roles %v stopped", cfg.Storage.Roles)
}

func shutdownTracing() {
	ctx, cancel := context.WithTimeout(trpc.BackgroundContext(), 5*time.Second)
	defer cancel()
	if err := trpcotel.Shutdown(ctx); err != nil {
		log.Errorf("flush OpenTelemetry spans: %v", err)
	}
}

func registerStorageMetricsReporter(s *server.Server, storage storageconfig.StorageConfig) {
	if s == nil {
		return
	}
	serviceName := "storage_access"
	timerName := "trpc.moox.storage.access.metrics.timer"
	switch {
	case storage.HasRole("primary") && !storage.HasRole("access"):
		serviceName = "storage_primary_trpc"
	case storage.HasRole("view_index") && !storage.HasRole("access"):
		serviceName, timerName = "storage_view_index", "trpc.moox.storage.view_index.metrics.timer"
	case storage.HasRole("view_builder") && !storage.HasRole("access"):
		serviceName, timerName = "storage_view_builder", "trpc.moox.storage.view_builder.metrics.timer"
	case storage.HasRole("view_query") && !storage.HasRole("access"):
		serviceName, timerName = "storage_view_query", "trpc.moox.storage.view_query.metrics.timer"
	case storage.HasRole("view") && !storage.HasRole("access"):
		serviceName, timerName = "storage_view", "trpc.moox.storage.view.metrics.timer"
	}
	h, err := report.NewHandler(report.DefaultConfig(serviceName))
	if err != nil {
		log.Warnf("storage metrics reporter disabled: %v", err)
		return
	}
	service := s.Service(timerName)
	if service == nil {
		log.Warnf("storage metrics timer service %s is not configured, skip register", timerName)
		return
	}
	timer.RegisterHandlerService(service, h.Handle)
}

func registerAccessScanService(s *server.Server, service pb.AccessScanService) {
	for _, name := range []string{
		"trpc.moox.storage.AccessScan.trpc",
		"trpc.moox.storage.AccessScan",
	} {
		if target := s.Service(name); target != nil {
			pb.RegisterAccessScanService(target, service)
			return
		}
	}
	exitWithStartupError("access role requires the internal AccessScan tRPC service", nil)
}

func exitWithStartupError(message string, err error) {
	if err != nil {
		log.Errorf("%s: %v", message, err)
		_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", message, err)
	} else {
		log.Errorf("%s", message)
		_, _ = fmt.Fprintln(os.Stderr, message)
	}
	os.Exit(1)
}
