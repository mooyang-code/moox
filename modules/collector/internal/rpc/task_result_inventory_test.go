package rpc

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

func TestTaskResultMarketCalendarContract(t *testing.T) {
	calendarID, timezone, sessions, err := taskResultMarketCalendar("crypto")
	require.NoError(t, err)
	require.Empty(t, calendarID)
	require.Equal(t, "UTC", timezone)
	require.Empty(t, sessions)

	calendarID, timezone, sessions, err = taskResultMarketCalendar("stockcn")
	require.NoError(t, err)
	require.Equal(t, "cn_stock", calendarID)
	require.Equal(t, "Asia/Shanghai", timezone)
	require.Equal(t, []string{"09:30-11:30", "13:00-15:00"}, sessions)
}

func TestGetTaskResultInventoryPagesOwnedResultsAndRepresentsLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openCollectorTestStore(t)
	now := time.Date(2026, 10, 3, 10, 5, 0, 0, time.UTC)
	activeIDs := taskresult.ResultIDs("task-active")
	resampleIDs := taskresult.ResultIDs("task-resample")
	disabledIDs := taskresult.ResultIDs("task-disabled")
	tasks := []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "task-active", TaskName: "active", DataType: "kline", Enabled: true, PrepareState: domain.PrepareStateReady, CollectParams: `{"provider":"binance","market_type":"spot","market_id":"crypto","target_dataset_id":"bars","frequency":"1m","output_fields":["open","high"]}`, ResultDatasetID: activeIDs.DatasetID},
		{SpaceID: "crypto", TaskID: "task-resample", TaskName: "resample", DataType: "kline_resample", Enabled: true, PrepareState: domain.PrepareStateReady, CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"derived","target_frequency":"5m","alignment":"epoch_utc"}`, ResultDatasetID: resampleIDs.DatasetID},
		{SpaceID: "crypto", TaskID: "task-disabled", TaskName: "disabled", DataType: "kline", Enabled: false, PrepareState: domain.PrepareStatePending, CollectParams: `{"provider":"binance","market_type":"spot","market_id":"crypto","target_dataset_id":"bars","frequency":"1m","output_fields":["close"]}`, ResultDatasetID: disabledIDs.DatasetID},
		{SpaceID: "crypto", TaskID: "task-factor", TaskName: "factor", DataType: "factor", Enabled: true},
	}
	for _, task := range tasks {
		require.NoError(t, db.Tasks().Create(ctx, task))
	}
	metadata := &taskResultMetadataFake{datasets: make(map[string]*storagepb.Dataset), indexedTo: make(map[string]string)}
	for taskID, ids := range map[string]taskresult.IDs{
		"task-active":   activeIDs,
		"task-resample": resampleIDs,
		"task-disabled": disabledIDs,
	} {
		owned := newTaskResultMetadataFake(ids, taskID)
		metadata.indexedTo[ids.DatasetID] = now.Add(-time.Minute).Format(time.RFC3339)
		metadata.datasets[ids.DatasetID] = owned.datasets[ids.DatasetID]
	}
	service := &Service{
		persistence:   db,
		taskRepo:      db.Tasks(),
		resultManager: taskresult.NewManagerWithAPI(metadata),
	}

	first, err := service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{SpaceId: "crypto", Page: &commonpb.Page{Page: 1, Size: 2}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, first.GetRetInfo().GetCode())
	require.Len(t, first.GetEntries(), 2)
	require.NotEmpty(t, first.GetSnapshotId())
	require.Equal(t, uint32(3), first.GetPage().GetTotal())
	entry := first.GetEntries()[0]
	require.Equal(t, "task-active", entry.GetTaskId())
	require.Equal(t, activeIDs.DatasetID, entry.GetDatasetId())
	require.True(t, entry.GetOwnershipVerified())
	require.Equal(t, "1m", entry.GetFrequency())
	// 直接行情任务的周期由 Storage 裁决：清单不再带最近终结周期，只带允许迟到时间。
	require.Empty(t, entry.GetLatestCompletedPeriod())
	require.Empty(t, entry.GetLatestCompletedStatus())
	require.EqualValues(t, 15, entry.GetAllowedLatenessSeconds())
	require.Equal(t, now.Add(-time.Minute).Format(time.RFC3339), entry.GetViewLastDataTime())
	require.NotEmpty(t, entry.GetObservedAt())
	concurrent, err := service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{SpaceId: "crypto", Page: &commonpb.Page{Page: 1, Size: 2}})
	require.NoError(t, err)
	require.NotEqual(t, first.GetSnapshotId(), concurrent.GetSnapshotId())

	second, err := service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{
		SpaceId: "crypto", Page: &commonpb.Page{Page: 2, Size: 2}, SnapshotId: first.GetSnapshotId(),
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, second.GetRetInfo().GetCode())
	require.Len(t, second.GetEntries(), 1)
	require.Equal(t, "task-disabled", second.GetEntries()[0].GetTaskId())
	require.False(t, second.GetEntries()[0].GetEnabled())
	require.False(t, second.GetEntries()[0].GetOwnershipVerified(), "停用任务不检查结果归属")
	require.Equal(t, "pending", second.GetEntries()[0].GetResultStatus())
	service.inventorySnapshotMu.Lock()
	_, firstSnapshotRetained := service.inventorySnapshots[first.GetSnapshotId()]
	service.inventorySnapshotMu.Unlock()
	require.False(t, firstSnapshotRetained, "final page should release the snapshot slot")

	// Deletion is expressed by absence from a new complete snapshot, not by
	// synthesizing a tombstone that could outlive the authoritative task row.
	require.NoError(t, db.Tasks().DeleteByTaskID(ctx, "crypto", "task-disabled"))
	updated, err := service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{SpaceId: "crypto", Page: &commonpb.Page{Page: 1, Size: 2}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, updated.GetRetInfo().GetCode())
	require.Equal(t, uint32(2), updated.GetPage().GetTotal())
}

func TestGetTaskResultInventoryCapacityIsPerSpaceAndRejectsBeforeMetadataFanout(t *testing.T) {
	ctx := context.Background()
	db := openCollectorTestStore(t)
	ids := taskresult.ResultIDs("task-owned")
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-owned", TaskName: "owned", DataType: "kline", Enabled: true,
		PrepareState: domain.PrepareStateReady, CollectParams: `{"provider":"binance","market_type":"spot","market_id":"crypto","frequency":"1m"}`,
		ResultDatasetID: ids.DatasetID,
	}
	require.NoError(t, db.Tasks().Create(ctx, task))
	metadata := newTaskResultMetadataFake(ids, task.TaskID)
	service := &Service{
		persistence:        db,
		taskRepo:           db.Tasks(),
		resultManager:      taskresult.NewManagerWithAPI(metadata),
		inventorySnapshots: make(map[string]*taskResultInventorySnapshot, maxTaskResultInventorySnapshotsTotal),
	}
	for index := 0; index < maxTaskResultInventorySnapshotsPerSpace; index++ {
		id := fmt.Sprintf("snapshot-%d", index)
		service.inventorySnapshots[id] = &taskResultInventorySnapshot{
			id: id, spaceID: "crypto", pageSize: 10, observed: time.Now().UTC(), expiresAt: time.Now().UTC().Add(time.Minute),
		}
	}

	response, err := service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{SpaceId: "crypto", Page: &commonpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, response.GetRetInfo().GetCode())
	require.Zero(t, metadata.getDatasets, "full snapshot capacity must be rejected before Storage metadata inspection")

	// Exhausting one Space's allowance cannot block another Space.
	stockIDs := taskresult.ResultIDs("task-stock")
	stockTask := domain.CollectionTask{
		SpaceID: "stockcn", TaskID: "task-stock", TaskName: "stock", DataType: "kline", Enabled: true,
		PrepareState: domain.PrepareStatePending, CollectParams: `{"provider":"tdx","market_type":"stock","market_id":"stockcn","frequency":"1d"}`,
		ResultDatasetID: stockIDs.DatasetID,
	}
	require.NoError(t, db.Tasks().Create(ctx, stockTask))
	response, err = service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{SpaceId: "stockcn", Page: &commonpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
	require.Len(t, response.GetEntries(), 1)
	require.Equal(t, "stockcn", response.GetEntries()[0].GetSpaceId())
}

func TestGetTaskResultInventoryRejectsUnownedResultsAndMismatchedSnapshot(t *testing.T) {
	ctx := context.Background()
	db := openCollectorTestStore(t)
	ids := taskresult.ResultIDs("task-owned")
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-owned", TaskName: "owned", DataType: "kline", Enabled: true,
		PrepareState: domain.PrepareStateReady, CollectParams: `{"provider":"binance","market_type":"spot","market_id":"crypto","frequency":"1m"}`,
		ResultDatasetID: ids.DatasetID,
	}
	require.NoError(t, db.Tasks().Create(ctx, task))
	metadata := newTaskResultMetadataFake(ids, "another-task")
	service := &Service{persistence: db, taskRepo: db.Tasks(), resultManager: taskresult.NewManagerWithAPI(metadata)}

	response, err := service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{SpaceId: "crypto", Page: &commonpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, response.GetRetInfo().GetCode())
	require.Empty(t, response.GetEntries())
	require.Empty(t, service.inventorySnapshotBuilds, "failed snapshot build must release its reserved slot")

	response, err = service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{SpaceId: "crypto", Page: &commonpb.Page{Page: 2, Size: 10}, SnapshotId: "unknown"})
	require.NoError(t, err)
	require.NotEqual(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
}

func TestGetTaskResultInventoryRequiresSpaceAndRejectsCrossSpaceSnapshotToken(t *testing.T) {
	ctx := context.Background()
	db := openCollectorTestStore(t)
	for _, task := range []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "crypto-a", TaskName: "crypto a", DataType: "kline", Enabled: true, PrepareState: domain.PrepareStatePending, CollectParams: `{"provider":"binance","frequency":"1m"}`},
		{SpaceID: "crypto", TaskID: "crypto-b", TaskName: "crypto b", DataType: "kline", Enabled: true, PrepareState: domain.PrepareStatePending, CollectParams: `{"provider":"binance","frequency":"1m"}`},
		{SpaceID: "stockcn", TaskID: "stock-a", TaskName: "stock a", DataType: "kline", Enabled: true, PrepareState: domain.PrepareStatePending, CollectParams: `{"provider":"tdx","frequency":"1d"}`},
	} {
		require.NoError(t, db.Tasks().Create(ctx, task))
	}
	service := &Service{persistence: db, taskRepo: db.Tasks()}

	missingSpace, err := service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{Page: &commonpb.Page{Page: 1, Size: 1}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, missingSpace.GetRetInfo().GetCode())

	first, err := service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{SpaceId: "crypto", Page: &commonpb.Page{Page: 1, Size: 1}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, first.GetRetInfo().GetCode())
	require.Equal(t, uint32(2), first.GetPage().GetTotal(), "first-page inventory must contain only the requested Space")
	require.Equal(t, "crypto", first.GetEntries()[0].GetSpaceId())

	crossSpacePage, err := service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{
		SpaceId: "stockcn", Page: &commonpb.Page{Page: 2, Size: 1}, SnapshotId: first.GetSnapshotId(),
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, crossSpacePage.GetRetInfo().GetCode())
	require.Empty(t, crossSpacePage.GetEntries())

	second, err := service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{
		SpaceId: "crypto", Page: &commonpb.Page{Page: 2, Size: 1}, SnapshotId: first.GetSnapshotId(),
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, second.GetRetInfo().GetCode())
	require.Equal(t, "crypto", second.GetEntries()[0].GetSpaceId())

	stock, err := service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{SpaceId: "stockcn", Page: &commonpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, stock.GetRetInfo().GetCode())
	require.Equal(t, uint32(1), stock.GetPage().GetTotal())
	require.Equal(t, "stockcn", stock.GetEntries()[0].GetSpaceId())
}

func TestGetTaskResultInventoryMetadataFanoutIsBoundedAndOrdered(t *testing.T) {
	const taskCount = 48
	service, metadata, expectedTaskIDs := newTaskResultInventoryConcurrentService(t, taskCount, 3*time.Millisecond)

	response, err := service.GetTaskResultInventory(context.Background(), &pb.GetTaskResultInventoryReq{
		SpaceId: "crypto", Page: &commonpb.Page{Page: 1, Size: maxTaskResultInventoryPage},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
	require.Len(t, response.GetEntries(), taskCount)
	gotTaskIDs := make([]string, len(response.GetEntries()))
	for index, entry := range response.GetEntries() {
		gotTaskIDs[index] = entry.GetTaskId()
	}
	require.Equal(t, expectedTaskIDs, gotTaskIDs, "concurrent inspection must retain repository order")
	maximum := metadata.maximumConcurrency()
	require.Greater(t, maximum, 1, "metadata inspection should fan out instead of serializing Storage calls")
	require.LessOrEqual(t, maximum, taskResultInventoryMetadataConcurrency, "Storage fanout must stay bounded")
}

func TestGetTaskResultInventoryMetadataFanoutCancelsOnRequestDeadline(t *testing.T) {
	service, metadata, _ := newTaskResultInventoryConcurrentService(t, 100, 0)
	metadata.waitForCancel = true
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan *pb.GetTaskResultInventoryRsp, 1)
	go func() {
		response, _ := service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{
			SpaceId: "crypto", Page: &commonpb.Page{Page: 1, Size: 10},
		})
		result <- response
	}()

	select {
	case <-metadata.firstRequestStarted:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("Storage metadata request did not start")
	}
	select {
	case response := <-result:
		require.Equal(t, pb.ErrorCode_INNER_ERR, response.GetRetInfo().GetCode())
	case <-time.After(time.Second):
		t.Fatal("inventory build did not stop after request cancellation")
	}
	service.inventorySnapshotMu.Lock()
	remainingBuilds := service.inventorySnapshotBuilds["crypto"]
	service.inventorySnapshotMu.Unlock()
	require.Zero(t, remainingBuilds, "canceled snapshot must release its per-Space build reservation")
	require.LessOrEqual(t, metadata.requestCount(), taskResultInventoryMetadataConcurrency,
		"request cancellation must prevent an unbounded tail of metadata calls")
}

func newTaskResultInventoryConcurrentService(t *testing.T, taskCount int, delay time.Duration) (*Service, *taskResultInventoryConcurrentMetadataFake, []string) {
	t.Helper()
	ctx := context.Background()
	db := openCollectorTestStore(t)
	base := newTaskResultMetadataFake(taskresult.IDs{}, "")
	expected := make([]string, 0, taskCount)
	for index := 0; index < taskCount; index++ {
		taskID := fmt.Sprintf("task-%03d", index)
		ids := taskresult.ResultIDs(taskID)
		task := domain.CollectionTask{
			SpaceID: "crypto", TaskID: taskID, TaskName: taskID, DataType: "kline", Enabled: true,
			PrepareState:    domain.PrepareStateReady,
			CollectParams:   `{"provider":"binance","market_type":"spot","market_id":"crypto","target_dataset_id":"bars","frequency":"1m"}`,
			ResultDatasetID: ids.DatasetID,
		}
		require.NoError(t, db.Tasks().Create(ctx, task))
		owned := newTaskResultMetadataFake(ids, taskID)
		base.datasets[ids.DatasetID] = owned.datasets[ids.DatasetID]
		expected = append(expected, taskID)
	}
	metadata := &taskResultInventoryConcurrentMetadataFake{
		taskResultMetadataFake: base, delay: delay, firstRequestStarted: make(chan struct{}),
	}
	return &Service{
		persistence: db, taskRepo: db.Tasks(),
		resultManager: taskresult.NewManagerWithAPI(metadata),
	}, metadata, expected
}

type taskResultInventoryConcurrentMetadataFake struct {
	*taskResultMetadataFake
	mu                  sync.Mutex
	baseMu              sync.Mutex
	active              int
	maximum             int
	requests            int
	delay               time.Duration
	waitForCancel       bool
	firstRequestStarted chan struct{}
}

func (f *taskResultInventoryConcurrentMetadataFake) beginRequest() {
	f.mu.Lock()
	f.requests++
	f.active++
	if f.active > f.maximum {
		f.maximum = f.active
	}
	if f.requests == 1 && f.firstRequestStarted != nil {
		close(f.firstRequestStarted)
	}
	f.mu.Unlock()
}

func (f *taskResultInventoryConcurrentMetadataFake) endRequest() {
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
}

func (f *taskResultInventoryConcurrentMetadataFake) GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	f.beginRequest()
	defer f.endRequest()
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	f.baseMu.Lock()
	defer f.baseMu.Unlock()
	return f.taskResultMetadataFake.GetDataset(ctx, req)
}

func (f *taskResultInventoryConcurrentMetadataFake) wait(ctx context.Context) error {
	if f.waitForCancel {
		<-ctx.Done()
		return ctx.Err()
	}
	if f.delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(f.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func (f *taskResultInventoryConcurrentMetadataFake) maximumConcurrency() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maximum
}

func (f *taskResultInventoryConcurrentMetadataFake) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

// 未登记的市场报错：不当成连续交易，也没有允许迟到时间的默认值。
func TestTaskResultRejectsUnregisteredMarket(t *testing.T) {
	_, _, _, err := taskResultMarketCalendar("no_such_space")
	require.Error(t, err)
	_, err = allowedLatenessSeconds(nil, "no_such_space")
	require.Error(t, err)
	seconds, err := allowedLatenessSeconds(nil, "stockcn")
	require.NoError(t, err)
	require.Equal(t, 90, seconds)
	seconds, err = allowedLatenessSeconds(nil, "crypto")
	require.NoError(t, err)
	require.Equal(t, 15, seconds)
}

// 清单带上数据集的历史补齐状态：补齐中的标 history_seeding，已结束的带结束时刻，没有补齐记录的都不带；
// 读不到补齐记录时清单照常返回、都不带。
func TestGetTaskResultInventoryCarriesHistorySeeds(t *testing.T) {
	ctx := context.Background()
	db := openCollectorTestStore(t)
	finished := time.Date(2026, 11, 25, 9, 45, 0, 0, time.UTC)
	ids := map[string]taskresult.IDs{}
	for _, taskID := range []string{"task-seeding", "task-seeded", "task-plain"} {
		ids[taskID] = taskresult.ResultIDs(taskID)
		require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
			SpaceID: "stockus", TaskID: taskID, TaskName: taskID, DataType: "kline", Enabled: false, PrepareState: domain.PrepareStatePending,
			CollectParams:   `{"provider":"cnbc","market_type":"equity","market_id":"stockus","frequency":"5m"}`,
			ResultDatasetID: ids[taskID].DatasetID,
		}))
	}
	ops := &seedOps{views: []klinecheck.SeedView{
		{HistorySeed: store.HistorySeed{SpaceID: "stockus", DatasetID: ids["task-seeding"].DatasetID, Frequency: "5m", Status: store.SeedPreparing}},
		{HistorySeed: store.HistorySeed{SpaceID: "stockus", DatasetID: ids["task-seeded"].DatasetID, Frequency: "5m", Status: store.SeedDone, FinishedAt: &finished}},
		{HistorySeed: store.HistorySeed{SpaceID: "stockus", DatasetID: ids["task-plain"].DatasetID, Frequency: "1m", Status: store.SeedRunning}},
	}}
	service := &Service{persistence: db, taskRepo: db.Tasks(), klineOps: ops}
	rsp, err := service.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{SpaceId: "stockus", Page: &commonpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "stockus", ops.skipArgs[0])
	require.False(t, ops.onlyOpen, "要已结束的记录，才知道结束时刻")
	byTask := map[string]*pb.TaskResultInventoryEntry{}
	for _, entry := range rsp.GetEntries() {
		byTask[entry.GetTaskId()] = entry
	}
	require.True(t, byTask["task-seeding"].GetHistorySeeding(), "拼作业也算补齐中")
	require.Zero(t, byTask["task-seeding"].GetHistorySeedFinishedAt())
	require.False(t, byTask["task-seeded"].GetHistorySeeding())
	require.Equal(t, finished.UnixMilli(), byTask["task-seeded"].GetHistorySeedFinishedAt())
	require.False(t, byTask["task-plain"].GetHistorySeeding(), "频率不同的补齐记录不算")
	require.Zero(t, byTask["task-plain"].GetHistorySeedFinishedAt())

	failing := &Service{persistence: db, taskRepo: db.Tasks(), klineOps: &failingSeedOps{}}
	rsp, err = failing.GetTaskResultInventory(ctx, &pb.GetTaskResultInventoryReq{SpaceId: "stockus", Page: &commonpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetEntries(), 3)
	for _, entry := range rsp.GetEntries() {
		require.False(t, entry.GetHistorySeeding())
	}
}

type failingSeedOps struct{ KlineOps }

func (failingSeedOps) SeedViews(context.Context, string, bool) ([]klinecheck.SeedView, error) {
	return nil, fmt.Errorf("库暂时不可用")
}
