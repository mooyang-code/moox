package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	deviceduckdb "github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb"
	storagesvc "github.com/mooyang-code/moox/modules/storage/internal/service/access"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view"
	viewbuilder "github.com/mooyang-code/moox/modules/storage/internal/service/view/builder"
	searchsvc "github.com/mooyang-code/moox/modules/storage/internal/service/view/search"
	viewindexsvc "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

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
	if shouldRegisterViewQueryRole(storage) {
		viewService := view.NewService(view.ServiceOptions{
			Metadata:          viewMetadata,
			TimeSeriesIndexes: timeSeriesQuery,
			RecordIndexes:     recordQuery,
		})
		runtime.service = viewService
		pb.RegisterDataViewService(s, viewService)
	}
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
		builderService, err := startViewBuilderService(trpc.BackgroundContext(), storage, events, viewMetadata, map[string]viewindex.ViewIndexEngine{
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

// parseStorageDuration supports Go duration suffixes plus days and weeks.
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
	if storage.HasRole("access") && storage.Maintenance.HostMetricsCleanup.IsEnabled() {
		if err := storage.Maintenance.HostMetricsCleanup.Validate(); err != nil {
			return err
		}
	}
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
