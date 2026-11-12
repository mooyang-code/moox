//go:build cgo

package e2e

// View 就绪耗时压测的旧路径装配：PrimaryStore（直接读写 Pebble）→ Outbox → JetStream → View 消费者。
// 各组件都在本进程内，EventBus 是内嵌的 NATS JetStream（文件存储，经本机回环连接），参数按生产配置：
//   - Outbox 转发用默认参数（100ms 兜底轮询，写入提交后立即唤醒）；
//   - View 消费者 fetch_batch、max_workers、max_ack_pending 都是 1，ack_wait 120s（config/storage.yaml）；
//   - MOOX_STORAGE 的四个主题、文件存储、2 分钟去重窗口、8MB 消息上限（modules/eventbus/config/app.yaml）。
// 与生产的差别：Primary 与 View 之间是进程内调用，没有 RPC；View 用到的元数据（View 列表、
// View 周期状态）是内存实现，不经 SQLite；三个数据集共用一个静态消费者（生产是每个数据集一个，场景串行执行，等价）。

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	storageoutbox "github.com/mooyang-code/moox/modules/storage/internal/service/datanode/outbox"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
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
	legacyBenchSecret   = "view-ready-bench-secret"
	legacyBenchMaxBytes = 8 << 20
	// legacyBenchPrefillBatch 是预填 View 时每次直接写入索引的行数。
	legacyBenchPrefillBatch = 20000
)

type legacyReadyBenchEnv struct {
	primary *primarystore.Service
	view    *viewservice.Service
	auth    *pb.AuthInfo
	clock   atomic.Int64
	watcher *readyWatcher
}

func (e *legacyReadyBenchEnv) Primary() readyBenchPrimary { return e.primary }
func (e *legacyReadyBenchEnv) Auth() *pb.AuthInfo         { return e.auth }
func (e *legacyReadyBenchEnv) Watcher() *readyWatcher     { return e.watcher }
func (e *legacyReadyBenchEnv) SetPeriodClock(at time.Time) {
	e.clock.Store(at.UnixNano())
}

func (e *legacyReadyBenchEnv) CountViewRows(ctx context.Context, viewID, subject string, start, end time.Time) (uint64, error) {
	req := &pb.QueryTimeSeriesRowsReq{
		AuthInfo: legacyBenchViewAuth(), SpaceId: readyBenchSpace, ViewId: viewID,
		TimeRange: &pb.TimeRange{StartTime: start.UTC().Format(time.RFC3339), EndTime: end.UTC().Format(time.RFC3339)},
		Page:      &pb.Page{Page: 1, Size: 1}, TotalMode: pb.TotalMode_FORCE_EXACT,
	}
	if subject != "" {
		req.Selectors = []*pb.TimeSeriesSelector{{SpaceId: readyBenchSpace, DatasetId: e.viewDataset(viewID), SubjectId: subject, Freq: readyBenchFreq}}
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

func (e *legacyReadyBenchEnv) viewDataset(viewID string) string {
	for _, id := range []string{readyBenchKlineSet, readyBenchFactorSet} {
		if viewID == id+"_view" {
			return id
		}
	}
	return ""
}

func legacyBenchViewAuth() *pb.AuthInfo {
	return &pb.AuthInfo{AppId: "storage-bench", AppKey: serviceauth.Key(legacyBenchSecret, "storage-bench")}
}

func newReadyBenchEnv(t *testing.T, datasets []readyBenchDataset) readyBenchEnv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	env := &legacyReadyBenchEnv{auth: &pb.AuthInfo{AppId: "storage-primary"}}
	env.SetPeriodClock(readyBenchBase.Add(-time.Hour))

	// EventBus：内嵌 JetStream，MOOX_STORAGE 与生产一致。
	server, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), MaxPayload: legacyBenchMaxBytes, NoLog: true, NoSigs: true,
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
			"moox.event.storage.dataset.rows.upserted.v2.>", "moox.event.storage.dataset.period.completed.v1.>",
			"moox.event.storage.view.data.ready.v2.>", "moox.event.storage.dataset.sync_point.v1.>",
		},
		Retention: nats.LimitsPolicy, Discard: nats.DiscardOld, Storage: nats.FileStorage, Duplicates: 2 * time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	env.watcher = newReadyWatcher(t, nc)
	eventClient, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.ClientURL()}, Name: "storage-view-ready-bench"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eventClient.Close() })

	// PrimaryStore 与它的 Pebble 库；写入时按数据集点查确认它仍在，与生产 primary 一致。
	snapshot := newLegacyBenchSnapshot(datasets)
	store, err := pebble.Open(pebble.Options{
		NodeID: "storage-node-0", Path: filepath.Join(t.TempDir(), "pebble"), MaxEventBytes: legacyBenchMaxBytes,
		PeriodNow: func() time.Time { return time.Unix(0, env.clock.Load()).UTC() },
		DatasetExists: func(spaceID, datasetID string) bool {
			_, ok := snapshot.GetDataset(spaceID, datasetID)
			return ok
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	env.primary, err = primarystore.New(primarystore.Options{
		Snapshot: func() metadata.RequestSnapshot { return snapshot },
		Store:    store,
	})
	if err != nil {
		t.Fatal(err)
	}
	relayErrors := make(chan error, 16)
	relay, err := storageoutbox.NewRelay(store, storageoutbox.NewJetStreamPublisher(eventClient), storageoutbox.RelayOptions{
		ErrorReporter: func(err error) {
			select {
			case relayErrors <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	relay.Start(ctx)
	t.Cleanup(func() {
		relay.Close()
		select {
		case err := <-relayErrors:
			t.Errorf("Outbox 转发出错：%v", err)
		default:
		}
	})

	// View：建好索引、预填历史、按元数据挂接，再启动消费。
	view, err := viewservice.New(filepath.Join(t.TempDir(), "view"), legacyBenchSecret)
	if err != nil {
		t.Fatal(err)
	}
	env.view = view
	view.SetPrimaryAuth(env.auth)
	view.SetPrimaryReader(primaryFieldReader{service: env.primary})
	metadataClient := newLegacyBenchMetadata()
	for _, dataset := range datasets {
		metadataClient.views = append(metadataClient.views, prepareLegacyBenchView(t, ctx, view, dataset))
	}
	if err := view.RestoreActiveViews(ctx, viewservice.MaintenanceOptions{Metadata: metadataClient}); err != nil {
		t.Fatal(err)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var filters []string
	for _, dataset := range datasets {
		filters = append(filters, exactDatasetEventSubjects(t, registry, readyBenchSpace, dataset.datasetID)...)
	}
	stop, err := view.StartEventConsumer(ctx, eventClient, viewservice.EventConsumerOptions{
		PartitionID: "bench", Consumer: "view_ready_bench", FilterSubjects: filters,
		FetchBatch: 1, MaxWorkers: 1, MaxAckPending: 1, AckWaitMS: 120000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return env
}

// prepareLegacyBenchView 建空索引，按 prefillBars 直接写入历史（不经应用路径），返回挂接用的 View 元数据。
func prepareLegacyBenchView(t *testing.T, ctx context.Context, view *viewservice.Service, dataset readyBenchDataset) *pb.View {
	t.Helper()
	columns := make([]*pb.ViewColumn, len(dataset.columns))
	for i, column := range dataset.columns {
		columns[i] = &pb.ViewColumn{SpaceId: readyBenchSpace, ViewId: dataset.viewID, ColumnName: column.name, OriginId: column.name, ValueType: column.kind}
	}
	const schemaHash = "bench-schema-1"
	auth := legacyBenchViewAuth()
	if rsp, err := view.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: dataset.viewID, Schema: &pb.ViewIndexSchema{
		SpaceId: readyBenchSpace, ViewId: dataset.viewID, DatasetId: dataset.datasetID, ViewVersion: 1, Engine: "duckdb", ViewSchemaHash: schemaHash, Columns: columns,
	}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("建 View %s 的索引：rsp=%v err=%v", dataset.viewID, rsp.GetRetInfo(), err)
	}
	if dataset.prefillBars > 0 {
		started := time.Now()
		batch := make([]*pb.ViewIndexRowWrite, 0, legacyBenchPrefillBatch)
		flush := func() {
			if len(batch) == 0 {
				return
			}
			rsp, err := view.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: dataset.viewID, Batch: &pb.ViewIndexWriteBatch{
				ViewRevision: 1, ViewSchemaHash: schemaHash, WriteMode: "BACKFILL", RowWrites: batch,
			}})
			if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
				t.Fatalf("预填 View %s：rsp=%v err=%v", dataset.viewID, rsp.GetRetInfo(), err)
			}
			batch = batch[:0]
		}
		for _, subject := range dataset.subjects {
			for bar := dataset.prefillBars; bar >= 1; bar-- {
				at := readyBenchBase.Add(-time.Duration(bar) * readyBenchStep)
				row := readyBenchRow(dataset, subject, at, 0)
				batch = append(batch, &pb.ViewIndexRowWrite{Key: &pb.ViewIndexRowKey{RowKey: row.GetKey()}, Fields: row.GetFields()})
				if len(batch) == legacyBenchPrefillBatch {
					flush()
				}
			}
		}
		flush()
		t.Logf("预填 View %s：%d 个标的 × %d 根，耗时 %s", dataset.viewID, len(dataset.subjects), dataset.prefillBars, time.Since(started).Round(time.Millisecond))
	}
	return &pb.View{
		SpaceId: readyBenchSpace, ViewId: dataset.viewID, DatasetId: dataset.datasetID, Engine: "duckdb", Freq: readyBenchFreq,
		ActiveIndexId: dataset.viewID, ViewRevision: 1, ActiveViewSchemaHash: schemaHash, ActiveColumns: columns, Status: "active",
	}
}

// legacyBenchSnapshot 是 PrimaryStore 的请求快照：周期提交与完成标记要求数据集在快照里、是时序数据且已启用。
type legacyBenchSnapshot struct {
	datasets map[string]*pb.Dataset
	columns  map[string][]*pb.DatasetColumn
}

func newLegacyBenchSnapshot(datasets []readyBenchDataset) *legacyBenchSnapshot {
	snapshot := &legacyBenchSnapshot{datasets: make(map[string]*pb.Dataset), columns: make(map[string][]*pb.DatasetColumn)}
	for _, dataset := range datasets {
		snapshot.datasets[dataset.datasetID] = &pb.Dataset{
			SpaceId: readyBenchSpace, DatasetId: dataset.datasetID, Name: dataset.datasetID,
			DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: readyBenchFreq, Status: "active",
			Attributes: map[string]string{"allowed_lateness_seconds": fmt.Sprint(readyBenchLatenessSeconds)},
		}
		for _, column := range dataset.columns {
			snapshot.columns[dataset.datasetID] = append(snapshot.columns[dataset.datasetID], &pb.DatasetColumn{
				SpaceId: readyBenchSpace, DatasetId: dataset.datasetID, ColumnName: column.name, OriginId: column.name, ValueType: column.kind, Status: "active",
			})
		}
	}
	return snapshot
}

func (s *legacyBenchSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	if spaceID != readyBenchSpace {
		return nil, false
	}
	dataset, ok := s.datasets[datasetID]
	return dataset, ok
}

func (s *legacyBenchSnapshot) ListDatasetColumns(spaceID, datasetID string, _ *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	if spaceID != readyBenchSpace {
		return nil, &pb.PageResult{}, nil
	}
	columns := s.columns[datasetID]
	return columns, &pb.PageResult{Total: uint32(len(columns))}, nil
}

// legacyBenchMetadata 是 View 用到的元数据：View 列表与 View 周期状态在内存里，其余方法不应被调用（调用即 panic）。
type legacyBenchMetadata struct {
	pb.MetadataClientProxy
	views  []*pb.View
	mu     sync.Mutex
	states map[string]*pb.ViewPeriodDatasetState
}

func newLegacyBenchMetadata() *legacyBenchMetadata {
	return &legacyBenchMetadata{states: make(map[string]*pb.ViewPeriodDatasetState)}
}

func (m *legacyBenchMetadata) ListViews(_ context.Context, req *pb.ListViewsReq, _ ...client.Option) (*pb.ListViewsRsp, error) {
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

func (m *legacyBenchMetadata) GetView(_ context.Context, req *pb.GetViewReq, _ ...client.Option) (*pb.GetViewRsp, error) {
	for _, view := range m.views {
		if view.GetSpaceId() == req.GetSpaceId() && view.GetViewId() == req.GetViewId() {
			return &pb.GetViewRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}, View: proto.Clone(view).(*pb.View)}, nil
		}
	}
	return &pb.GetViewRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NOT_FOUND, Msg: "view not found"}}, nil
}

// UpsertViewPeriodDatasetState 与 SQLite 实现一致：同一键先到者胜，返回已记录的状态。
func (m *legacyBenchMetadata) UpsertViewPeriodDatasetState(_ context.Context, req *pb.UpsertViewPeriodDatasetStateReq, _ ...client.Option) (*pb.UpsertViewPeriodDatasetStateRsp, error) {
	state := req.GetState()
	key := fmt.Sprintf("%s/%s/%s/%s/%d", state.GetSpaceId(), state.GetViewId(), state.GetDatasetId(), state.GetFrequency(), state.GetPeriodTime())
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.states[key]; ok {
		return &pb.UpsertViewPeriodDatasetStateRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}, State: proto.Clone(existing).(*pb.ViewPeriodDatasetState)}, nil
	}
	m.states[key] = proto.Clone(state).(*pb.ViewPeriodDatasetState)
	return &pb.UpsertViewPeriodDatasetStateRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}, State: proto.Clone(state).(*pb.ViewPeriodDatasetState)}, nil
}

func (m *legacyBenchMetadata) ListViewPeriodDatasetStates(_ context.Context, req *pb.ListViewPeriodDatasetStatesReq, _ ...client.Option) (*pb.ListViewPeriodDatasetStatesRsp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var states []*pb.ViewPeriodDatasetState
	for _, state := range m.states {
		if state.GetSpaceId() == req.GetSpaceId() && state.GetViewId() == req.GetViewId() && state.GetFrequency() == req.GetFrequency() && state.GetPeriodTime() == req.GetPeriodTime() {
			states = append(states, proto.Clone(state).(*pb.ViewPeriodDatasetState))
		}
	}
	sort.Slice(states, func(i, j int) bool { return states[i].GetDatasetId() < states[j].GetDatasetId() })
	return &pb.ListViewPeriodDatasetStatesRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}, States: states}, nil
}
