package main

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	storagebootstrap "github.com/mooyang-code/moox/modules/storage/internal/bootstrap"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	storagehealth "github.com/mooyang-code/moox/modules/storage/internal/health"
	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	metadataservice "github.com/mooyang-code/moox/modules/storage/internal/service/catalog"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	storageoutbox "github.com/mooyang-code/moox/modules/storage/internal/service/datanode/outbox"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcotel"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"
	_ "trpc.group/trpc-go/trpc-database/timer"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/server"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

func main() {
	trpclog.InstallServiceName("storage")
	var err error
	switch os.Getenv("MOOX_STORAGE_ROLE") {
	case "", "node":
		err = runDataNodeRole()
	case "primary":
		err = runPrimaryRole()
	case "view":
		err = runViewRole()
	default:
		err = fmt.Errorf("unknown storage role %q", os.Getenv("MOOX_STORAGE_ROLE"))
	}
	if err != nil {
		log.Fatal(err)
	}
}

func runPrimaryRole() error {
	root := os.Getenv("MOOX_STORAGE_HOME")
	if root == "" {
		root = "./var/storage"
	}
	metadataPath := os.Getenv("MOOX_STORAGE_METADATA_PATH")
	if metadataPath == "" {
		metadataPath = filepath.Join(root, "metadata", "storage_metadata.db")
	}
	meta, err := metasqlite.Open(trpc.BackgroundContext(), metasqlite.Options{Path: metadataPath})
	if err != nil {
		return err
	}
	defer meta.Close()
	if err := meta.ValidateSchemaVersion(trpc.BackgroundContext()); err != nil {
		return fmt.Errorf("metadata schema validation failed: %w", err)
	}
	cached, err := metacache.New(trpc.BackgroundContext(), meta, metacache.Options{})
	if err != nil {
		return fmt.Errorf("open metadata cache: %w", err)
	}
	defer cached.Close()
	secret := os.Getenv("MOOX_STORAGE_NODE_AUTH_SECRET")
	if secret == "" {
		return errors.New("MOOX_STORAGE_NODE_AUTH_SECRET is required for primary role")
	}
	primarySecret := os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	if primarySecret == "" {
		return errors.New("MOOX_STORAGE_PRIMARY_AUTH_SECRET is required for primary role")
	}
	viewSecret := os.Getenv("MOOX_STORAGE_VIEW_AUTH_SECRET")
	if viewSecret == "" {
		return errors.New("MOOX_STORAGE_VIEW_AUTH_SECRET is required for primary role")
	}
	resolver := newDataNodeResolver(cached.RequestSnapshot, func(target string) pb.DataNodeRuntimeService {
		opts := []client.Option{client.WithTarget(target), client.WithNetwork("tcp"), client.WithProtocol("trpc")}
		return &dataNodeProxyAdapter{
			proxy:        pb.NewDataNodeRuntimeClientProxy(opts...),
			markerProxy:  pb.NewDataNodeMarkerRuntimeClientProxy(opts...),
			historyProxy: pb.NewDataNodeHistoryRuntimeClientProxy(opts...),
		}
	})
	viewTarget := os.Getenv("MOOX_STORAGE_VIEW_TARGET")
	if viewTarget == "" {
		viewTarget = "ip://127.0.0.1:20103"
	}
	viewProxy := &dataViewProxyAdapter{
		proxy: pb.NewDataViewClientProxy(client.WithTarget(viewTarget), client.WithNetwork("tcp"), client.WithProtocol("trpc")),
		auth:  &pb.AuthInfo{AppId: "storage-primary", AppKey: datanode.ServiceAuthKey(viewSecret, "storage-primary")},
	}
	viewResolver := func(ctx context.Context, spaceID, datasetID string) (pb.DataViewService, string, error) {
		views, err := cached.ListViewsByDataset(ctx, spaceID, datasetID)
		if err != nil {
			return nil, "", err
		}
		for _, view := range views {
			if view != nil && view.GetPrimaryDatasetId() == datasetID && view.GetActiveIndexId() != "" {
				return viewProxy, view.GetViewId(), nil
			}
		}
		return nil, "", fmt.Errorf("dataset %s/%s has no active view", spaceID, datasetID)
	}
	datasetMetrics, err := observability.NewDatasetMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		return fmt.Errorf("initialize storage dataset metrics: %w", err)
	}
	resultDatasetResolver := func(ctx context.Context, spaceID, sourceViewID string) (string, error) {
		for pageNo := uint32(1); ; pageNo++ {
			datasets, page, err := cached.ListDatasets(ctx, metadata.DatasetQuery{SpaceID: spaceID, Page: &pb.Page{Page: pageNo, Size: 100}})
			if err != nil {
				return "", err
			}
			for _, dataset := range datasets {
				attrs := dataset.GetAttributes()
				if attrs["dataset_role"] == "factor_result" && attrs["source_view_id"] == sourceViewID {
					return dataset.GetDatasetId(), nil
				}
			}
			if page == nil || !page.GetHasMore() || len(datasets) == 0 {
				break
			}
		}
		return "", fmt.Errorf("factor result dataset for source view %s/%s is not found", spaceID, sourceViewID)
	}
	svc, err := primarystore.New(primarystore.Options{Resolver: resolver, View: viewResolver, Validator: primarystore.NewMetadataValidator(cached), Snapshot: cached.RequestSnapshot, ResultDataset: resultDatasetResolver, SyncPoints: meta, Authorizer: func(auth *pb.AuthInfo) error {
		if auth == nil || auth.GetAppId() == "" ||
			!hmac.Equal([]byte(strings.ToLower(auth.GetAppKey())), []byte(datanode.ServiceAuthKey(primarySecret, auth.GetAppId()))) {
			return errors.New("invalid primary auth")
		}
		return nil
	}, AuthSigner: func(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
		if auth == nil {
			return nil, errors.New("auth_info is required")
		}
		clone := proto.Clone(auth).(*pb.AuthInfo)
		clone.AppKey = datanode.ServiceAuthKey(secret, clone.GetAppId())
		return clone, nil
	}, DatasetMetrics: datasetMetrics})
	if err != nil {
		return err
	}
	metadataSvc, err := metadataservice.NewMetadataService(meta, cached, metadataservice.Options{AuthSecret: secret, OperatorAuthSecret: primarySecret})
	if err != nil {
		return err
	}
	cleanupCtx, stopCleanup := context.WithCancel(trpc.BackgroundContext())
	defer stopCleanup()
	cleanupAuth := &pb.AuthInfo{AppId: "storage-primary", AppKey: datanode.ServiceAuthKey(secret, "storage-primary")}
	go runCleanupLoop(cleanupCtx, cached, resolver, cleanupAuth, time.Hour)
	go runViewPeriodCleanupLoop(cleanupCtx, meta, time.Hour, storageViewPeriodRetention())
	s := trpc.NewServer()
	for _, name := range []string{"trpc.moox.storage.PrimaryStore", "trpc.moox.storage.PrimaryStore.trpc", "trpc.moox.storage.PrimaryStore.http"} {
		if listener := s.Service(name); listener != nil {
			pb.RegisterPrimaryStoreService(listener, svc)
		}
	}
	for _, name := range []string{"trpc.moox.storage.Metadata", "trpc.moox.storage.Metadata.trpc", "trpc.moox.storage.Metadata.http"} {
		if listener := s.Service(name); listener != nil {
			pb.RegisterMetadataService(listener, metadataSvc)
		}
	}
	if err := storagebootstrap.RegisterMetricsReporter(s, "primary"); err != nil {
		return err
	}
	if err := registerRoleHealth(s, "storage-primary"); err != nil {
		return err
	}
	return s.Serve()
}

type datasetReader interface {
	ListDatasets(context.Context, metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error)
}

func runCleanupLoop(ctx context.Context, reader datasetReader, resolver primarystore.NodeResolver, auth *pb.AuthInfo, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	if err := cleanupDatasets(ctx, reader, resolver, auth, time.Now().UTC()); err != nil {
		log.Printf("storage cleanup initial run failed: %v", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := cleanupDatasets(ctx, reader, resolver, auth, now.UTC()); err != nil {
				log.Printf("storage cleanup run failed: %v", err)
			}
		}
	}
}

func runViewPeriodCleanupLoop(ctx context.Context, store metadata.ViewPeriodStateStore, interval, retention time.Duration) {
	if store == nil {
		return
	}
	if interval <= 0 {
		interval = time.Hour
	}
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	cleanup := func() {
		before := time.Now().UTC().Add(-retention)
		if _, err := store.DeleteViewPeriodDatasetStatesBefore(ctx, before); err != nil {
			log.Printf("storage View period state cleanup failed: %v", err)
		}
		if _, err := store.DeleteViewSyncPointsBefore(ctx, before); err != nil {
			log.Printf("storage View sync point cleanup failed: %v", err)
		}
	}
	cleanup()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

func storageViewPeriodRetention() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("MOOX_STORAGE_VIEW_PERIOD_RETENTION")); raw != "" {
		if retention, err := time.ParseDuration(raw); err == nil && retention > 0 {
			return retention
		}
		log.Printf("invalid MOOX_STORAGE_VIEW_PERIOD_RETENTION=%q; using 168h", raw)
	}
	return 7 * 24 * time.Hour
}

func cleanupDatasets(ctx context.Context, reader datasetReader, resolver primarystore.NodeResolver, auth *pb.AuthInfo, now time.Time) error {
	var result error
	for pageNo := uint32(1); ; pageNo++ {
		datasets, page, err := reader.ListDatasets(ctx, metadata.DatasetQuery{DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Page: &pb.Page{Page: pageNo, Size: 1000}})
		if err != nil {
			return err
		}
		for _, dataset := range datasets {
			if dataset == nil || (dataset.GetStatus() != "" && dataset.GetStatus() != "active") || dataset.GetKeepDuration() == "0" {
				continue
			}
			keep, err := time.ParseDuration(dataset.GetKeepDuration())
			if err != nil || keep <= 0 {
				result = errors.Join(result, fmt.Errorf("dataset %s/%s has invalid keep_duration %q", dataset.GetSpaceId(), dataset.GetDatasetId(), dataset.GetKeepDuration()))
				continue
			}
			node, err := resolver(ctx, dataset.GetSpaceId(), dataset.GetDatasetId())
			if err != nil {
				result = errors.Join(result, err)
				continue
			}
			before := now.UTC().Add(-keep).Truncate(cleanupBucketDuration()).Format("2006-01-02T15:04:05.000000000Z")
			rsp, err := node.CleanupExpiredBuckets(ctx, &pb.CleanupExpiredBucketsReq{
				AuthInfo: auth, SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId(), BeforeBucketStart: before,
			})
			if err != nil {
				result = errors.Join(result, err)
				continue
			}
			if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
				result = errors.Join(result, errors.New(rsp.GetRetInfo().GetMsg()))
			}
		}
		if page == nil || !page.GetHasMore() || len(datasets) == 0 {
			return result
		}
	}
}

func cleanupBucketDuration() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("MOOX_STORAGE_BUCKET_DURATION")); raw != "" {
		if duration, err := time.ParseDuration(raw); err == nil && duration > 0 {
			return duration
		}
		log.Printf("invalid MOOX_STORAGE_BUCKET_DURATION=%q; using 24h", raw)
	}
	return 24 * time.Hour
}

func runViewRole() error {
	root := os.Getenv("MOOX_STORAGE_HOME")
	if root == "" {
		root = "./var/storage"
	}
	viewSecret := os.Getenv("MOOX_STORAGE_VIEW_AUTH_SECRET")
	if viewSecret == "" {
		return errors.New("MOOX_STORAGE_VIEW_AUTH_SECRET is required for view role")
	}
	svc, err := viewservice.New(filepath.Join(root, "view-indexes"), viewSecret)
	if err != nil {
		return err
	}
	if !svc.HasEngine("duckdb") {
		return errors.New("storage-view requires a CGO-enabled DuckDB engine")
	}
	rawURL := os.Getenv("MOOX_STORAGE_EVENTBUS_URL")
	if rawURL == "" {
		return errors.New("MOOX_STORAGE_EVENTBUS_URL is required for view role")
	}
	metadataTarget := envOrDefault("MOOX_STORAGE_METADATA_TARGET", "ip://127.0.0.1:20200")
	metadataNetwork := envOrDefault("MOOX_STORAGE_METADATA_NETWORK", "tcp")
	metadataProtocol := envOrDefault("MOOX_STORAGE_METADATA_PROTOCOL", "http")
	metadataProxy := pb.NewMetadataClientProxy(client.WithTarget(metadataTarget), client.WithNetwork(metadataNetwork), client.WithProtocol(metadataProtocol))
	primaryTarget := envOrDefault("MOOX_STORAGE_PRIMARY_TARGET", "ip://127.0.0.1:20201")
	primaryNetwork := envOrDefault("MOOX_STORAGE_PRIMARY_NETWORK", "tcp")
	primaryProtocol := envOrDefault("MOOX_STORAGE_PRIMARY_PROTOCOL", "http")
	primarySecret := os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	if primarySecret == "" {
		return errors.New("MOOX_STORAGE_PRIMARY_AUTH_SECRET is required for view role")
	}
	// Primary history pages are key-only and bounded by the rebuild lookback,
	// but a cold Pebble history index can still take longer than the ordinary
	// 30s request budget while it is being compacted. Keep this timeout scoped
	// to the view's rebuild reader; live PrimaryStore traffic keeps its normal
	// service timeout.
	primaryProxy := pb.NewPrimaryStoreClientProxy(
		client.WithTarget(primaryTarget),
		client.WithNetwork(primaryNetwork),
		client.WithProtocol(primaryProtocol),
		client.WithTimeout(5*time.Minute),
	)
	svc.SetPrimaryAuth(&pb.AuthInfo{AppId: "storage-view", AppKey: datanode.ServiceAuthKey(primarySecret, "storage-view")})
	svc.SetPrimaryReader(primaryProxy)
	rebuildCheckInterval, rebuildLookback, _, rebuildMaxPending, rebuildIdleChecks, maxPendingConfigured, idleChecksConfigured, err := storageViewRebuildSettings()
	if err != nil {
		return err
	}
	maintenancePolicy, err := storageViewMaintenancePolicy()
	if err != nil {
		return err
	}
	if policyInterval, parseErr := time.ParseDuration(strings.TrimSpace(maintenancePolicy.MaintenanceCheckInterval)); parseErr != nil || policyInterval < 30*time.Second {
		return fmt.Errorf("storage view maintenance_check_interval must be at least 30s")
	} else {
		rebuildCheckInterval = policyInterval
	}
	if maintenancePolicy.MaxViewFileBytes <= 0 || maintenancePolicy.MaxPeriodsPerSeries <= maintenancePolicy.RebuildLookbackPeriods {
		return errors.New("storage view maintenance policy limits are invalid")
	}
	maintenanceOptions := viewservice.MaintenanceOptions{
		Metadata:                    metadataProxy,
		Primary:                     primaryProxy,
		PrimaryRange:                primaryProxy,
		OwnerID:                     "storage-view",
		Interval:                    rebuildCheckInterval,
		RebuildLookback:             rebuildLookback,
		RebuildLookbackPeriods:      map[string]uint64{"default": maintenancePolicy.RebuildLookbackPeriods},
		Grace:                       time.Minute,
		MaxViewFileBytes:            maintenancePolicy.MaxViewFileBytes,
		RebuildMaxPending:           rebuildMaxPending,
		RebuildIdleChecks:           rebuildIdleChecks,
		RebuildMaxPendingConfigured: maxPendingConfigured,
		RebuildIdleChecksConfigured: idleChecksConfigured,
		Policy:                      maintenancePolicy,
		MaxPeriodsPerSeries:         maintenancePolicy.MaxPeriodsPerSeries,
	}
	// Bind the listeners before opening/validating historical indexes. View
	// restoration can still take time on a large deployment; keeping the
	// process reachable lets liveness probes observe the process while
	// readiness remains false until the consumer is bound.
	s := trpc.NewServer()
	indexListener := s.Service("trpc.moox.storage.ViewIndex")
	if indexListener == nil {
		return errors.New("ViewIndex listener is not configured")
	}
	pb.RegisterViewIndexService(indexListener, svc)
	for _, name := range []string{"trpc.moox.storage.DataView", "trpc.moox.storage.DataView.trpc", "trpc.moox.storage.DataView.http"} {
		if listener := s.Service(name); listener != nil {
			pb.RegisterDataViewService(listener, svc)
		}
	}
	if err := storagebootstrap.RegisterMetricsReporter(s, "view"); err != nil {
		return err
	}
	cleanupOptions := viewservice.RetiredIndexCleanupOptions{
		Metadata:           metadataProxy,
		MinUnreferencedAge: time.Minute,
	}
	if err := storagebootstrap.RegisterViewIndexCleanupTimer(s, func(ctx context.Context) error {
		return svc.CleanupRetiredIndexes(ctx, cleanupOptions)
	}); err != nil {
		return err
	}
	if err := registerRoleHealth(s, "storage-view"); err != nil {
		return err
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- s.Serve() }()
	restoreStarted := time.Now()
	if err := svc.RestoreActiveViews(trpc.BackgroundContext(), maintenanceOptions); err != nil {
		observability.DefaultViewMetrics.ObserveRestore(false, time.Since(restoreStarted))
		return err
	}
	observability.DefaultViewMetrics.ObserveRestore(true, time.Since(restoreStarted))
	eventConfig, err := storageEventBusConfig([]string{rawURL}, "storage-view")
	if err != nil {
		return err
	}
	eventClient, err := jetstream.Connect(trpc.BackgroundContext(), eventConfig)
	if err != nil {
		return err
	}
	defer eventClient.Close()
	consumerOptions, err := storageViewConsumerOptions()
	if err != nil {
		return err
	}
	// Validate against a temporary expanded topology. Never mutate the static
	// startup options: the configured wildcard belongs to the dynamic
	// reconciler, while the durable static misc consumer must keep its stable
	// exact filter set across restarts.
	validationOptions := cloneViewConsumerOptions(consumerOptions)
	if err := validateStorageViewConsumerPartitions(trpc.BackgroundContext(), metadataProxy, &pb.AuthInfo{AppId: "storage-view", AppKey: datanode.ServiceAuthKey(primarySecret, "storage-view")}, &validationOptions); err != nil {
		return err
	}
	staticConsumerOptions := cloneViewConsumerOptions(consumerOptions)
	stripWildcardConsumerRoutes(&staticConsumerOptions)
	// The legacy misc durable previously held an inventory-expanded wildcard
	// filter. Reusing that durable with a newly discovered View would violate
	// JetStream's immutable filter contract. Static consumers therefore keep
	// only the exact, latency-sensitive partitions; the reconciler owns every
	// misc Dataset with a stable per-Dataset durable instead.
	stripMiscConsumerPartition(&staticConsumerOptions)
	dynamicConsumerOptions := cloneViewConsumerOptions(consumerOptions)
	stopConsumer, err := svc.StartEventConsumer(trpc.BackgroundContext(), eventClient, staticConsumerOptions)
	if err != nil {
		return err
	}
	defer stopConsumer()
	dynamicReconciler, err := svc.NewInventoryReconciler(viewservice.InventoryReconcilerOptions{
		Metadata: metadataProxy, Primary: primaryProxy, EventClient: eventClient, Consumer: dynamicConsumerOptions, Interval: 30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("create storage view dynamic inventory reconciler: %w", err)
	}
	// Bind the current inventory before starting the periodic reconciler. The
	// legacy catch-all durable is intentionally retained during this rollout;
	// its pending sequence is a rollback/replay safety net and must not be
	// deleted until operators have verified the per-Dataset consumers.
	if err := dynamicReconciler.Reconcile(trpc.BackgroundContext()); err != nil {
		return fmt.Errorf("initial storage view dynamic inventory reconcile: %w", err)
	}
	stopDynamicConsumer, err := dynamicReconciler.Start(trpc.BackgroundContext())
	if err != nil {
		return fmt.Errorf("start storage view dynamic inventory reconciler: %w", err)
	}
	defer stopDynamicConsumer()
	stopViewMaintainer, err := svc.StartViewMaintainerAsync(trpc.BackgroundContext(), maintenanceOptions)
	if err != nil {
		return err
	}
	defer stopViewMaintainer()
	return <-serveErr
}

func stripWildcardConsumerRoutes(options *viewservice.EventConsumerOptions) {
	if options == nil {
		return
	}
	for i := range options.PartitionConfigs {
		partition := &options.PartitionConfigs[i]
		routes := partition.DatasetRoutes[:0]
		for _, route := range partition.DatasetRoutes {
			if strings.TrimSpace(route.DatasetID) == "*" {
				continue
			}
			routes = append(routes, route)
		}
		partition.DatasetRoutes = routes
	}
}

func stripMiscConsumerPartition(options *viewservice.EventConsumerOptions) {
	if options == nil {
		return
	}
	partitions := options.PartitionConfigs[:0]
	for _, partition := range options.PartitionConfigs {
		if strings.TrimSpace(partition.Consumer) == events.StorageViewMiscConsumer {
			continue
		}
		partitions = append(partitions, partition)
	}
	options.PartitionConfigs = partitions
}

func cloneViewConsumerOptions(options viewservice.EventConsumerOptions) viewservice.EventConsumerOptions {
	clone := options
	clone.FilterSubjects = append([]string(nil), options.FilterSubjects...)
	clone.DatasetRoutes = append([]viewservice.DatasetRoute(nil), options.DatasetRoutes...)
	clone.AllowedDatasetSpaces = append([]string(nil), options.AllowedDatasetSpaces...)
	clone.PartitionConfigs = make([]viewservice.EventConsumerOptions, len(options.PartitionConfigs))
	for i, partition := range options.PartitionConfigs {
		clone.PartitionConfigs[i] = partition
		clone.PartitionConfigs[i].FilterSubjects = append([]string(nil), partition.FilterSubjects...)
		clone.PartitionConfigs[i].DatasetRoutes = append([]viewservice.DatasetRoute(nil), partition.DatasetRoutes...)
		clone.PartitionConfigs[i].PartitionConfigs = nil
	}
	return clone
}

func storageViewMaintenancePolicy() (storageconfig.ViewMaintenancePolicy, error) {
	policy := storageconfig.ViewMaintenancePolicy{
		MaintenanceCheckInterval: "1m",
		RebuildLookbackPeriods:   1000,
		MaxPeriodsPerSeries:      2000,
		MaxViewFileBytes:         1 << 30,
	}
	path := strings.TrimSpace(os.Getenv("MOOX_STORAGE_CONFIG"))
	if path == "" {
		return policy, nil
	}
	var runtimeConfig storageconfig.RuntimeConfig
	loader := storageconfig.NewConfigLoader(filepath.Dir(path))
	if err := loader.LoadConfigWithDefaults(filepath.Base(path), &runtimeConfig, runtimeConfig.ApplyDefaults); err != nil {
		return policy, fmt.Errorf("load storage view maintenance config: %w", err)
	}
	policyPath := strings.TrimSpace(runtimeConfig.Storage.View.MaintenancePolicyFile)
	if policyPath == "" {
		return policy, nil
	}
	if !filepath.IsAbs(policyPath) {
		// MOOX_STORAGE_CONFIG points at <package>/config/*.yaml while the
		// deployed policy path is expressed from the package root.
		packageRoot := filepath.Dir(filepath.Dir(path))
		policyPath = filepath.Join(packageRoot, policyPath)
	}
	return storageconfig.LoadViewMaintenancePolicy(policyPath)
}

func validateStorageViewConsumerPartitions(ctx context.Context, metadataProxy pb.MetadataClientProxy, auth *pb.AuthInfo, options *viewservice.EventConsumerOptions) error {
	if options == nil {
		return errors.New("storage view consumer options are required")
	}
	if metadataProxy == nil {
		return errors.New("storage view metadata client is required for consumer partition validation")
	}
	managed := make([]storageconfig.StorageViewConsumerDataset, 0)
	seen := make(map[string]struct{})
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := metadataProxy.ListViews(ctx, &pb.ListViewsReq{AuthInfo: auth, Status: "active", Page: &pb.Page{Page: pageNo, Size: 100}})
		if err != nil {
			return fmt.Errorf("list managed views for consumer partition validation: %w", err)
		}
		if rsp == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			if rsp == nil {
				return errors.New("list managed views for consumer partition validation returned nil response")
			}
			return fmt.Errorf("list managed views for consumer partition validation: %s", rsp.GetRetInfo().GetMsg())
		}
		for _, view := range rsp.GetViews() {
			if view == nil {
				continue
			}
			ids := append([]string{}, view.GetDatasetIds()...)
			if view.GetPrimaryDatasetId() != "" {
				ids = append(ids, view.GetPrimaryDatasetId())
			}
			for _, column := range view.GetColumns() {
				origin := strings.TrimSpace(column.GetOriginId())
				if datasetID, _, ok := strings.Cut(origin, "."); ok && datasetID != "" {
					ids = append(ids, datasetID)
				}
			}
			for _, datasetID := range ids {
				datasetID = strings.TrimSpace(datasetID)
				if datasetID == "" {
					continue
				}
				key := view.GetSpaceId() + "\x00" + datasetID
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				managed = append(managed, storageconfig.StorageViewConsumerDataset{SpaceID: view.GetSpaceId(), DatasetID: datasetID})
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetViews()) == 0 {
			break
		}
	}
	if err := expandStorageViewConsumerRoutes(options, managed); err != nil {
		return err
	}
	partitions := storageconfig.StorageView{ConsumerPartitions: nil}
	// Options have already been rendered from the normalized config. Rebuild a
	// minimal topology from those routes for validation without duplicating the
	// YAML loader in this process.
	for _, partition := range options.PartitionConfigs {
		routes := make([]storageconfig.StorageViewConsumerRoute, 0, len(partition.DatasetRoutes))
		for _, route := range partition.DatasetRoutes {
			routes = append(routes, storageconfig.StorageViewConsumerRoute{SpaceID: route.SpaceID, DatasetIDs: []string{route.DatasetID}})
		}
		partitions.ConsumerPartitions = append(partitions.ConsumerPartitions, storageconfig.StorageViewConsumerPartition{
			ID: partition.PartitionID, Durable: partition.Consumer, Routes: routes,
			AckWaitMS: partition.AckWaitMS, FetchBatch: partition.FetchBatch, MaxWorkers: partition.MaxWorkers,
			MaxAckPending: partition.MaxAckPending, Ordering: partition.Ordering, DeliverPolicy: partition.DeliverPolicy,
			MaxRetryAttempts: partition.MaxRetryAttempts,
		})
	}
	// A fresh installation can legitimately have no active View metadata yet.
	// In that state the configured routes are the allow-list for future Views;
	// enforce the exact inventory match only once metadata has an inventory.
	var managedInventory []storageconfig.StorageViewConsumerDataset
	if len(managed) > 0 {
		managedInventory = managed
	}
	if err := partitions.ValidateConsumerPartitions(managedInventory); err != nil {
		return fmt.Errorf("storage view consumer partition validation failed: %w", err)
	}
	return nil
}

// expandStorageViewConsumerRoutes turns an explicit wildcard route into
// concrete metadata routes before JetStream subjects are rendered. Exact
// routes win, so the latency-sensitive Kline and Factor partitions can never
// be pulled into the catch-all "misc" partition.
func expandStorageViewConsumerRoutes(options *viewservice.EventConsumerOptions, managed []storageconfig.StorageViewConsumerDataset) error {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	eventFamilies := []events.Event{events.DatasetRowsUpserted, events.DatasetPeriodCollected, events.FactorPeriodComputed, events.DatasetSyncPoint}
	explicit := make(map[string]struct{})
	for _, partition := range options.PartitionConfigs {
		for _, route := range partition.DatasetRoutes {
			if route.DatasetID != "*" {
				explicit[route.SpaceID+"\x00"+route.DatasetID] = struct{}{}
			}
		}
	}
	for i := range options.PartitionConfigs {
		partition := &options.PartitionConfigs[i]
		routes := make([]viewservice.DatasetRoute, 0, len(partition.DatasetRoutes))
		for _, route := range partition.DatasetRoutes {
			if route.DatasetID != "*" {
				routes = append(routes, route)
				continue
			}
			for _, dataset := range managed {
				if dataset.SpaceID != route.SpaceID {
					continue
				}
				key := dataset.SpaceID + "\x00" + dataset.DatasetID
				if _, exists := explicit[key]; exists {
					continue
				}
				routes = append(routes, viewservice.DatasetRoute{SpaceID: dataset.SpaceID, DatasetID: dataset.DatasetID})
				for _, event := range eventFamilies {
					filter, renderErr := registry.RenderSubject(event, dataset.SpaceID, dataset.DatasetID)
					if renderErr != nil {
						return fmt.Errorf("render consumer partition %q filter: %w", partition.PartitionID, renderErr)
					}
					partition.FilterSubjects = appendUniqueString(partition.FilterSubjects, filter)
				}
			}
		}
		partition.DatasetRoutes = routes
	}
	return nil
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func storageViewConsumerOptions() (viewservice.EventConsumerOptions, error) {
	path := strings.TrimSpace(os.Getenv("MOOX_STORAGE_CONFIG"))
	if path == "" {
		var runtimeConfig storageconfig.RuntimeConfig
		runtimeConfig.ApplyDefaults()
		return buildStorageViewConsumerOptions(runtimeConfig)
	}
	var runtimeConfig storageconfig.RuntimeConfig
	loader := storageconfig.NewConfigLoader(filepath.Dir(path))
	if err := loader.LoadConfigWithDefaults(filepath.Base(path), &runtimeConfig, runtimeConfig.ApplyDefaults); err != nil {
		return viewservice.EventConsumerOptions{}, fmt.Errorf("load storage view consumer config: %w", err)
	}
	return buildStorageViewConsumerOptions(runtimeConfig)
}

func buildStorageViewConsumerOptions(runtimeConfig storageconfig.RuntimeConfig) (viewservice.EventConsumerOptions, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return viewservice.EventConsumerOptions{}, err
	}
	eventFamilies := []events.Event{
		events.DatasetRowsUpserted,
		events.DatasetPeriodCollected,
		events.FactorPeriodComputed,
		events.DatasetSyncPoint,
	}
	partitions := make([]viewservice.EventConsumerOptions, 0, len(runtimeConfig.Storage.View.ConsumerPartitions))
	allowedSpaces := make(map[string]struct{})
	for _, partition := range runtimeConfig.Storage.View.ConsumerPartitions {
		filters := make([]string, 0, len(partition.Datasets())*len(eventFamilies))
		for _, dataset := range partition.Datasets() {
			if dataset.DatasetID == "*" && dataset.SpaceID != "" {
				allowedSpaces[dataset.SpaceID] = struct{}{}
			}
			if dataset.DatasetID == "*" {
				continue
			}
			for _, event := range eventFamilies {
				filter, err := registry.RenderSubject(event, dataset.SpaceID, dataset.DatasetID)
				if err != nil {
					return viewservice.EventConsumerOptions{}, fmt.Errorf("render consumer partition %q filter: %w", partition.ID, err)
				}
				filters = append(filters, filter)
			}
		}
		partitions = append(partitions, viewservice.EventConsumerOptions{
			PartitionID:    partition.ID,
			FilterSubjects: filters,
			DatasetRoutes: func() []viewservice.DatasetRoute {
				routes := make([]viewservice.DatasetRoute, 0, len(partition.Datasets()))
				for _, dataset := range partition.Datasets() {
					routes = append(routes, viewservice.DatasetRoute{SpaceID: dataset.SpaceID, DatasetID: dataset.DatasetID})
				}
				return routes
			}(),
			Consumer:         partition.Durable,
			AckWaitMS:        partition.AckWaitMS,
			FetchBatch:       partition.FetchBatch,
			MaxWorkers:       partition.MaxWorkers,
			MaxAckPending:    partition.MaxAckPending,
			Ordering:         partition.Ordering,
			DeliverPolicy:    storageViewDeliverPolicy(partition.DeliverPolicy),
			MaxRetryAttempts: partition.MaxRetryAttempts,
		})
	}
	allowed := make([]string, 0, len(allowedSpaces))
	// Local/integration deployments may create short-lived spaces dynamically.
	// Keep the checked-in route allow-list closed by default, while permitting
	// an operator to opt in additional exact space IDs without editing YAML.
	for _, raw := range strings.Split(os.Getenv("MOOX_STORAGE_VIEW_ALLOWED_DATASET_SPACES"), ",") {
		if spaceID := strings.TrimSpace(raw); spaceID != "" {
			allowedSpaces[spaceID] = struct{}{}
		}
	}
	allowed = allowed[:0]
	for spaceID := range allowedSpaces {
		allowed = append(allowed, spaceID)
	}
	sort.Strings(allowed)
	return viewservice.EventConsumerOptions{PartitionConfigs: partitions, AllowedDatasetSpaces: allowed}, nil
}

func storageViewDeliverPolicy(configured string) string {
	if strings.TrimSpace(os.Getenv("MOOX_STORAGE_VIEW_REPLAY_PENDING")) == "1" {
		return "all"
	}
	return configured
}

func storageViewRebuildSettings() (time.Duration, time.Duration, int64, uint64, uint32, bool, bool, error) {
	const defaultInterval = time.Minute
	path := strings.TrimSpace(os.Getenv("MOOX_STORAGE_CONFIG"))
	if path == "" {
		return defaultInterval, 24 * time.Hour, 1 << 30, 32, 3, false, false, nil
	}
	var runtimeConfig storageconfig.RuntimeConfig
	loader := storageconfig.NewConfigLoader(filepath.Dir(path))
	if err := loader.LoadConfigWithDefaults(filepath.Base(path), &runtimeConfig, runtimeConfig.ApplyDefaults); err != nil {
		return 0, 0, 0, 0, 0, false, false, fmt.Errorf("load storage view rebuild config: %w", err)
	}
	interval, err := time.ParseDuration(strings.TrimSpace(runtimeConfig.Storage.View.MaintenanceCheckInterval))
	if err != nil || interval <= 0 {
		return 0, 0, 0, 0, 0, false, false, fmt.Errorf("storage view maintenance_check_interval must be a positive duration")
	}
	if interval < 30*time.Second {
		return 0, 0, 0, 0, 0, false, false, errors.New("storage view maintenance_check_interval must be at least 30s")
	}
	lookback, err := time.ParseDuration(strings.TrimSpace(runtimeConfig.Storage.View.RebuildLookback))
	if err != nil || lookback <= 0 {
		return 0, 0, 0, 0, 0, false, false, errors.New("storage view rebuild_lookback must be a positive duration")
	}
	if runtimeConfig.Storage.View.MaxViewFileBytes <= 0 {
		return 0, 0, 0, 0, 0, false, false, errors.New("storage view max_view_file_bytes must be positive")
	}
	maxPendingConfigured := runtimeConfig.Storage.View.HasRebuildMaxPendingSetting()
	idleChecksConfigured := runtimeConfig.Storage.View.HasRebuildIdleChecksSetting()
	if idleChecksConfigured && runtimeConfig.Storage.View.RebuildIdleChecks == 0 {
		return 0, 0, 0, 0, 0, false, false, errors.New("storage view rebuild_idle_checks must be greater than zero")
	}
	return interval, lookback, runtimeConfig.Storage.View.MaxViewFileBytes, runtimeConfig.Storage.View.RebuildMaxPending, runtimeConfig.Storage.View.RebuildIdleChecks,
		maxPendingConfigured, idleChecksConfigured, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

type dataNodeProxyKey struct {
	NodeID        string
	ServiceTarget string
}

func newDataNodeResolver(snapshotProvider func() metadata.RequestSnapshot, newProxy func(string) pb.DataNodeRuntimeService) primarystore.NodeResolver {
	if newProxy == nil {
		newProxy = func(target string) pb.DataNodeRuntimeService {
			opts := []client.Option{client.WithTarget(target), client.WithNetwork("tcp"), client.WithProtocol("trpc")}
			return &dataNodeProxyAdapter{
				proxy:        pb.NewDataNodeRuntimeClientProxy(opts...),
				markerProxy:  pb.NewDataNodeMarkerRuntimeClientProxy(opts...),
				historyProxy: pb.NewDataNodeHistoryRuntimeClientProxy(opts...),
			}
		}
	}
	proxies := make(map[dataNodeProxyKey]pb.DataNodeRuntimeService)
	var proxiesMu sync.Mutex
	return func(ctx context.Context, spaceID, datasetID string) (pb.DataNodeRuntimeService, error) {
		snapshot := metadata.RequestSnapshotFromContext(ctx)
		if snapshot == nil && snapshotProvider != nil {
			snapshot = snapshotProvider()
		}
		nodeID, target, err := resolveDataNodeFromSnapshot(snapshot, spaceID, datasetID)
		if err != nil {
			return nil, err
		}
		key := dataNodeProxyKey{NodeID: nodeID, ServiceTarget: target}
		proxiesMu.Lock()
		defer proxiesMu.Unlock()
		if proxy := proxies[key]; proxy != nil {
			return proxy, nil
		}
		for existing := range proxies {
			if existing.NodeID == nodeID {
				delete(proxies, existing)
			}
		}
		proxy := newProxy(target)
		if proxy == nil {
			return nil, errors.New("data node proxy is unavailable")
		}
		proxies[key] = proxy
		return proxy, nil
	}
}

func resolveDataNodeFromSnapshot(snapshot metadata.RequestSnapshot, spaceID, datasetID string) (string, string, error) {
	if snapshot == nil {
		return "", "", errors.New("metadata cache snapshot is unavailable")
	}
	dataset, ok := snapshot.GetDataset(spaceID, datasetID)
	if !ok || dataset == nil {
		return "", "", fmt.Errorf("dataset %s/%s is not found", spaceID, datasetID)
	}
	if dataset.GetStatus() != "active" {
		return "", "", fmt.Errorf("dataset %s/%s is not active", spaceID, datasetID)
	}
	nodeID := strings.TrimSpace(dataset.GetDataNodeId())
	if nodeID == "" {
		return "", "", fmt.Errorf("dataset %s/%s has no data_node_id", spaceID, datasetID)
	}
	node, ok := snapshot.GetDataNode(nodeID)
	if !ok || node == nil {
		return "", "", fmt.Errorf("data node %q is not found", nodeID)
	}
	if node.GetNodeId() != nodeID || node.GetStatus() != "active" {
		return "", "", fmt.Errorf("data node %q is not active", nodeID)
	}
	target, err := normalizeServiceTarget(node.GetServiceTarget())
	if err != nil {
		return "", "", fmt.Errorf("data node %q has invalid service_target: %w", nodeID, err)
	}
	return nodeID, target, nil
}

func normalizeServiceTarget(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "ip" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("service_target must be an ip://host:port address")
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil || host == "" {
		return "", errors.New("service_target must include host and port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("service_target port is invalid")
	}
	return "ip://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
}

type dataNodeProxyAdapter struct {
	proxy        pb.DataNodeRuntimeClientProxy
	markerProxy  pb.DataNodeMarkerRuntimeClientProxy
	historyProxy pb.DataNodeHistoryRuntimeClientProxy
}

func (a *dataNodeProxyAdapter) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if a == nil || a.historyProxy == nil {
		return nil, errors.New("DataNode history runtime is unavailable")
	}
	return a.historyProxy.ReadTimeSeriesRows(ctx, req)
}

type dataViewProxyAdapter struct {
	proxy pb.DataViewClientProxy
	auth  *pb.AuthInfo
}

func (a *dataNodeProxyAdapter) UpsertFields(ctx context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	return a.proxy.UpsertFields(ctx, req)
}
func (a *dataNodeProxyAdapter) ReadFields(ctx context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	return a.proxy.ReadFields(ctx, req)
}
func (a *dataNodeProxyAdapter) GetNodeState(ctx context.Context, req *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	return a.proxy.GetNodeState(ctx, req)
}
func (a *dataNodeProxyAdapter) CleanupExpiredBuckets(ctx context.Context, req *pb.CleanupExpiredBucketsReq) (*pb.CleanupExpiredBucketsRsp, error) {
	return a.proxy.CleanupExpiredBuckets(ctx, req)
}
func (a *dataNodeProxyAdapter) AppendDatasetPeriodCollected(ctx context.Context, req *pb.AppendDatasetPeriodCollectedReq) (*pb.AppendDatasetPeriodCollectedRsp, error) {
	return a.markerProxy.AppendDatasetPeriodCollected(ctx, req)
}
func (a *dataNodeProxyAdapter) AppendFactorPeriodComputed(ctx context.Context, req *pb.AppendFactorPeriodComputedReq) (*pb.AppendFactorPeriodComputedRsp, error) {
	return a.markerProxy.AppendFactorPeriodComputed(ctx, req)
}
func (a *dataNodeProxyAdapter) AppendDatasetSyncPointMarker(ctx context.Context, req *pb.AppendDatasetSyncPointMarkerReq) (*pb.AppendDatasetSyncPointMarkerRsp, error) {
	return a.markerProxy.AppendDatasetSyncPointMarker(ctx, req)
}
func (a *dataNodeProxyAdapter) GetFactorPeriodComputedMarker(ctx context.Context, req *pb.GetFactorPeriodComputedMarkerReq) (*pb.GetFactorPeriodComputedMarkerRsp, error) {
	return a.markerProxy.GetFactorPeriodComputedMarker(ctx, req)
}
func (a *dataViewProxyAdapter) QueryTimeSeriesRows(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
	clone := proto.Clone(req).(*pb.QueryTimeSeriesRowsReq)
	clone.AuthInfo = proto.Clone(a.auth).(*pb.AuthInfo)
	return a.proxy.QueryTimeSeriesRows(ctx, clone)
}

func (a *dataViewProxyAdapter) SearchRecordRows(ctx context.Context, req *pb.SearchRecordRowsReq) (*pb.SearchRecordRowsRsp, error) {
	clone := proto.Clone(req).(*pb.SearchRecordRowsReq)
	clone.AuthInfo = proto.Clone(a.auth).(*pb.AuthInfo)
	return a.proxy.SearchRecordRows(ctx, clone)
}

// This small role entrypoint intentionally keeps the DataNode process
// independent from PrimaryStore and View. Deployment selects the role through
// its tRPC listener configuration; one process owns exactly one Pebble node.
func runDataNodeRole() error {
	root := os.Getenv("MOOX_STORAGE_HOME")
	if root == "" {
		root = "./var/storage"
	}
	nodeID := os.Getenv("MOOX_STORAGE_NODE_ID")
	if nodeID == "" {
		nodeID = "storage-node-0"
	}
	authSecret := os.Getenv("MOOX_STORAGE_NODE_AUTH_SECRET")
	rawURL := os.Getenv("MOOX_STORAGE_EVENTBUS_URL")
	if rawURL == "" {
		return errors.New("MOOX_STORAGE_EVENTBUS_URL is required for node role")
	}
	eventConfig, err := storageEventBusConfig([]string{rawURL}, "storage-node")
	if err != nil {
		return err
	}
	svc, err := datanode.NewService(datanode.Options{
		NodeID: nodeID, AuthSecret: authSecret,
		Pebble: pebble.Options{
			NodeID: nodeID, Path: filepath.Join(root, "pebble", nodeID),
			MaxEventBytes: eventConfig.MaxPayloadBytes(), BucketDuration: cleanupBucketDuration(),
		},
	})
	if err != nil {
		return err
	}
	defer svc.Close()
	client, err := jetstream.Connect(trpc.BackgroundContext(), eventConfig)
	if err != nil {
		return err
	}
	defer client.Close()
	publisher := storageoutbox.NewJetStreamPublisher(client)
	relay, err := storageoutbox.NewRelay(svc.Store(), publisher, storageoutbox.RelayOptions{})
	if err != nil {
		return err
	}
	relay.Start(trpc.BackgroundContext())
	defer relay.Close()
	s := trpc.NewServer()
	listener := s.Service("trpc.moox.storage.DataNodeRuntime")
	if listener == nil {
		return errors.New("DataNode listener is not configured")
	}
	pb.RegisterDataNodeRuntimeService(listener, svc)
	pb.RegisterDataNodeMarkerRuntimeService(listener, svc)
	pb.RegisterDataNodeHistoryRuntimeService(listener, svc)
	if err := storagebootstrap.RegisterMetricsReporter(s, "node"); err != nil {
		return err
	}
	if err := registerRoleHealth(s, "storage-node"); err != nil {
		return err
	}
	return s.Serve()
}

const storageEventBusReconnectBufferBytes = 32 * 1024 * 1024

func storageEventBusConfig(urls []string, name string) (jetstream.Config, error) {
	cfg := jetstream.ConfigFromEnv(urls, name)
	reconnectBuffer := strings.TrimSpace(os.Getenv("MOOX_EVENTBUS_RECONNECT_BUFFER_BYTES"))
	if reconnectBuffer == "" {
		cfg.ReconnectBufferBytes = storageEventBusReconnectBufferBytes
	} else {
		value, err := strconv.Atoi(reconnectBuffer)
		if err != nil || value < 0 {
			return jetstream.Config{}, fmt.Errorf("MOOX_EVENTBUS_RECONNECT_BUFFER_BYTES must be a non-negative integer")
		}
		cfg.ReconnectBufferBytes = value
	}
	path, err := storageEventBusCredentialFile()
	if err != nil {
		return jetstream.Config{}, err
	}
	if path == "" {
		return cfg, nil
	}
	if cfg.Credentials != "" || cfg.Username != "" {
		return jetstream.Config{}, errors.New("storage eventbus credential file conflicts with EventBus credential environment")
	}
	if err := cfg.ApplyCredentialFile(path); err != nil {
		return jetstream.Config{}, fmt.Errorf("storage eventbus credential: %w", err)
	}
	// The role-specific URL is an explicit deployment override. Credential
	// files may carry a default/public URL for other roles, but storage roles
	// on the EventBus host must be able to select the local loopback endpoint
	// without editing shared credentials.
	cfg.URLs = append([]string(nil), urls...)
	return cfg, nil
}

func storageEventBusCredentialFile() (string, error) {
	if path := strings.TrimSpace(os.Getenv("MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE")); path != "" {
		return jetstream.ExpandCredentialPath(path), nil
	}
	configPath := strings.TrimSpace(os.Getenv("MOOX_STORAGE_CONFIG"))
	if configPath == "" {
		return "", nil
	}
	var runtimeConfig storageconfig.RuntimeConfig
	loader := storageconfig.NewConfigLoader(filepath.Dir(configPath))
	if err := loader.LoadConfig(filepath.Base(configPath), &runtimeConfig); err != nil {
		return "", fmt.Errorf("load storage eventbus config: %w", err)
	}
	return jetstream.ExpandCredentialPath(strings.TrimSpace(runtimeConfig.Storage.EventBus.CredentialFile)), nil
}

func registerRoleHealth(s *server.Server, instance string) error {
	if s == nil {
		return errors.New("storage health server is unavailable")
	}
	state := storagehealth.New("storage", instance, "", "")
	state.SnapshotFunc = storagehealth.SnapshotForRole(instance, observability.DefaultViewMetrics)
	state.SetReady(true)
	if err := storagehealth.Register(s.Service("trpc.moox.storage.Health"), state); err != nil {
		return fmt.Errorf("register storage health: %w", err)
	}
	return nil
}
