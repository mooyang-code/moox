package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	storagebootstrap "github.com/mooyang-code/moox/modules/storage/internal/bootstrap"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	storagehealth "github.com/mooyang-code/moox/modules/storage/internal/health"
	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	metadataservice "github.com/mooyang-code/moox/modules/storage/internal/service/catalog"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	storagerelay "github.com/mooyang-code/moox/modules/storage/internal/service/datanode/relay"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	_ "github.com/mooyang-code/moox/packages/clsreporter/logcls"
	"github.com/mooyang-code/moox/packages/events"
	// 注册与 catalog.yaml max_body_bytes(32 MiB) 对齐的 tRPC 帧上限（64 MiB）及有界 zstd 字典压缩。
	_ "github.com/mooyang-code/moox/packages/gatewayauth/nativewire"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/prometheus/client_golang/prometheus"
	_ "trpc.group/trpc-go/trpc-database/timer"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
	tlog "trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

func main() {
	var err error
	switch os.Getenv("MOOX_STORAGE_ROLE") {
	case "primary":
		err = runPrimaryRole()
	case "view":
		err = runViewRole()
	default:
		err = fmt.Errorf("未知的 Storage 角色 %q，只支持 primary 和 view", os.Getenv("MOOX_STORAGE_ROLE"))
	}
	if err != nil {
		// Fatalf 写完会先把日志队列发出去再退出，这最后一条错误才进得了重要日志。
		tlog.Fatalf("storage exited: %v", err)
	}
}

// pebbleDirName 是主服务 Pebble 库在数据目录下的子目录名。库的实例标识另由 Pebble 库首次打开时随机分配，见 pebble.Open。
const pebbleDirName = "storage-primary"

func runPrimaryRole() error {
	loadedCfg, err := loadRuntimeStorageConfig()
	if err != nil {
		return err
	}
	metadataPath := loadedCfg.MetadataDBPath(os.Getenv("MOOX_STORAGE_METADATA_PATH"))
	rawURL := os.Getenv("MOOX_STORAGE_EVENTBUS_URL")
	if rawURL == "" {
		return errors.New("MOOX_STORAGE_EVENTBUS_URL is required for primary role")
	}
	eventConfig, err := storageEventBusConfig(loadedCfg, []string{rawURL}, "storage-primary")
	if err != nil {
		return err
	}
	policy, err := loadedCfg.LoadPolicy()
	if err != nil {
		return err
	}
	meta, err := metasqlite.Open(trpc.BackgroundContext(), metasqlite.Options{Path: metadataPath})
	if err != nil {
		return err
	}
	defer meta.Close()
	if err := meta.ValidateSchemaVersion(trpc.BackgroundContext()); err != nil {
		return fmt.Errorf("metadata schema validation failed: %w", err)
	}
	cached, err := metacache.NewDatasetCache(trpc.BackgroundContext(), meta)
	if err != nil {
		return fmt.Errorf("open dataset cache: %w", err)
	}
	defer cached.Close()
	store, err := pebble.Open(pebble.Options{
		Path:          filepath.Join(loadedCfg.Storage.Devices.PebblePath, pebbleDirName),
		MaxEventBytes: eventConfig.MaxPayloadBytes(),
		// 写入拿到数据集读锁后按最新的 DatasetCache 无锁确认数据集仍在，见 Metadata.DeleteDataset。
		DatasetExists: cached.HasDataset,
	})
	if err != nil {
		return err
	}
	defer store.Close()
	if err := observability.RegisterPebbleDiskUsage(prometheus.DefaultRegisterer, store.DiskSpaceUsage); err != nil {
		return err
	}
	datasetMetrics, err := observability.NewDatasetMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		return fmt.Errorf("initialize storage dataset metrics: %w", err)
	}
	attention, err := observability.NewPrimaryAttention(prometheus.DefaultRegisterer)
	if err != nil {
		return fmt.Errorf("initialize storage-primary attention: %w", err)
	}
	deadlineRejections := observability.NewDeadlineRejections(attention)
	attention.AddSource("outbox", storagerelay.OutboxAttention(observability.DefaultViewMetrics))
	svc, err := primarystore.New(primarystore.Options{
		Store: store, Validator: primarystore.NewMetadataValidator(cached), Snapshot: cached.RequestSnapshot,
		DatasetMetrics: datasetMetrics, DeadlineRejections: deadlineRejections,
	})
	if err != nil {
		return err
	}
	metadataSvc, err := metadataservice.NewMetadataService(meta, cached, metadataservice.Options{Rows: store, Retention: policy.Retention})
	if err != nil {
		return err
	}
	cleanupMetrics, err := observability.NewCleanupMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		return err
	}
	backgroundCtx, stopBackground := context.WithCancel(trpc.BackgroundContext())
	defer stopBackground()
	go runCleanupLoop(backgroundCtx, datasetCleanup{reader: cached, store: store, retention: policy.Retention, metrics: cleanupMetrics}, time.Hour)
	// EventBus 不可用时主服务照常启动：转发协程照常运行，连上 EventBus 后从游标接着转发积压的周期完成事件。
	stopRelay, err := storagerelay.StartWithEventBus(backgroundCtx, store, eventConfig)
	if err != nil {
		return err
	}
	defer stopRelay()
	s := trpc.NewServer()
	// 要在 NewServer 之后装：NewServer 读配置时会换掉默认的日志对象。
	trpclog.InstallServiceName("storage")
	if listener := s.Service("trpc.moox.storage.PrimaryStore.trpc"); listener != nil {
		pb.RegisterPrimaryStoreService(listener, svc)
	}
	if listener := s.Service("trpc.moox.storage.Metadata.trpc"); listener != nil {
		pb.RegisterMetadataService(listener, metadataSvc)
	}
	healthState, err := registerRoleHealth(s, "storage-primary")
	if err != nil {
		return err
	}
	if err := storagebootstrap.RegisterMetricsReporter(s, "primary", healthz.Readiness(healthState.Snapshot)); err != nil {
		return err
	}
	return s.Serve()
}

type datasetReader interface {
	ListDatasets(context.Context, metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error)
}

// bucketCleaner 是主服务 Pebble 库的保留期清理：按数据集删早于给定日桶的行、账本与完成标记，
// 另按变更日志的保留期（默认 7 天）删变更日志。
type bucketCleaner interface {
	CleanupExpiredBuckets(ctx context.Context, spaceID, datasetID string, beforeBucket time.Time) (uint64, error)
	CleanupChangeLog(ctx context.Context, cutoff time.Time) (int, error)
	ChangeLogRetention() time.Duration
}

func runCleanupLoop(ctx context.Context, cleanup datasetCleanup, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	if err := cleanup.run(ctx, time.Now().UTC()); err != nil {
		tlog.Errorf("storage cleanup initial run failed: %v", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := cleanup.run(ctx, now.UTC()); err != nil {
				tlog.Errorf("storage cleanup run failed: %v", err)
			}
		}
	}
}

// datasetCleanup deletes the rows of each active time-series Dataset that
// are older than its retention in the Storage policy.
type datasetCleanup struct {
	reader    datasetReader
	store     bucketCleaner
	retention storagepolicy.Retention
	metrics   *observability.CleanupMetrics
}

func (c datasetCleanup) run(ctx context.Context, now time.Time) error {
	var result error
	// 变更日志按创建时间清理，与各数据集的保留期无关。
	if _, err := c.store.CleanupChangeLog(ctx, now.Add(-c.store.ChangeLogRetention())); err != nil {
		result = errors.Join(result, fmt.Errorf("清理变更日志: %w", err))
	}
	for pageNo := uint32(1); ; pageNo++ {
		datasets, page, err := c.reader.ListDatasets(ctx, metadata.DatasetQuery{DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Page: &pb.Page{Page: pageNo, Size: 1000}})
		if err != nil {
			return errors.Join(result, err)
		}
		for _, dataset := range datasets {
			if dataset == nil || (dataset.GetStatus() != "" && dataset.GetStatus() != "active") {
				continue
			}
			if err := c.cleanupDataset(ctx, dataset, now); err != nil {
				c.metrics.ObserveFailure(dataset.GetSpaceId(), dataset.GetDatasetId())
				result = errors.Join(result, fmt.Errorf("dataset %s/%s: %w", dataset.GetSpaceId(), dataset.GetDatasetId(), err))
			}
		}
		if page == nil || !page.GetHasMore() || len(datasets) == 0 {
			return result
		}
	}
}

func (c datasetCleanup) cleanupDataset(ctx context.Context, dataset *pb.Dataset, now time.Time) error {
	period, _, err := c.retention.Resolve(dataset.GetSpaceId(), dataset.GetFreq())
	if err != nil {
		return err
	}
	if period.Forever {
		return nil
	}
	// Rows are deleted by whole day buckets, so a Dataset keeps between its
	// retention and one more bucket of history.
	cutoff := now.UTC().Add(-period.Duration).Truncate(pebble.BucketDuration)
	started := time.Now()
	deleted, err := c.store.CleanupExpiredBuckets(ctx, dataset.GetSpaceId(), dataset.GetDatasetId(), cutoff)
	if err != nil {
		return err
	}
	elapsed := time.Since(started)
	c.metrics.ObserveCleanup(dataset.GetSpaceId(), dataset.GetDatasetId(), cutoff, elapsed, deleted)
	tlog.Infof("storage cleanup space=%s dataset=%s freq=%s retention=%s cutoff=%s deleted_ranges=%d elapsed=%s",
		dataset.GetSpaceId(), dataset.GetDatasetId(), dataset.GetFreq(), period, cutoff.Format(time.RFC3339), deleted, elapsed.Round(time.Millisecond))
	return nil
}

func runViewRole() error {
	loadedCfg, err := loadRuntimeStorageConfig()
	if err != nil {
		return err
	}
	viewSettings, err := loadedCfg.ViewSettings()
	if err != nil {
		return err
	}
	policy, err := loadedCfg.LoadPolicy()
	if err != nil {
		return err
	}
	svc, err := viewservice.New(loadedCfg.Storage.Devices.ViewIndexRoot)
	if err != nil {
		return err
	}
	svc.SetMetrics(observability.DefaultViewMetrics)
	viewAttention, err := observability.NewViewAttention(prometheus.DefaultRegisterer)
	if err != nil {
		return fmt.Errorf("initialize storage-view attention: %w", err)
	}
	observability.DefaultViewMetrics.SetAttention(viewAttention)
	if !svc.HasEngine("duckdb") {
		return errors.New("storage-view requires a CGO-enabled DuckDB engine")
	}
	rawURL := os.Getenv("MOOX_STORAGE_EVENTBUS_URL")
	if rawURL == "" {
		return errors.New("MOOX_STORAGE_EVENTBUS_URL is required for view role")
	}
	// 与 PrimaryStore 一样直连同机的 Metadata，不经网关。
	metadataProxy := pb.NewMetadataClientProxy(
		client.WithTarget(envOrDefault("MOOX_STORAGE_METADATA_TARGET", "ip://127.0.0.1:20100")),
		client.WithNetwork("tcp"),
		client.WithProtocol("trpc"),
		client.WithTransport(transport.DefaultClientTransport),
	)
	primaryTarget := envOrDefault("MOOX_STORAGE_PRIMARY_TARGET", "ip://127.0.0.1:20102")
	// Primary history pages are key-only and bounded by the rebuild lookback,
	// but a cold Pebble history index can still take longer than the ordinary
	// 30s request budget while it is being compacted. Keep this timeout scoped
	// to the view's rebuild reader; live PrimaryStore traffic keeps its normal
	// service timeout.
	primaryProxy := pb.NewPrimaryStoreClientProxy(
		client.WithTarget(primaryTarget),
		client.WithNetwork("tcp"),
		client.WithProtocol("trpc"),
		client.WithTransport(transport.DefaultClientTransport),
		client.WithTimeout(5*time.Minute),
	)
	svc.SetPrimaryReader(primaryProxy)
	// The policy is validated on load, so its durations parse.
	maintenanceInterval, _ := time.ParseDuration(policy.View.MaintenanceCheckInterval)
	// 每序列保留根数随 DataView 查询响应告知调用方：不依赖维护器是否启动。
	svc.SetSeriesBars(policy.View.Bars)
	svc.SetMaxAge(policy)
	maintenanceOptions := viewservice.MaintenanceOptions{
		Metadata:                metadataProxy,
		PrimaryRange:            primaryProxy,
		Interval:                maintenanceInterval,
		Bars:                    policy.View.Bars,
		MaxViewFileBytes:        policy.View.MaxViewFileBytes,
		BackfillPageSize:        viewSettings.BackfillPageSize,
		BackfillRequestInterval: viewSettings.BackfillRequestInterval,
	}
	// 先绑定监听再恢复已建的索引：部署大时恢复要一段时间，进程先可达，存活检查能看到它。
	s := trpc.NewServer()
	// 要在 NewServer 之后装：NewServer 读配置时会换掉默认的日志对象。
	trpclog.InstallServiceName("storage")
	listener := s.Service("trpc.moox.storage.DataView.trpc")
	if listener == nil {
		return errors.New("DataView 监听没有配置")
	}
	pb.RegisterDataViewService(listener, svc)
	healthState, err := registerRoleHealth(s, "storage-view")
	if err != nil {
		return err
	}
	if err := storagebootstrap.RegisterMetricsReporter(s, "view", healthz.Readiness(healthState.Snapshot)); err != nil {
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
	eventConfig, err := storageEventBusConfig(loadedCfg, []string{rawURL}, "storage-view")
	if err != nil {
		return err
	}
	eventClient, err := connectStorageViewEventBus(trpc.BackgroundContext(), eventConfig)
	if err != nil {
		return err
	}
	defer eventClient.Close()
	// 就绪事件用专用连接发布：发布超时（确认丢失）后只重连这条连接。
	readyClient, err := eventClient.Fork(trpc.BackgroundContext(), "storage-view-ready")
	if err != nil {
		return fmt.Errorf("为 View 就绪事件发布器建立 EventBus 连接失败：%w", err)
	}
	defer readyClient.Close()
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	readyPublisher, err := events.NewPublisher(readyClient, registry)
	if err != nil {
		return err
	}
	// 每个已建好索引的 View 一个协程跟随 PrimaryStore 的变更日志；单次读取的超时由跟随器按等待时长另设。
	stopFollowing, err := svc.StartFollowing(trpc.BackgroundContext(), viewservice.FollowOptions{
		Changes: primaryProxy, Range: primaryProxy, Publisher: readyPublisher, Reconnect: readyClient.Reconnect,
	})
	if err != nil {
		return err
	}
	defer stopFollowing()
	if storageViewMaintenanceDisabled() {
		// 运维主动暂停建索引时（Storage 主机过载），实时消费照常工作。
		tlog.Warnf("storage view historical maintenance disabled by MOOX_STORAGE_VIEW_MAINTENANCE_DISABLED=1")
	} else {
		stopViewMaintainer, err := svc.StartViewMaintainerAsync(trpc.BackgroundContext(), maintenanceOptions)
		if err != nil {
			return err
		}
		defer stopViewMaintainer()
	}
	stopMinuteTasks := svc.StartMinuteTasks(trpc.BackgroundContext(), viewservice.DeleteOldestOptions{BatchSize: viewSettings.DeleteOldestBatchSize})
	defer stopMinuteTasks()
	return <-serveErr
}

// connectStorageViewEventBus 连上 EventBus 为止：临时故障按退避重试，其他错误直接返回。看门狗负责重启崩溃的进程，
// 不该在公网 EventBus 重连期间反复重启一个健康的 View；连上之后的断线由就绪事件发布器的重连处理。
func connectStorageViewEventBus(ctx context.Context, config jetstream.Config) (*jetstream.Client, error) {
	backoff := time.Second
	for {
		client, err := jetstream.Connect(ctx, config)
		if err == nil {
			return client, nil
		}
		if !isTransientStorageViewEventBusError(err) {
			return nil, err
		}
		tlog.Warnf("storage view EventBus unavailable; retrying in %s: %v", backoff, err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func isTransientStorageViewEventBusError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"no servers available",
		"initial nats connection is not ready",
		"connection refused",
		"connection reset",
		"i/o timeout",
		"context deadline exceeded",
		"tls handshake timeout",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func storageViewMaintenanceDisabled() bool {
	value := strings.TrimSpace(os.Getenv("MOOX_STORAGE_VIEW_MAINTENANCE_DISABLED"))
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

// loadRuntimeStorageConfig 与 CLI 复用同一条单次配置加载路径，并应用 MOOX_STORAGE_HOME 目录重定位。
func loadRuntimeStorageConfig() (storageconfig.LoadedConfig, error) {
	loaded, err := storageconfig.Load(os.Getenv("MOOX_STORAGE_CONFIG"))
	if err != nil {
		return storageconfig.LoadedConfig{}, err
	}
	if home := os.Getenv("MOOX_STORAGE_HOME"); home != "" {
		loaded.Storage.ApplyHomeRoot(home)
	}
	return loaded, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

const storageEventBusReconnectBufferBytes = 32 * 1024 * 1024

func storageEventBusConfig(loadedCfg storageconfig.LoadedConfig, urls []string, name string) (jetstream.Config, error) {
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
	path := jetstream.ExpandCredentialPath(loadedCfg.EventBusCredentialFile(os.Getenv("MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE")))
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

// registerRoleHealth 注册健康检查；返回的状态再交给指标上报，上报里的就绪与 /readyz 是同一个判断。
func registerRoleHealth(s *server.Server, instance string) (*storagehealth.State, error) {
	if s == nil {
		return nil, errors.New("storage health server is unavailable")
	}
	state := storagehealth.New("storage", instance, "", "")
	state.SnapshotFunc = storagehealth.SnapshotForRole(instance, observability.DefaultViewMetrics)
	state.SetReady(true)
	if err := storagehealth.Register(s.Service("trpc.moox.storage.Health"), state); err != nil {
		return nil, fmt.Errorf("register storage health: %w", err)
	}
	return state, nil
}
