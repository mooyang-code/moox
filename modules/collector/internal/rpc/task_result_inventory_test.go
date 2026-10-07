package rpc

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

func TestTaskResultMarketCalendarContract(t *testing.T) {
	calendarID, timezone, sessions := taskResultMarketCalendar("crypto")
	require.Empty(t, calendarID)
	require.Equal(t, "UTC", timezone)
	require.Empty(t, sessions)

	calendarID, timezone, sessions = taskResultMarketCalendar("stockcn")
	require.Equal(t, "cn_stock", calendarID)
	require.Equal(t, "Asia/Shanghai", timezone)
	require.Equal(t, []string{"09:30-11:30", "13:00-15:00"}, sessions)
}

func TestGetTaskResultInventoryPagesOwnedResultsAndRepresentsLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openCollectorTestStore(t)
	now := time.Date(2026, 10, 3, 10, 5, 0, 0, time.UTC)
	activeIDs := taskresult.ResultIDsForTask("crypto", "task-active", "kline", "1m")
	resampleIDs := taskresult.ResultIDsForTask("crypto", "task-resample", "kline_resample", "5m")
	disabledIDs := taskresult.ResultIDsForTask("crypto", "task-disabled", "kline", "1m")
	tasks := []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "task-active", TaskName: "active", DataType: "kline", Enabled: true, PrepareState: domain.PrepareStateReady, CollectParams: `{"provider":"binance","market_type":"spot","market_id":"crypto","target_dataset_id":"bars","frequency":"1m","output_fields":["open","high"]}`, ResultDatasetID: activeIDs.DatasetID, ResultViewID: activeIDs.ViewID},
		{SpaceID: "crypto", TaskID: "task-resample", TaskName: "resample", DataType: "kline_resample", Enabled: true, PrepareState: domain.PrepareStateReady, CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"derived","target_frequency":"5m","alignment":"epoch_utc"}`, ResultDatasetID: resampleIDs.DatasetID, ResultViewID: resampleIDs.ViewID},
		{SpaceID: "crypto", TaskID: "task-disabled", TaskName: "disabled", DataType: "kline", Enabled: false, PrepareState: domain.PrepareStatePending, CollectParams: `{"provider":"binance","market_type":"spot","market_id":"crypto","target_dataset_id":"bars","frequency":"1m","output_fields":["close"]}`, ResultDatasetID: disabledIDs.DatasetID, ResultViewID: disabledIDs.ViewID},
		{SpaceID: "crypto", TaskID: "task-factor", TaskName: "factor", DataType: "factor", Enabled: true},
	}
	for _, task := range tasks {
		require.NoError(t, db.Tasks().Create(ctx, task))
	}
	_, _, err := db.Tasks().ReplaceTaskSeries(ctx, "crypto", "task-active", []domain.TaskSeries{{
		SubjectID: "BTC-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot",
		ProviderSymbol: "BTCUSDT", SeriesTag: "venue:binance|market:spot|source:spot_http",
	}})
	require.NoError(t, err)
	_, _, err = db.Tasks().ReplaceTaskSeries(ctx, "crypto", "task-resample", []domain.TaskSeries{
		{SubjectID: "BTC-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "BTCUSDT", SeriesTag: "venue:binance"},
		{SubjectID: "ETH-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "ETHUSDT", SeriesTag: "venue:binance"},
	})
	require.NoError(t, err)
	_, _, err = db.Tasks().ReplaceTaskSeries(ctx, "crypto", "task-disabled", []domain.TaskSeries{{
		SubjectID: "SOL-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot",
		ProviderSymbol: "SOLUSDT", SeriesTag: "venue:binance|market:spot|source:spot_http",
	}})
	require.NoError(t, err)
	metadata := &taskResultMetadataFake{datasets: make(map[string]*storagepb.Dataset), views: make(map[string]*storagepb.View)}
	for taskID, ids := range map[string]taskresult.IDs{
		"task-active":   activeIDs,
		"task-resample": resampleIDs,
		"task-disabled": disabledIDs,
	} {
		owned := newTaskResultMetadataFake(ids, taskID)
		owned.views[ids.ViewID].IndexedTo = now.Add(-time.Minute).Format(time.RFC3339)
		metadata.datasets[ids.DatasetID] = owned.datasets[ids.DatasetID]
		metadata.views[ids.ViewID] = owned.views[ids.ViewID]
	}
	periodTime := now.Add(-time.Minute)
	require.NoError(t, db.PeriodStorageStates().ObservePeriodStorageState(ctx, domain.PeriodStorageState{
		Key:        domain.PeriodKey{SpaceID: "crypto", DatasetID: activeIDs.DatasetID, Frequency: "1m", PeriodTime: periodTime},
		SeriesHash: "storage-series-hash", ExpectedCount: 1, DeadlineAt: now,
		Status: domain.PeriodStatusComplete, ConfirmedAt: now,
	}))
	// A newer local resample-readiness row is not authoritative for a direct
	// market-fetch task and must not advance its inventory watermark.
	legacyPeriodTime := periodTime.Add(time.Minute)
	legacyPeriodKey := domain.PeriodKey{SpaceID: "crypto", DatasetID: activeIDs.DatasetID, Frequency: "1m", PeriodTime: legacyPeriodTime}
	_, err = db.PeriodReadiness().EnsurePeriod(ctx, domain.PeriodSeed{
		PeriodKey: legacyPeriodKey, DeadlineAt: now,
		Tasks: []domain.PeriodTaskSeed{{InstanceID: "instance", WriteTargetID: "target", SubjectID: "BTC-USDT"}},
	})
	require.NoError(t, err)
	require.NoError(t, db.PeriodReadiness().MarkSubjectSuccess(ctx, legacyPeriodKey, "BTC-USDT", "", "", now.Add(-30*time.Second)))
	_, err = db.PeriodReadiness().FinalizeDue(ctx, now, 10)
	require.NoError(t, err)
	service := &Service{
		persistence:   db,
		taskRepo:      db.Tasks(),
		resultManager: taskresult.NewManagerWithAPI(metadata, &storagepb.AuthInfo{AppId: "collector"}),
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
	require.Equal(t, activeIDs.ViewID, entry.GetViewId())
	require.True(t, entry.GetOwnershipVerified())
	require.Equal(t, "1m", entry.GetFrequency())
	require.Equal(t, periodTime.Format(time.RFC3339), entry.GetLatestCompletedPeriod())
	require.Equal(t, domain.PeriodStatusComplete, entry.GetLatestCompletedStatus())
	require.Equal(t, now.Add(-time.Minute).Format(time.RFC3339), entry.GetViewLastDataTime())
	require.NotEmpty(t, entry.GetObservedAt())
	require.True(t, entry.GetCanaryCandidateAvailable())
	require.Equal(t, "BTC-USDT", entry.GetSubjectId())
	require.Equal(t, "BTCUSDT", entry.GetProviderSymbol())
	require.Equal(t, "binance", entry.GetProvider())
	require.Equal(t, "spot_http", entry.GetSourceId())
	require.Equal(t, "spot", entry.GetMarketType())
	require.Equal(t, "venue:binance|market:spot|source:spot_http", entry.GetSeriesTag())
	require.Equal(t, uint32(0), entry.GetSeriesIndex())
	require.NotEmpty(t, entry.GetSeriesHash())
	require.Equal(t, uint32(1), entry.GetExpectedCount())
	require.Equal(t, []string{"high", "open"}, entry.GetOutputFields())
	resample := first.GetEntries()[1]
	require.False(t, resample.GetCanaryCandidateAvailable(), "multiple series must not be exposed as one canary candidate")
	require.Empty(t, resample.GetSubjectId())
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
	require.True(t, second.GetEntries()[0].GetOwnershipVerified())
	require.True(t, second.GetEntries()[0].GetCanaryCandidateAvailable(), "disabled task ownership and singleton series remain verifiable")
	require.Equal(t, "SOL-USDT", second.GetEntries()[0].GetSubjectId())
	require.Equal(t, []string{"close"}, second.GetEntries()[0].GetOutputFields())
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
	ids := taskresult.ResultIDsForTask("crypto", "task-owned", "kline", "1m")
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-owned", TaskName: "owned", DataType: "kline", Enabled: true,
		PrepareState: domain.PrepareStateReady, CollectParams: `{"provider":"binance","market_type":"spot","market_id":"crypto","frequency":"1m"}`,
		ResultDatasetID: ids.DatasetID, ResultViewID: ids.ViewID,
	}
	require.NoError(t, db.Tasks().Create(ctx, task))
	metadata := newTaskResultMetadataFake(ids, task.TaskID)
	service := &Service{
		persistence:        db,
		taskRepo:           db.Tasks(),
		resultManager:      taskresult.NewManagerWithAPI(metadata, &storagepb.AuthInfo{AppId: "collector"}),
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
	require.Zero(t, metadata.getViews, "full snapshot capacity must be rejected before Storage metadata inspection")

	// Exhausting one Space's allowance cannot block another Space.
	stockIDs := taskresult.ResultIDsForTask("stockcn", "task-stock", "kline", "1d")
	stockTask := domain.CollectionTask{
		SpaceID: "stockcn", TaskID: "task-stock", TaskName: "stock", DataType: "kline", Enabled: true,
		PrepareState: domain.PrepareStatePending, CollectParams: `{"provider":"tdx","market_type":"stock","market_id":"stockcn","frequency":"1d"}`,
		ResultDatasetID: stockIDs.DatasetID, ResultViewID: stockIDs.ViewID,
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
	ids := taskresult.ResultIDsForTask("crypto", "task-owned", "kline", "1m")
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-owned", TaskName: "owned", DataType: "kline", Enabled: true,
		PrepareState: domain.PrepareStateReady, CollectParams: `{"provider":"binance","market_type":"spot","market_id":"crypto","frequency":"1m"}`,
		ResultDatasetID: ids.DatasetID, ResultViewID: ids.ViewID,
	}
	require.NoError(t, db.Tasks().Create(ctx, task))
	metadata := newTaskResultMetadataFake(ids, "another-task")
	service := &Service{persistence: db, taskRepo: db.Tasks(), resultManager: taskresult.NewManagerWithAPI(metadata, &storagepb.AuthInfo{AppId: "collector"})}

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
		ids := taskresult.ResultIDsForTask("crypto", taskID, "kline", "1m")
		task := domain.CollectionTask{
			SpaceID: "crypto", TaskID: taskID, TaskName: taskID, DataType: "kline", Enabled: true,
			PrepareState:    domain.PrepareStateReady,
			CollectParams:   `{"provider":"binance","market_type":"spot","market_id":"crypto","target_dataset_id":"bars","frequency":"1m"}`,
			ResultDatasetID: ids.DatasetID, ResultViewID: ids.ViewID,
		}
		require.NoError(t, db.Tasks().Create(ctx, task))
		owned := newTaskResultMetadataFake(ids, taskID)
		base.datasets[ids.DatasetID] = owned.datasets[ids.DatasetID]
		base.views[ids.ViewID] = owned.views[ids.ViewID]
		expected = append(expected, taskID)
	}
	metadata := &taskResultInventoryConcurrentMetadataFake{
		taskResultMetadataFake: base, delay: delay, firstRequestStarted: make(chan struct{}),
	}
	return &Service{
		persistence: db, taskRepo: db.Tasks(),
		resultManager: taskresult.NewManagerWithAPI(metadata, &storagepb.AuthInfo{AppId: "collector"}),
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

func (f *taskResultInventoryConcurrentMetadataFake) GetView(ctx context.Context, req *storagepb.GetViewReq) (*storagepb.GetViewRsp, error) {
	f.beginRequest()
	defer f.endRequest()
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	f.baseMu.Lock()
	defer f.baseMu.Unlock()
	return f.taskResultMetadataFake.GetView(ctx, req)
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
