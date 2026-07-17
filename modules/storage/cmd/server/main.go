package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	bootstrapeventbus "github.com/mooyang-code/moox/modules/storage/internal/bootstrap/eventbus"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/health"
	deviceduckdb "github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb"
	storagesvc "github.com/mooyang-code/moox/modules/storage/internal/service/access"
	primarysvc "github.com/mooyang-code/moox/modules/storage/internal/service/primary"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view"
	viewbuilder "github.com/mooyang-code/moox/modules/storage/internal/service/view/builder"
	searchsvc "github.com/mooyang-code/moox/modules/storage/internal/service/view/search"
	viewindexsvc "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/healthz"
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
	var rowsChangedBus coreeventbus.Bus
	if needsRowsUpdatedBus(cfg.Storage) {
		var err error
		rowsChangedBus, err = bootstrapeventbus.NewRowsUpdatedBus(trpc.BackgroundContext(), cfg.Storage.EventBus)
		if err != nil {
			exitWithStartupError("初始化 storage eventbus 失败", err)
		}
		defer func() {
			if err := rowsChangedBus.Close(); err != nil {
				log.Errorf("关闭 storage eventbus 失败: %v", err)
			}
		}()
		opts.Events = rowsChangedBus
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
		startHostRetention(storageService, cfg.Storage.View.Maintenance)
	}

	if shouldRegisterViewQueryRole(cfg.Storage) || shouldStartViewBuilderRole(cfg.Storage) || shouldStartViewIndexRole(cfg.Storage) {
		viewRuntime, err := registerViewRole(s, cfg.Storage, rowsChangedBus, storageService, accessReader)
		if err != nil {
			exitWithStartupError("初始化 ViewService 失败", err)
		}
		log.Infof("View role initialized")
		defer func() {
			if err := viewRuntime.Close(); err != nil {
				log.Errorf("关闭 view runtime 失败: %v", err)
			}
		}()
	} else {
		registerNoopViewTimers(s)
	}

	if shouldCreatePrimaryService(cfg.Storage) {
		var envelopePublisher primarysvc.EnvelopePublisher
		if candidate, ok := rowsChangedBus.(primarysvc.EnvelopePublisher); ok {
			envelopePublisher = candidate
		}
		primaryService := primarysvc.NewService(primarysvc.Options{
			Root:       opts.Root,
			PebblePath: opts.PebblePath,
			Publisher:  envelopePublisher,
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
	if err := registerStorageHealth(s, cfg.Storage); err != nil {
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

func startHostRetention(access *storagesvc.Service, maintenance storageconfig.StorageViewMaintenance) {
	if access == nil || !maintenance.IsEnabled() {
		return
	}
	retention, ok := parseStorageDuration(maintenance.HostRetention)
	if !ok || retention <= 0 {
		return
	}
	interval, ok := parseStorageDuration(maintenance.HostInterval)
	if !ok || interval <= 0 {
		interval = time.Hour
	}
	ctx, cancel := context.WithCancel(trpc.BackgroundContext())
	go func() {
		defer cancel()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if deleted, err := access.PruneHostDatasets(ctx, "moox_system", maintenance.HostDatasetIDs, retention, time.Now().UTC()); err != nil {
				log.Warnf("host dataset retention failed: %v", err)
			} else if deleted > 0 {
				log.Infof("host dataset retention deleted %d rows", deleted)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
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

type viewRuntime struct {
	service *view.Service
	builder *viewbuilder.Service
	duck    *deviceduckdb.IndexManager
	search  *searchsvc.Service
}

func (r *viewRuntime) Close() error {
	if r == nil {
		return nil
	}
	var err error
	if r.builder != nil {
		err = errors.Join(err, r.builder.Close())
	}
	if r.service != nil {
		err = errors.Join(err, r.service.Close())
	}
	view.SetDefaultMaintenance(nil)
	if r.search != nil {
		err = errors.Join(err, r.search.Close())
	}
	if r.duck != nil {
		err = errors.Join(err, r.duck.Close())
	}
	return err
}

func registerStorageHealth(s *server.Server, storage storageconfig.StorageConfig) error {
	serviceName := storageServiceName(storage)
	state := health.New("storage", serviceName, "", "")
	state.SnapshotFunc = storageHealthSnapshot(storage, state)
	if s == nil {
		return fmt.Errorf("storage health service is unavailable")
	}
	if err := health.Register(s.Service("trpc.moox.storage.Health"), state); err != nil {
		return fmt.Errorf("storage health server failed to start: %w", err)
	}
	return nil
}

func storageHealthSnapshot(storage storageconfig.StorageConfig, state *health.State) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		serviceName := storageServiceName(storage)
		roleSummary := storageRoleSummary(storage)
		rootReady := storage.Root != "" && pathExists(storage.Root)
		metadataRequired := roleSummary != "view_index"
		metadataReady := !metadataRequired || (storage.Metadata.Path != "" && pathExists(storage.Metadata.Path))
		ready := rootReady && metadataReady
		state.SetReady(ready)
		rsp := healthz.Base("storage", serviceName, "", "", storageStartedAt, ready)
		rsp.Service = serviceName
		rsp.Details = map[string]any{
			"service":          serviceName,
			"role":             roleSummary,
			"roles":            roleSummary,
			"root":             storage.Root,
			"eventbus_type":    storage.EventBus.Type,
			"metadata_path":    storage.Metadata.Path,
			"view_max_workers": storage.View.MaxWorkers,
			"primary_service":  storage.Primary.ServiceName,
			"root_ready":       rootReady,
			"metadata_ready":   metadataReady,
		}
		return rsp
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func storageServiceName(storage storageconfig.StorageConfig) string {
	roleSummary := storageRoleSummary(storage)
	switch roleSummary {
	case "view_query":
		return "storage-view-query"
	case "view_builder":
		return "storage-view-builder"
	case "view_index":
		return "storage-view-index"
	case "access":
		return "storage-access"
	case "access,view", "view":
		return "storage"
	default:
		if roleSummary == "" {
			return "storage"
		}
		return "storage-" + strings.ReplaceAll(strings.ReplaceAll(roleSummary, "_", "-"), ",", "-")
	}
}

func storageRoleSummary(storage storageconfig.StorageConfig) string {
	return strings.Join(storage.Roles, ",")
}

func registerViewRole(s *server.Server, storage storageconfig.StorageConfig, events coreeventbus.Subscriber, storageService *storagesvc.Service, accessReader viewbuilder.AccessReader) (*viewRuntime, error) {
	runtime := &viewRuntime{}
	var (
		duckEngine      view.ManagedViewIndex
		bleveEngine     view.ManagedViewIndex
		timeSeriesQuery view.TimeSeriesIndexQuery
		recordQuery     view.RecordIndexQuery
	)
	if shouldStartViewIndexRole(storage) {
		duckManager, err := deviceduckdb.OpenIndexManager(deviceduckdb.IndexManagerOptions{Root: storage.Devices.ViewIndexRoot})
		if err != nil {
			return nil, err
		}
		searchService := searchsvc.NewService(searchsvc.Options{Root: storage.Devices.ViewIndexRoot})
		runtime.duck = duckManager
		runtime.search = searchService
		duckEngine = duckManager
		bleveEngine = searchService
		timeSeriesQuery = duckManager
		recordQuery = searchService
		ownerService := viewindexsvc.NewService(viewindexsvc.Options{
			Engines: map[string]viewindexsvc.ManagedEngine{
				"duckdb": duckManager,
				"bleve":  searchService,
			},
			TimeSeries: duckManager,
			Records:    searchService,
		})
		pb.RegisterViewIndexService(s, ownerService)
		duckClient := viewindexsvc.NewLocalClient(ownerService, "duckdb")
		bleveClient := viewindexsvc.NewLocalClient(ownerService, "bleve")
		duckEngine = duckClient
		bleveEngine = bleveClient
		timeSeriesQuery = duckClient
		recordQuery = bleveClient
	} else {
		duckClient := viewindexsvc.NewRemoteClient(storage.View.IndexServiceName, "duckdb")
		bleveClient := viewindexsvc.NewRemoteClient(storage.View.IndexServiceName, "bleve")
		duckEngine = duckClient
		bleveEngine = bleveClient
		timeSeriesQuery = duckClient
		recordQuery = bleveClient
	}

	var viewMetadata view.Metadata
	if shouldRegisterViewQueryRole(storage) || shouldStartViewBuilderRole(storage) {
		viewMetadata = metadataForViewRuntime(storage, storageService)
	}
	var viewService *view.Service
	if shouldRegisterViewQueryRole(storage) {
		viewService = view.NewService(view.ServiceOptions{
			Metadata:          viewMetadata,
			TimeSeriesIndexes: timeSeriesQuery,
			RecordIndexes:     recordQuery,
		})
		runtime.service = viewService
		pb.RegisterDataViewService(s, viewService)
	}
	var builderService *viewbuilder.Service
	if shouldStartViewBuilderRole(storage) {
		engines := map[string]view.ManagedViewIndex{
			"duckdb": duckEngine,
			"bleve":  bleveEngine,
		}
		maintenance := view.NewMaintenanceManager(view.MaintenanceOptions{
			Metadata: viewMetadata,
			Engines:  engines,
			Config:   maintenanceConfigFromStorage(storage.View.Maintenance, storage.View.MaxWorkers),
			Facts:    accessReader,
			Records:  accessReader,
		})
		view.SetDefaultMaintenance(maintenance)
		timer.RegisterScheduler("viewBuilderSchedule", &timer.DefaultScheduler{})
		registerTimerHandlerService("trpc.moox.storage.view.timer", s.Service("trpc.moox.storage.view.timer"), func(ctx context.Context) error {
			return view.HandleSchedule(ctx, "op=maintain")
		})
		var err error
		builderService, err = startViewBuilderService(trpc.BackgroundContext(), storage, events, viewMetadata, map[string]viewindex.ViewIndexEngine{
			"duckdb": duckEngine,
			"bleve":  bleveEngine,
		}, accessReader)
		if err != nil {
			_ = runtime.Close()
			return nil, err
		}
		runtime.builder = builderService
	} else {
		registerNoopViewTimers(s)
	}
	return runtime, nil
}

func startViewBuilderService(ctx context.Context, storage storageconfig.StorageConfig, events coreeventbus.Subscriber, metadata view.Metadata, engines map[string]viewindex.ViewIndexEngine, accessReader viewbuilder.AccessReader) (*viewbuilder.Service, error) {
	service := viewbuilder.NewService(viewbuilder.Options{
		Events:     events,
		Reader:     accessReader,
		Metadata:   metadata,
		Engines:    engines,
		BatchSize:  storage.View.BatchSize,
		BatchWait:  time.Duration(storage.View.BatchWaitMS) * time.Millisecond,
		MaxWorkers: storage.View.MaxWorkers,
	})
	if err := service.Start(ctx); err != nil {
		return nil, err
	}
	return service, nil
}

func metadataForViewRuntime(storage storageconfig.StorageConfig, storageService *storagesvc.Service) view.Metadata {
	if storageService != nil {
		return storageService.MetadataStore()
	}
	return view.NewRemoteMetadata(storage.View.MetadataServiceName)
}

func maintenanceConfigFromStorage(raw storageconfig.StorageViewMaintenance, maxViewsPerRun int) view.MaintenanceConfig {
	cfg := view.MaintenanceConfig{
		Enabled:                   raw.IsEnabled(),
		OwnerID:                   maintenanceOwnerID(raw.OwnerID),
		PageSize:                  uint32(raw.PageSize),
		MaxEntries:                int64(raw.MaxEntries),
		TargetEntries:             int64(raw.TargetEntries),
		MaxPhysicalBytes:          uint64(max(raw.MaxPhysicalBytes, 0)),
		MinFreeDiskBytes:          uint64(max(raw.MinFreeDiskBytes, 0)),
		MinReadyEntries:           int64(raw.MinReadyEntries),
		MaxViewsPerRun:            maxViewsPerRun,
		TimeSeriesRetentionByFreq: make(map[string]time.Duration, len(raw.TimeSeries.RetentionByFreq)),
	}
	if d, ok := parseStorageDuration(raw.LeaseTTL); ok {
		cfg.LeaseTTL = d
	}
	if d, ok := parseStorageDuration(raw.RunBudget); ok {
		cfg.RunBudget = d
	}
	if d, ok := parseStorageDuration(raw.OverlapWindow); ok {
		cfg.OverlapWindow = d
	}
	if d, ok := parseStorageDuration(raw.AllowedLag); ok {
		cfg.AllowedLag = d
	}
	if d, ok := parseStorageDuration(raw.RemoveGrace); ok {
		cfg.RemoveGrace = d
	}
	if d, ok := parseStorageDuration(raw.TimeSeries.DefaultRetentionWindow); ok {
		cfg.TimeSeriesDefaultRetention = d
	}
	for freq, window := range raw.TimeSeries.RetentionByFreq {
		if d, ok := parseStorageDuration(window); ok {
			cfg.TimeSeriesRetentionByFreq[freq] = d
		}
	}
	if d, ok := parseStorageDuration(raw.Record.RetentionWindow); ok {
		cfg.RecordRetention = d
	}
	return cfg
}

func maintenanceOwnerID(configured string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "storage-view-builder"
	}
	raw := make([]byte, 6)
	suffix := ""
	if _, err := rand.Read(raw); err == nil {
		suffix = hex.EncodeToString(raw)
	} else {
		suffix = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return fmt.Sprintf("%s:%d:%s", host, os.Getpid(), suffix)
}

// parseStorageDuration parses durations from storage.yaml, which uses
// standard Go duration suffixes (h/m/s) plus "d" for days (e.g. "30d",
// "730d") that time.ParseDuration does not support natively.
func parseStorageDuration(raw string) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d, true
	}
	unit := raw[len(raw)-1:]
	numberPart := raw[:len(raw)-1]
	count, err := strconv.ParseFloat(numberPart, 64)
	if err != nil {
		return 0, false
	}
	switch unit {
	case "d", "D":
		return time.Duration(count * 24 * float64(time.Hour)), true
	case "w", "W":
		return time.Duration(count * 7 * 24 * float64(time.Hour)), true
	default:
		return 0, false
	}
}

func registerNoopViewTimers(s *server.Server) {
	timer.RegisterScheduler("viewBuilderSchedule", &timer.DefaultScheduler{})
	noop := func(context.Context) error { return nil }
	registerTimerHandlerService("trpc.moox.storage.view.timer", s.Service("trpc.moox.storage.view.timer"), noop)
}

func registerTimerHandlerService(name string, service server.Service, handle func(context.Context) error) bool {
	if service == nil {
		log.Warnf("timer service %s is not configured, skip register", name)
		return false
	}
	timer.RegisterHandlerService(service, handle)
	return true
}

func validateStorageDeployment(storage storageconfig.StorageConfig) error {
	if shouldStartViewBuilderRole(storage) && !storage.HasRole("access") && isMemoryRowsUpdatedBus(storage.EventBus) {
		return errors.New("storage view builder role requires non-memory eventbus when access role is not in the same process")
	}
	return nil
}

func needsRowsUpdatedBus(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("access") || storage.HasRole("primary") || shouldStartViewBuilderRole(storage)
}

func shouldRegisterViewQueryRole(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("view") || storage.HasRole("view_query")
}

func shouldStartViewBuilderRole(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("view") || storage.HasRole("view_builder")
}

func shouldStartViewIndexRole(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("view") || storage.HasRole("view_index")
}

func shouldCreateStorageService(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("access")
}

func shouldCreatePrimaryService(storage storageconfig.StorageConfig) bool {
	if storage.HasRole("primary") {
		return true
	}
	return storage.HasRole("access") && strings.TrimSpace(storage.Primary.ServiceName) == ""
}

func accessReaderForRuntime(storage storageconfig.StorageConfig, storageService *storagesvc.Service) viewbuilder.AccessReader {
	var local viewbuilder.AccessReader
	accessServiceName := storage.View.AccessServiceName
	scanServiceName := storage.View.AccessScanServiceName
	if storageService != nil && storage.HasRole("access") {
		local = storageService.FactReader()
		accessServiceName = ""
		scanServiceName = ""
	}
	return viewbuilder.NewAccessReader(local, accessServiceName, scanServiceName)
}

func shouldUseLocalAccessReader(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("access") && shouldStartViewBuilderRole(storage) && isMemoryRowsUpdatedBus(storage.EventBus)
}

func isMemoryRowsUpdatedBus(cfg storageconfig.StorageEventBus) bool {
	kind := strings.ToLower(strings.TrimSpace(cfg.Type))
	return kind == "" || kind == "memory"
}

func configPathFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-conf=") {
			return strings.TrimPrefix(arg, "-conf=")
		}
		if strings.HasPrefix(arg, "--conf=") {
			return strings.TrimPrefix(arg, "--conf=")
		}
		if (arg == "-conf" || arg == "--conf") && i+1 < len(args) {
			return args[i+1]
		}
	}
	if path := os.Getenv("STORAGE_CONFIG_FILE"); path != "" {
		return path
	}
	if dir := os.Getenv("STORAGE_CONFIG_PATH"); dir != "" {
		return filepath.Join(dir, "trpc_go.yaml")
	}
	return filepath.Join("config", "trpc_go.yaml")
}

func storageConfigPathFromArgs(args []string, frameworkConfigPath string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-storage-conf=") {
			return strings.TrimPrefix(arg, "-storage-conf=")
		}
		if strings.HasPrefix(arg, "--storage-conf=") {
			return strings.TrimPrefix(arg, "--storage-conf=")
		}
		if (arg == "-storage-conf" || arg == "--storage-conf") && i+1 < len(args) {
			return args[i+1]
		}
	}
	if path := os.Getenv("MOOX_STORAGE_CONFIG"); path != "" {
		return path
	}
	if path := os.Getenv("STORAGE_APP_CONFIG"); path != "" {
		return path
	}
	if dir := os.Getenv("STORAGE_CONFIG_PATH"); dir != "" {
		return filepath.Join(dir, "storage.yaml")
	}
	if frameworkConfigPath != "" {
		return filepath.Join(filepath.Dir(frameworkConfigPath), "storage.yaml")
	}
	return filepath.Join("config", "storage.yaml")
}

func loadStorageOptions(configPath string) storagesvc.Options {
	cfg, ok := loadStorageConfig(configPath)
	if !ok {
		return storagesvc.Options{}
	}
	return storageOptionsFromConfig(cfg.Storage)
}

func storageOptionsFromConfig(storage storageconfig.StorageConfig) storagesvc.Options {
	return storagesvc.Options{
		Root:               storage.Root,
		MetadataPath:       storage.Metadata.Path,
		PebblePath:         storage.Devices.PebblePath,
		ParquetPath:        storage.Devices.ParquetPath,
		PrimaryServiceName: storage.Primary.ServiceName,
	}
}

func loadRuntimeConfig(configPath string) storageconfig.RuntimeConfig {
	if cfg, ok := loadStorageConfig(configPath); ok {
		return cfg
	}
	var cfg storageconfig.RuntimeConfig
	cfg.ApplyDefaults()
	return cfg
}

func loadStorageConfig(configPath string) (storageconfig.RuntimeConfig, bool) {
	var cfg storageconfig.RuntimeConfig
	if configPath == "" {
		return cfg, false
	}
	dir := filepath.Dir(configPath)
	file := filepath.Base(configPath)
	if err := storageconfig.NewConfigLoader(dir).LoadConfigWithDefaults(file, &cfg, cfg.ApplyDefaults); err != nil {
		log.Warnf("加载 storage 配置失败，使用默认目录: %v", err)
		return cfg, false
	}
	return cfg, true
}

func clearSocketFiles() {
	files, err := filepath.Glob("./*")
	if err != nil {
		log.Errorf("读取目录失败: %v", err)
		return
	}

	for _, file := range files {
		baseFile := filepath.Base(file)
		if strings.HasPrefix(baseFile, "0.0.0.0") || strings.HasPrefix(baseFile, "127.0.0.1") {
			if err := os.Remove(file); err != nil {
				log.Errorf("删除文件 %s 失败: %v", file, err)
			}
		}
	}
}
