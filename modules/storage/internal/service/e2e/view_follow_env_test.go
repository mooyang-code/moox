//go:build cgo

package e2e

// View 跟随变更日志路径的装配：PrimaryStore（直接读写 Pebble）→ 变更日志 → View 跟随器 → ViewDataReady 发布到 EventBus。
// 各组件都在本进程内，Primary 与 View 之间是进程内调用，没有 RPC；EventBus 是内嵌的 NATS JetStream（文件存储，本机回环连接），
// MOOX_STORAGE 的主题、2 分钟去重窗口、8MB 消息上限与生产一致，Outbox 照常转发 period.completed 等跨机事件。

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	storageoutbox "github.com/mooyang-code/moox/modules/storage/internal/service/datanode/outbox"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	viewduckdb "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/duckdb"
	"github.com/mooyang-code/moox/modules/storage/internal/serviceauth"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

const (
	followEnvSecret   = "view-follow-env-secret"
	followEnvMaxBytes = 8 << 20
	// followEnvPrefillBatch 是预填 View 时每次直接写入索引的行数。
	followEnvPrefillBatch = 20000
	followEnvSchemaHash   = "follow-env-schema-1"
	// followEnvViewCreatedAt 是 View 元数据的创建时间（与 SQLite 的 c_ctime 同一格式）。
	followEnvViewCreatedAt = "2026-11-01 00:00:00"
)

// followEnv 是一套装配好的 Storage：Primary、跟随变更日志的 View、EventBus。实现 readyBenchEnv。
type followEnv struct {
	store   *pebble.Store
	primary *primarystore.Service
	view    *viewservice.Service
	auth    *pb.AuthInfo
	clock   atomic.Int64
	watcher *readyWatcher
	// metadata 是 View 的元数据：只有 View 的声明，已建状态在索引文件里。
	metadata *followEnvMetadata
	datasets map[string]string
	// follow 是启动跟随时的参数，stopFollowing 停掉跟随（模拟 View 停机）。
	follow        viewservice.FollowOptions
	stopFollowing func()
	// root 是 View 的索引目录。
	root string
}

func (e *followEnv) Primary() readyBenchPrimary { return e.primary }
func (e *followEnv) Auth() *pb.AuthInfo         { return e.auth }
func (e *followEnv) Watcher() *readyWatcher     { return e.watcher }
func (e *followEnv) SetPeriodClock(at time.Time) {
	e.clock.Store(at.UnixNano())
}

func (e *followEnv) CountViewRows(ctx context.Context, viewID, subject string, start, end time.Time) (uint64, error) {
	req := &pb.QueryTimeSeriesRowsReq{
		AuthInfo: followEnvViewAuth(), SpaceId: readyBenchSpace, ViewId: viewID,
		TimeRange: &pb.TimeRange{StartTime: start.UTC().Format(time.RFC3339), EndTime: end.UTC().Format(time.RFC3339)},
		Page:      &pb.Page{Page: 1, Size: 1}, TotalMode: pb.TotalMode_FORCE_EXACT,
	}
	if subject != "" {
		req.Selectors = []*pb.TimeSeriesSelector{{SpaceId: readyBenchSpace, DatasetId: e.datasets[viewID], SubjectId: subject, Freq: readyBenchFreq}}
	}
	rsp, err := e.view.QueryTimeSeriesRows(ctx, req)
	if err != nil {
		return 0, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return 0, fmt.Errorf("查询 View %s：%s", viewID, rsp.GetRetInfo().GetMsg())
	}
	return uint64(rsp.GetPageResult().GetTotal()), nil
}

// WaitApplied 等 View 跟上当前的日志头部。
func (e *followEnv) WaitApplied(t *testing.T, ctx context.Context, viewID string) {
	t.Helper()
	rsp, err := e.view.WaitViewApplied(ctx, &pb.WaitViewAppliedReq{AuthInfo: followEnvViewAuth(), SpaceId: readyBenchSpace, ViewId: viewID, WaitTimeoutMs: 20000})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || !rsp.GetApplied() {
		t.Fatalf("等 View %s 跟上变更日志：rsp=%v err=%v", viewID, rsp, err)
	}
}

func followEnvViewAuth() *pb.AuthInfo {
	return &pb.AuthInfo{AppId: "storage-bench", AppKey: serviceauth.Key(followEnvSecret, "storage-bench")}
}

// followEnvOptions 选择 View 的建法：prebuilt 为真时在 View 进程启动前直接建好索引、预填历史并记下当前日志头部
// （压测用，预填 1 万根/序列走 Primary 回填太慢）；否则由 View 的维护流程从 Primary 读存量建索引。
type followEnvOptions struct {
	prebuilt bool
	// rangeReader 包一层 Primary 的时序读取（用来在建索引中途插入写入）；为空时直接用 Primary。
	rangeReader func(viewservice.TimeSeriesRangeReader) viewservice.TimeSeriesRangeReader
	// snapshot 为空时用内存快照登记数据集；e2e 用 SQLite 元数据时传入它的快照。
	snapshot func() metadata.RequestSnapshot
	// datasetExists 为空时按内存快照判断。
	datasetExists func(spaceID, datasetID string) bool
	validator     primarystore.Validator
	// setup 在 Primary 建好之后、View 启动之前调用（e2e 在这里经 Metadata 服务建数据集）。
	setup func(store *pebble.Store)
}

func newFollowEnv(t *testing.T, datasets []readyBenchDataset, opts followEnvOptions) *followEnv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	env := &followEnv{auth: &pb.AuthInfo{AppId: "storage-primary"}, datasets: make(map[string]string)}
	env.SetPeriodClock(readyBenchBase.Add(-time.Hour))
	for _, dataset := range datasets {
		env.datasets[dataset.viewID] = dataset.datasetID
	}

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

	// Primary：Pebble 与 PrimaryStore，写入时按数据集点查确认它仍在；Outbox 照常转发跨机事件。
	snapshot := newFollowEnvSnapshot(datasets)
	datasetExists := opts.datasetExists
	if datasetExists == nil {
		datasetExists = func(spaceID, datasetID string) bool {
			_, ok := snapshot.GetDataset(spaceID, datasetID)
			return ok
		}
	}
	env.store, err = pebble.Open(pebble.Options{
		Path: filepath.Join(t.TempDir(), "pebble"), MaxEventBytes: followEnvMaxBytes,
		PeriodNow:     func() time.Time { return time.Unix(0, env.clock.Load()).UTC() },
		DatasetExists: datasetExists,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.store.Close() })
	snapshotFunc := opts.snapshot
	if snapshotFunc == nil {
		snapshotFunc = func() metadata.RequestSnapshot { return snapshot }
	}
	env.primary, err = primarystore.New(primarystore.Options{Store: env.store, Snapshot: snapshotFunc, Validator: opts.validator})
	if err != nil {
		t.Fatal(err)
	}
	stopRelay, err := storageoutbox.StartWithEventBus(ctx, env.store, jetstream.Config{URLs: []string{server.ClientURL()}, Name: "storage-primary-follow-env"})
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
		view := followEnvView(dataset)
		if opts.prebuilt {
			prebuildFollowEnvIndex(t, ctx, root, env.store, view, dataset)
		}
		env.metadata.views = append(env.metadata.views, view)
	}
	env.view, err = viewservice.New(root, followEnvSecret)
	if err != nil {
		t.Fatal(err)
	}
	reader := primaryFieldReader{service: env.primary}
	var rangeReader viewservice.TimeSeriesRangeReader = reader
	if opts.rangeReader != nil {
		rangeReader = opts.rangeReader(reader)
	}
	env.view.SetPrimaryAuth(env.auth)
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
func (e *followEnv) WaitBuilt(t *testing.T, ctx context.Context, viewID string) *pb.GetViewStateRsp {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		rsp, err := e.view.GetViewState(ctx, &pb.GetViewStateReq{AuthInfo: followEnvViewAuth(), SpaceId: readyBenchSpace, ViewId: viewID})
		if err == nil && rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS && rsp.GetIndexId() != "" {
			return rsp
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("30 秒内 View %s 没有建好索引", viewID)
	return nil
}

func followEnvView(dataset readyBenchDataset) *pb.View {
	columns := make([]*pb.ViewColumn, len(dataset.columns))
	for i, column := range dataset.columns {
		columns[i] = &pb.ViewColumn{SpaceId: readyBenchSpace, ViewId: dataset.viewID, ColumnName: column.name, OriginId: column.name, ValueType: column.kind,
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, SortOrder: uint32(i)}
	}
	return &pb.View{SpaceId: readyBenchSpace, ViewId: dataset.viewID, DatasetId: dataset.datasetID, Engine: "duckdb", Freq: readyBenchFreq, Columns: columns, Status: "active", CreatedAt: followEnvViewCreatedAt}
}

// prebuildFollowEnvIndex 在 View 进程启动前直接建好索引、按 prefillBars 写入历史，并把当前日志头部记为跟随起点与建成序号。
func prebuildFollowEnvIndex(t *testing.T, ctx context.Context, root string, store *pebble.Store, view *pb.View, dataset readyBenchDataset) {
	t.Helper()
	manager, err := viewduckdb.OpenIndexManager(viewduckdb.IndexManagerOptions{Root: filepath.Join(root, "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	indexID := viewindex.ViewIndexID(view.GetSpaceId(), view.GetViewId())
	schema := viewindex.ViewIndexSchema{SpaceID: readyBenchSpace, ViewID: view.GetViewId(), PrimaryDatasetID: dataset.datasetID, ViewVersion: 1, Engine: "duckdb", SchemaHash: followEnvSchemaHash, Columns: view.GetColumns(), ViewCreatedAt: view.GetCreatedAt()}
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
				t.Fatalf("预填 View %s：%v", view.GetViewId(), err)
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
		t.Logf("预填 View %s：%d 个标的 × %d 根，耗时 %s", view.GetViewId(), len(dataset.subjects), dataset.prefillBars, time.Since(started).Round(time.Millisecond))
	}
	head, err := store.ChangeCursor()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveFollowState(ctx, indexID, viewindex.FollowState{Cursor: viewindex.ChangeCursor{StoreID: head.GetStoreId(), Sequence: head.GetSequence()}, BuiltSequence: head.GetSequence()}); err != nil {
		t.Fatal(err)
	}
}

// followEnvSnapshot 是 PrimaryStore 的请求快照：周期提交与完成标记要求数据集在快照里、是时序数据且已启用。
type followEnvSnapshot struct {
	datasets map[string]*pb.Dataset
	columns  map[string][]*pb.DatasetColumn
}

func newFollowEnvSnapshot(datasets []readyBenchDataset) *followEnvSnapshot {
	snapshot := &followEnvSnapshot{datasets: make(map[string]*pb.Dataset), columns: make(map[string][]*pb.DatasetColumn)}
	for _, dataset := range datasets {
		snapshot.datasets[dataset.datasetID] = &pb.Dataset{
			SpaceId: readyBenchSpace, DatasetId: dataset.datasetID, Name: dataset.datasetID,
			DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: readyBenchFreq, Status: "active",
			AllowedLatenessSeconds: uint32(readyBenchLatenessSeconds),
		}
		for _, column := range dataset.columns {
			snapshot.columns[dataset.datasetID] = append(snapshot.columns[dataset.datasetID], &pb.DatasetColumn{
				SpaceId: readyBenchSpace, DatasetId: dataset.datasetID, ColumnName: column.name, OriginId: column.name, ValueType: column.kind, Status: "active",
			})
		}
	}
	return snapshot
}

func (s *followEnvSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	if spaceID != readyBenchSpace {
		return nil, false
	}
	dataset, ok := s.datasets[datasetID]
	return dataset, ok
}

func (s *followEnvSnapshot) ListDatasetColumns(spaceID, datasetID string, _ *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	if spaceID != readyBenchSpace {
		return nil, &pb.PageResult{}, nil
	}
	columns := s.columns[datasetID]
	return columns, &pb.PageResult{Total: uint32(len(columns))}, nil
}

// followEnvMetadata 是 View 用到的元数据：只有 View 列表，其余方法不应被调用（调用即 panic）。
type followEnvMetadata struct {
	pb.MetadataClientProxy
	mu    sync.Mutex
	views []*pb.View
}

func (m *followEnvMetadata) ListViews(_ context.Context, req *pb.ListViewsReq, _ ...client.Option) (*pb.ListViewsRsp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	views := make([]*pb.View, 0, len(m.views))
	if req.GetPage().GetPage() <= 1 {
		for _, view := range m.views {
			if req.GetSpaceId() == "" || req.GetSpaceId() == view.GetSpaceId() {
				views = append(views, proto.Clone(view).(*pb.View))
			}
		}
	}
	return &pb.ListViewsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}, Views: views, PageResult: &pb.PageResult{Total: uint32(len(views))}}, nil
}
