//go:build cgo

package e2e

// View 跟随变更日志路径的装配：PrimaryStore（直接读写 Pebble）→ 变更日志 → View 跟随器 → ViewDataReady 发布到 EventBus。
// 各组件都在本进程内，Primary 与 View 之间是进程内调用，没有 RPC；EventBus 是内嵌的 NATS JetStream（文件存储，本机回环连接），
// MOOX_STORAGE 的主题、2 分钟去重窗口、8MB 消息上限与生产一致，转发协程照常把 period.completed 发到 EventBus。

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	storagerelay "github.com/mooyang-code/moox/modules/storage/internal/service/datanode/relay"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	viewduckdb "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/duckdb"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

const (
	followEnvMaxBytes = 8 << 20
	// followEnvPrefillBatch 是预填 View 时每次直接写入索引的行数。
	followEnvPrefillBatch = 20000
	followEnvSchemaHash   = "follow-env-schema-1"
	// followEnvDatasetCreatedAt 是数据集元数据的创建时刻（2026-11-01T00:00:00Z 的 Unix 毫秒）。
	followEnvDatasetCreatedAt int64 = 1793491200000
)

// followEnv 是一套装配好的 Storage：Primary、跟随变更日志的 View、EventBus。实现 readyBenchEnv。
type followEnv struct {
	store   *pebble.Store
	primary *primarystore.Service
	view    *viewservice.Service
	clock   atomic.Int64
	watcher *readyWatcher
	// metadata 是 View 读的数据集元数据，已建状态在索引文件里。
	metadata *followEnvMetadata
	// follow 是启动跟随时的参数，stopFollowing 停掉跟随（模拟 View 停机）。
	follow        viewservice.FollowOptions
	stopFollowing func()
	// root 是 View 的索引目录。
	root string
}

func (e *followEnv) Primary() readyBenchPrimary { return e.primary }
func (e *followEnv) Watcher() *readyWatcher     { return e.watcher }
func (e *followEnv) SetPeriodClock(at time.Time) {
	e.clock.Store(at.UnixNano())
}

func (e *followEnv) CountViewRows(ctx context.Context, datasetID, subject string, start, end time.Time) (uint64, error) {
	req := &pb.QueryTimeSeriesRowsReq{
		SpaceId: readyBenchSpace, DatasetId: datasetID,
		TimeRange: &pb.TimeRange{StartTime: start.UTC().Format(time.RFC3339), EndTime: end.UTC().Format(time.RFC3339)},
		Page:      &pb.Page{Page: 1, Size: 1}, TotalMode: pb.TotalMode_FORCE_EXACT,
	}
	if subject != "" {
		req.Selectors = []*pb.TimeSeriesSelector{{SubjectId: subject, Freq: readyBenchFreq}}
	}
	rsp, err := e.view.QueryTimeSeriesRows(ctx, req)
	if err != nil {
		return 0, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return 0, fmt.Errorf("查询 View %s：%s", datasetID, rsp.GetRetInfo().GetMsg())
	}
	return uint64(rsp.GetPageResult().GetTotal()), nil
}

// WaitApplied 等 View 跟上当前的日志头部。
func (e *followEnv) WaitApplied(t *testing.T, ctx context.Context, datasetID string) {
	t.Helper()
	// 索引建好（GetViewState 能看到）到跟随器登记之间有一小段间隔，这期间服务回答 VIEW_NOT_READY，按契约重试。
	deadline := time.Now().Add(30 * time.Second)
	rsp, err := e.view.WaitViewApplied(ctx, &pb.WaitViewAppliedReq{SpaceId: readyBenchSpace, DatasetId: datasetID, WaitTimeoutMs: 20000})
	for err == nil && rsp.GetRetInfo().GetCode() == pb.ErrorCode_VIEW_NOT_READY && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		rsp, err = e.view.WaitViewApplied(ctx, &pb.WaitViewAppliedReq{SpaceId: readyBenchSpace, DatasetId: datasetID, WaitTimeoutMs: 20000})
	}
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || !rsp.GetApplied() {
		t.Fatalf("等 View %s 跟上变更日志：rsp=%v err=%v", datasetID, rsp, err)
	}
}

// followEnvOptions 选择 View 的建法：prebuilt 为真时在 View 进程启动前直接建好索引、预填历史并记下当前日志头部
// （压测用，预填 1 万根/序列走 Primary 回填太慢）；否则由 View 的维护流程从 Primary 读存量建索引。
type followEnvOptions struct {
	prebuilt bool
	// rangeReader 包一层 Primary 的时序读取（用来在建索引中途插入写入）；为空时直接用 Primary。
	rangeReader func(viewservice.TimeSeriesRangeReader) viewservice.TimeSeriesRangeReader
	// datasets 为空时用内存缓存登记数据集；e2e 用 SQLite 元数据时传入它的缓存。
	datasets *metacache.DatasetCache
	// setup 在 Primary 建好之后、View 启动之前调用（e2e 在这里经 Metadata 服务建数据集）。
	setup func(store *pebble.Store)
}

func newFollowEnv(t *testing.T, datasets []readyBenchDataset, opts followEnvOptions) *followEnv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	env := &followEnv{}
	env.SetPeriodClock(readyBenchBase.Add(-time.Hour))

	// EventBus：内嵌 JetStream，MOOX_STORAGE 与生产一致。
	server, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), MaxPayload: followEnvMaxBytes, NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	go server.Start()
	if !server.ReadyForConnections(10 * time.Second) {
		t.Fatal("内嵌 NATS 没有启动")
	}
	t.Cleanup(server.Shutdown)
	nc, err := nats.Connect(server.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name: "MOOX_STORAGE",
		Subjects: []string{
			"moox.event.storage.dataset.period.completed.v1.>", "moox.event.storage.view.data.ready.v2.>",
		},
		Retention: nats.LimitsPolicy, Discard: nats.DiscardOld, Storage: nats.FileStorage, Duplicates: 2 * time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	env.watcher = newReadyWatcher(t, nc)

	// Primary：Pebble 与 PrimaryStore，写入时按数据集点查确认它仍在；转发协程照常转发周期完成事件。
	datasetsCache := opts.datasets
	if datasetsCache == nil {
		datasetsCache = newFollowEnvDatasetCache(t, datasets)
	}
	env.store, err = pebble.Open(pebble.Options{
		Path: filepath.Join(t.TempDir(), "pebble"), MaxEventBytes: followEnvMaxBytes,
		PeriodNow:     func() time.Time { return time.Unix(0, env.clock.Load()).UTC() },
		DatasetExists: datasetsCache.HasDataset,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.store.Close() })
	env.primary, err = primarystore.New(primarystore.Options{Store: env.store, Datasets: datasetsCache})
	if err != nil {
		t.Fatal(err)
	}
	stopRelay, err := storagerelay.StartWithEventBus(ctx, env.store, jetstream.Config{URLs: []string{server.ClientURL()}, Name: "storage-primary-follow-env"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopRelay)
	if opts.setup != nil {
		opts.setup(env.store)
	}

	// View：先按需建好并预填索引，再启动跟随与维护。
	root := filepath.Join(t.TempDir(), "view")
	env.root = root
	env.metadata = &followEnvMetadata{}
	for _, dataset := range datasets {
		if opts.prebuilt {
			prebuildFollowEnvIndex(t, ctx, root, env.store, dataset)
		}
		env.metadata.add(dataset)
	}
	env.view, err = viewservice.New(root)
	if err != nil {
		t.Fatal(err)
	}
	reader := primaryFieldReader{service: env.primary}
	var rangeReader viewservice.TimeSeriesRangeReader = reader
	if opts.rangeReader != nil {
		rangeReader = opts.rangeReader(reader)
	}
	env.view.SetPrimaryReader(reader)
	eventClient, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.ClientURL()}, Name: "storage-view-follow-env"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eventClient.Close() })
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := events.NewPublisher(eventClient, registry)
	if err != nil {
		t.Fatal(err)
	}
	env.follow = viewservice.FollowOptions{Changes: reader, Range: rangeReader, Publisher: publisher, Reconnect: eventClient.Reconnect}
	if env.stopFollowing, err = env.view.StartFollowing(ctx, env.follow); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { env.stopFollowing() })
	maintenance := viewservice.MaintenanceOptions{Metadata: env.metadata, PrimaryRange: rangeReader, Interval: 100 * time.Millisecond, BackfillPageSize: 2000}
	if opts.prebuilt {
		if err := env.view.RestoreActiveViews(ctx, maintenance); err != nil {
			t.Fatal(err)
		}
		return env
	}
	stopMaintainer, err := env.view.StartViewMaintainerAsync(ctx, maintenance)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopMaintainer)
	return env
}

// WaitBuilt 等维护流程把 View 的索引建好。
func (e *followEnv) WaitBuilt(t *testing.T, ctx context.Context, datasetID string) *pb.GetViewStateRsp {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		rsp, err := e.view.GetViewState(ctx, &pb.GetViewStateReq{SpaceId: readyBenchSpace, DatasetId: datasetID})
		if err == nil && rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS && rsp.GetIndexId() != "" {
			return rsp
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("30 秒内 View %s 没有建好索引", datasetID)
	return nil
}

// followEnvColumns 是数据集的列（按建列顺序），也是它查询索引的列。
func followEnvColumns(dataset readyBenchDataset) []*pb.DatasetColumn {
	columns := make([]*pb.DatasetColumn, len(dataset.columns))
	for i, column := range dataset.columns {
		columns[i] = &pb.DatasetColumn{SpaceId: readyBenchSpace, DatasetId: dataset.datasetID, ColumnName: column.name, DisplayName: column.name, ValueType: column.kind, Status: "active"}
	}
	return columns
}

// prebuildFollowEnvIndex 在 View 进程启动前直接建好索引、按 prefillBars 写入历史，并把当前日志头部记为跟随起点与建成序号。
func prebuildFollowEnvIndex(t *testing.T, ctx context.Context, root string, store *pebble.Store, dataset readyBenchDataset) {
	t.Helper()
	manager, err := viewduckdb.OpenIndexManager(viewduckdb.IndexManagerOptions{Root: filepath.Join(root, "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	indexID := viewindex.IndexID(readyBenchSpace, dataset.datasetID)
	columns := make([]*viewindex.Column, len(dataset.columns))
	for i, column := range dataset.columns {
		columns[i] = &viewindex.Column{ColumnName: column.name, ValueType: column.kind, SortOrder: uint32(i)}
	}
	schema := viewindex.ViewIndexSchema{SpaceID: readyBenchSpace, DatasetID: dataset.datasetID, ViewVersion: 1, Engine: "duckdb", SchemaHash: followEnvSchemaHash, Columns: columns, DatasetCreatedAt: followEnvDatasetCreatedAt}
	if err := manager.Prepare(ctx, indexID, schema); err != nil {
		t.Fatal(err)
	}
	if dataset.prefillBars > 0 {
		started := time.Now()
		batch := make([]viewindex.RowWrite, 0, followEnvPrefillBatch)
		flush := func() {
			if len(batch) == 0 {
				return
			}
			if err := manager.Write(ctx, indexID, viewindex.ViewIndexWriteBatch{RowWrites: batch, ViewRevision: 1, ViewSchemaHash: followEnvSchemaHash, WriteMode: viewindex.Backfill}); err != nil {
				t.Fatalf("预填 View %s：%v", dataset.datasetID, err)
			}
			batch = batch[:0]
		}
		for _, subject := range dataset.subjects {
			for bar := dataset.prefillBars; bar >= 1; bar-- {
				row := readyBenchRow(dataset, subject, readyBenchBase.Add(-time.Duration(bar)*readyBenchStep), 0)
				batch = append(batch, viewindex.RowWrite{Key: viewindex.RowKey{Key: row.GetKey()}, Fields: row.GetFields()})
				if len(batch) == followEnvPrefillBatch {
					flush()
				}
			}
		}
		flush()
		t.Logf("预填 View %s：%d 个标的 × %d 根，耗时 %s", dataset.datasetID, len(dataset.subjects), dataset.prefillBars, time.Since(started).Round(time.Millisecond))
	}
	head, err := store.ChangeCursor()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveFollowState(ctx, indexID, viewindex.FollowState{Cursor: viewindex.ChangeCursor{StoreID: head.GetStoreId(), Sequence: head.GetSequence()}, BuiltSequence: head.GetSequence()}); err != nil {
		t.Fatal(err)
	}
}

// newFollowEnvDatasetCache 为 PrimaryStore 构造压测/集成用的内存数据集缓存。
func newFollowEnvDatasetCache(t *testing.T, datasets []readyBenchDataset) *metacache.DatasetCache {
	t.Helper()
	dsList := make([]*pb.Dataset, 0, len(datasets))
	var colList []*pb.DatasetColumn
	for _, dataset := range datasets {
		dsList = append(dsList, followEnvDataset(dataset))
		colList = append(colList, followEnvColumns(dataset)...)
	}
	return newTestDatasetCache(t, dsList, colList)
}

// followEnvDataset 是压测数据集的元数据。
func followEnvDataset(dataset readyBenchDataset) *pb.Dataset {
	return &pb.Dataset{
		SpaceId: readyBenchSpace, DatasetId: dataset.datasetID, Name: dataset.datasetID,
		DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: readyBenchFreq, Status: "active",
		AllowedLatenessSeconds: uint32(readyBenchLatenessSeconds), CreatedAt: followEnvDatasetCreatedAt,
	}
}

// followEnvMetadata 是 View 读的数据集元数据：数据集与它们的列。
type followEnvMetadata struct {
	mu       sync.Mutex
	datasets []*pb.Dataset
	columns  []*pb.DatasetColumn
}

func (m *followEnvMetadata) add(dataset readyBenchDataset) {
	m.addDataset(followEnvDataset(dataset), followEnvColumns(dataset)...)
}

func (m *followEnvMetadata) addDataset(dataset *pb.Dataset, columns ...*pb.DatasetColumn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.datasets = append(m.datasets, dataset)
	m.columns = append(m.columns, columns...)
}

func (m *followEnvMetadata) ListDatasets(_ context.Context, req *pb.ListDatasetsReq, _ ...client.Option) (*pb.ListDatasetsRsp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var datasets []*pb.Dataset
	if req.GetPage().GetPage() <= 1 {
		for _, dataset := range m.datasets {
			datasets = append(datasets, proto.Clone(dataset).(*pb.Dataset))
		}
	}
	return &pb.ListDatasetsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}, Datasets: datasets, PageResult: &pb.PageResult{Total: uint32(len(m.datasets))}}, nil
}

func (m *followEnvMetadata) ListDatasetColumns(_ context.Context, req *pb.ListDatasetColumnsReq, _ ...client.Option) (*pb.ListDatasetColumnsRsp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var columns []*pb.DatasetColumn
	if req.GetPage().GetPage() <= 1 {
		for _, column := range m.columns {
			columns = append(columns, proto.Clone(column).(*pb.DatasetColumn))
		}
	}
	return &pb.ListDatasetColumnsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}, Columns: columns, PageResult: &pb.PageResult{Total: uint32(len(m.columns))}}, nil
}
