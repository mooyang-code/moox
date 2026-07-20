//go:build legacy_storage

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
	"github.com/mooyang-code/moox/modules/storage/internal/service/dataview"
	searchsvc "github.com/mooyang-code/moox/modules/storage/internal/service/dataview/search"
	storagesvc "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	viewbuilder "github.com/mooyang-code/moox/modules/storage/internal/service/viewbuilder"
	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/service/viewbuilder/eventconsumer"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	viewindexsvc "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	deviceduckdb "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/duckdb"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

type viewRuntime struct {
	service       *view.Service
	builder       *viewbuilder.Service
	duck          *deviceduckdb.IndexManager
	search        *searchsvc.Service
	metadataProbe interface{ Ready(context.Context) error }
	metadata      view.Metadata
	heads         view.ShardHeadReader
	engines       map[string]view.ManagedViewIndex
	minFreeBytes  uint64
}

type viewRuntimeReadiness struct {
	runtime *viewRuntime
	storage storageconfig.StorageConfig
}

type multiViewShardHeadReader interface {
	ShardHeadsForDatasets(context.Context, string, []string) (map[string]uint64, error)
}

func runtimeShardHeads(ctx context.Context, reader view.ShardHeadReader, item *pb.View) (map[string]uint64, error) {
	datasetIDs := item.GetDatasetIds()
	if len(datasetIDs) == 0 {
		datasetIDs = []string{item.GetPrimaryDatasetId()}
	}
	if multi, ok := reader.(multiViewShardHeadReader); ok {
		return multi.ShardHeadsForDatasets(ctx, item.GetSpaceId(), datasetIDs)
	}
	return reader.ShardHeads(ctx, item.GetSpaceId(), item.GetPrimaryDatasetId())
}

func (r viewRuntimeReadiness) Ready() bool {
	return r.ReadyContext(trpc.BackgroundContext())
}

func (r viewRuntimeReadiness) ReadyContext(ctx context.Context) bool {
	if r.runtime == nil {
		return false
	}
	if r.runtime.metadataProbe != nil && r.runtime.metadataProbe.Ready(ctx) != nil {
		return false
	}
	if shouldRegisterViewQueryRole(r.storage) && r.runtime.service == nil {
		return false
	}
	if shouldStartViewBuilderRole(r.storage) && (r.runtime.builder == nil || !r.runtime.builder.Ready()) {
		return false
	}
	if shouldStartViewIndexRole(r.storage) && (r.runtime.duck == nil || r.runtime.search == nil) {
		return false
	}
	if shouldStartViewIndexRole(r.storage) {
		if _, err := r.runtime.duck.List(ctx); err != nil {
			return false
		}
		if _, err := r.runtime.search.List(ctx); err != nil {
			return false
		}
		if !r.runtime.freshnessReady(ctx) {
			return false
		}
	}
	return true
}

func (r *viewRuntime) freshnessReady(ctx context.Context) bool {
	if r == nil || r.metadata == nil || r.heads == nil {
		return true
	}
	var views []*pb.View
	for pageNo := uint32(1); ; pageNo++ {
		items, page, err := r.metadata.ListViews(ctx, "", "", "active", &pb.Page{Page: pageNo, Size: 10000})
		if err != nil {
			return false
		}
		views = append(views, items...)
		if page == nil || !page.GetHasMore() || len(items) == 0 {
			break
		}
	}
	for _, item := range views {
		if item == nil {
			return false
		}
		if strings.TrimSpace(item.GetActiveIndexId()) == "" {
			return false
		}
		engine := r.engines[strings.ToLower(strings.TrimSpace(item.GetEngine()))]
		if engine == nil {
			return false
		}
		stats, err := engine.Stat(ctx, item.GetActiveIndexId())
		if err != nil || !stats.Exists {
			return false
		}
		heads, err := runtimeShardHeads(ctx, r.heads, item)
		if err != nil || len(heads) == 0 || len(heads) != len(stats.ShardCheckpoints) {
			return false
		}
		for shardID, head := range heads {
			checkpoint, ok := stats.ShardCheckpoints[shardID]
			if !ok || checkpoint != head {
				return false
			}
		}
		for shardID := range stats.ShardCheckpoints {
			if _, ok := heads[shardID]; !ok {
				return false
			}
		}
		if r.minFreeBytes > 0 && stats.FreeDiskBytes > 0 && stats.FreeDiskBytes < r.minFreeBytes {
			return false
		}
	}
	return true
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

func registerViewRole(s *server.Server, storage storageconfig.StorageConfig, events coreeventbus.Subscriber, storageService *storagesvc.Service, sourceReader viewbuilder.PrimaryStoreReader) (*viewRuntime, error) {
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
		var err error
		viewMetadata, err = metadataForViewRuntime(storage, storageService)
		if err != nil {
			return nil, err
		}
	}
	if shouldRegisterViewQueryRole(storage) {
		var shardHeads view.ShardHeadReader
		if candidate, ok := sourceReader.(view.ShardHeadReader); ok {
			shardHeads = candidate
		}
		viewService := view.NewService(view.ServiceOptions{
			Metadata:          viewMetadata,
			TimeSeriesIndexes: timeSeriesQuery,
			RecordIndexes:     recordQuery,
			Heads:             shardHeads,
			RequireFreshness:  true,
		})
		runtime.service = viewService
		runtime.metadata = viewMetadata
		runtime.heads = shardHeads
		runtime.engines = map[string]view.ManagedViewIndex{"duckdb": duckEngine, "bleve": bleveEngine}
		runtime.minFreeBytes = uint64(max(storage.View.Maintenance.MinFreeDiskBytes, 0))
		if probe, ok := viewMetadata.(interface{ Ready(context.Context) error }); ok {
			runtime.metadataProbe = probe
		}
		pb.RegisterDataViewService(s, viewService)
	}
	if shouldStartViewBuilderRole(storage) {
		engines := map[string]view.ManagedViewIndex{
			"duckdb": duckEngine,
			"bleve":  bleveEngine,
		}
		var shardHeads view.ShardHeadReader
		if candidate, ok := sourceReader.(view.ShardHeadReader); ok {
			shardHeads = candidate
		}
		maintenance := view.NewMaintenanceManager(view.MaintenanceOptions{
			Metadata:     viewMetadata,
			Engines:      engines,
			Config:       maintenanceConfigFromStorage(storage.View.Maintenance, storage.View.MaxWorkers),
			Facts:        sourceReader,
			Records:      sourceReader,
			Heads:        shardHeads,
			RequireHeads: true,
		})
		view.SetDefaultMaintenance(maintenance)
		timer.RegisterScheduler("viewBuilderSchedule", &timer.DefaultScheduler{})
		registerTimerHandlerService("trpc.moox.storage.view.timer", s.Service("trpc.moox.storage.view.timer"), func(ctx context.Context) error {
			return view.HandleSchedule(ctx, "op=maintain")
		})
		builderService, err := startViewBuilderService(trpc.BackgroundContext(), storage, events, viewMetadata, map[string]viewindex.ViewIndexEngine{
			"duckdb": duckEngine,
			"bleve":  bleveEngine,
		}, sourceReader)
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

func startViewBuilderService(ctx context.Context, storage storageconfig.StorageConfig, events coreeventbus.Subscriber, metadata view.Metadata, engines map[string]viewindex.ViewIndexEngine, sourceReader viewbuilder.PrimaryStoreReader) (*viewbuilder.Service, error) {
	service := viewbuilder.NewService(viewbuilder.Options{
		Events:     events,
		Reader:     sourceReader,
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

func metadataForViewRuntime(storage storageconfig.StorageConfig, storageService *storagesvc.Service) (view.Metadata, error) {
	if storageService != nil {
		return storageService.MetadataStore(), nil
	}
	credentials, err := gatewayauth.ResolveCredentials(storage.View.StorageRPC.KeyID, storage.View.StorageRPC.HMACKeyFile)
	if err != nil {
		return nil, err
	}
	target := gatewayauth.ServiceGatewayTarget(storage.View.StorageRPC.GatewayTarget)
	nodeID := gatewayauth.ServiceGatewayNodeID()
	if nodeID == "" {
		nodeID = storage.View.StorageRPC.GatewayNodeID
	}
	options := gatewayauth.NewTRPCClientOptions(target, nodeID, credentials)
	return view.NewRemoteMetadata(storage.View.MetadataServiceName, options...), nil
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
	allowedRoles := map[string]struct{}{"primary": {}, "shard": {}, "view": {}}
	if len(storage.Roles) == 0 {
		return errors.New("storage roles must not be empty")
	}
	for _, role := range storage.Roles {
		role = strings.ToLower(strings.TrimSpace(role))
		if _, ok := allowedRoles[role]; !ok {
			return fmt.Errorf("unsupported storage role %q", role)
		}
	}
	if (storage.HasRole("primary") || storage.HasRole("shard")) && strings.TrimSpace(storage.Primary.ShardID) == "" {
		return errors.New("storage primary.shard_id must not be empty")
	}
	if storage.HasRole("primary") && storage.Maintenance.HostMetricsCleanup.IsEnabled() {
		if err := storage.Maintenance.HostMetricsCleanup.Validate(); err != nil {
			return err
		}
	}
	if shouldStartViewBuilderRole(storage) && !storage.HasRole("primary") && isMemoryRowsCommittedBus(storage.EventBus) {
		return errors.New("storage view builder role requires non-memory eventbus when primary role is not in the same process")
	}
	return nil
}

func needsRowsCommittedBus(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("primary") || storage.HasRole("shard") || storage.HasRole("view")
}

func shouldRegisterViewQueryRole(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("view")
}

func shouldStartViewBuilderRole(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("view")
}

func shouldStartViewIndexRole(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("view")
}

func shouldCreateStorageService(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("primary")
}

func shouldCreatePrimaryService(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("shard") || (storage.HasRole("primary") && strings.TrimSpace(storage.Primary.ServiceName) == "")
}

func sourceReaderForRuntime(storage storageconfig.StorageConfig, storageService *storagesvc.Service) (viewbuilder.PrimaryStoreReader, error) {
	var local viewbuilder.PrimaryStoreReader
	primaryStoreServiceName := storage.View.PrimaryStoreServiceName
	scanServiceName := storage.View.PrimaryStoreScanServiceName
	if storageService != nil && storage.HasRole("primary") && strings.TrimSpace(storage.Primary.ServiceName) == "" {
		local = storageService.FactReader()
		primaryStoreServiceName = ""
		scanServiceName = ""
	}
	if primaryStoreServiceName == "" && local != nil {
		return local, nil
	}
	credentials, err := gatewayauth.ResolveCredentials(storage.View.StorageRPC.KeyID, storage.View.StorageRPC.HMACKeyFile)
	if err != nil {
		return nil, err
	}
	target := gatewayauth.ServiceGatewayTarget(storage.View.StorageRPC.GatewayTarget)
	nodeID := gatewayauth.ServiceGatewayNodeID()
	if nodeID == "" {
		nodeID = storage.View.StorageRPC.GatewayNodeID
	}
	return viewbuilder.NewPrimaryStoreReaderWithGateway(local, primaryStoreServiceName, scanServiceName, target, nodeID, credentials), nil
}

func shouldUseLocalPrimaryStoreReader(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("primary") && shouldStartViewBuilderRole(storage) && isMemoryRowsCommittedBus(storage.EventBus)
}

func isMemoryRowsCommittedBus(cfg storageconfig.StorageEventBus) bool {
	kind := strings.ToLower(strings.TrimSpace(cfg.Type))
	return kind == "" || kind == "memory"
}
