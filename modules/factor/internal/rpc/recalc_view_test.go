package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	publicstoragepb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
)

func TestRecalcWaitsForSyncPointAndUsesViewReadyExecutor(t *testing.T) {
	var mu sync.Mutex
	var order []string
	executor := &recordingViewReadyExecutor{record: func(item string) { mu.Lock(); defer mu.Unlock(); order = append(order, item) }}
	waiter := &recordingViewWaiter{record: func(item string) { mu.Lock(); defer mu.Unlock(); order = append(order, item) }}
	metadata := &recalcViewMetadataClient{recordingFactorMetadataClient: newRecordingFactorMetadataClient("space", "factor")}
	db := openRPCTestDB(t)
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{FactorID: "factor", Name: "factor", SourceHash: "hash", InputColumns: []string{"close"}, Outputs: []string{"score"}, LookbackPeriods: 1, Status: domain.FactorStatusEnabled}))
	require.NoError(t, db.Bindings().Upsert(context.Background(), domain.FactorBinding{BindingID: "binding", FactorID: "factor", SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "factor_result", ResultViewID: "factor_result_view", Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled}))
	svc := NewWithRuntime(db, nil, WithMetadataSync(registry.NewMetadataSync(metadata, nil)), WithViewReadyExecutor(executor, waiter))
	start := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC)
	rsp, err := svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{RequestId: "request-1", SyncRequestId: "import-1", FactorId: "factor", SpaceId: "space", SourceViewId: "source_view", SubjectId: "BTC", Freq: "1m", StartTime: start.Format(time.RFC3339Nano), EndTime: start.Add(2 * time.Minute).Format(time.RFC3339Nano)})
	require.NoError(t, err)
	require.EqualValues(t, 0, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Equal(t, []string{"wait", "execute", "execute"}, order)
	require.Equal(t, []string{"bars", "funding"}, waiter.datasetIDs)
	require.Len(t, executor.triggerIDs, 2)
	require.NotEqual(t, executor.triggerIDs[0], executor.triggerIDs[1])
}

func TestRecalcRejectsTimesOutsideStoragePeriodBoundaries(t *testing.T) {
	db := openRPCTestDB(t)
	svc := NewWithRuntime(db, nil)
	start := time.Date(2026, 8, 9, 1, 0, 0, 500_000_000, time.UTC)
	rsp, err := svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{
		RequestId: "request-misaligned", SpaceId: "space", SourceViewId: "source_view",
		SubjectId: "BTC", Freq: "1m", StartTime: start.Format(time.RFC3339Nano),
		EndTime: start.Add(time.Minute).Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	require.NotZero(t, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "Storage period boundary")
}

type recordingViewReadyExecutor struct {
	triggerIDs []string
	record     func(string)
}

func (e *recordingViewReadyExecutor) ExecuteSelected(_ context.Context, _, triggerID, _ string, _ *publicstoragepb.ViewSourcePeriodReady) error {
	e.record("execute")
	e.triggerIDs = append(e.triggerIDs, triggerID)
	return nil
}

type recordingViewWaiter struct {
	datasetIDs []string
	record     func(string)
}

func (w *recordingViewWaiter) WaitViewSyncPoint(_ context.Context, _, _, _ string, datasetIDs []string) error {
	w.record("wait")
	w.datasetIDs = append([]string(nil), datasetIDs...)
	return nil
}

type recalcViewMetadataClient struct{ *recordingFactorMetadataClient }

func (c *recalcViewMetadataClient) GetView(context.Context, *storagepb.GetViewReq) (*storagepb.GetViewRsp, error) {
	return &storagepb.GetViewRsp{RetInfo: success(), View: &storagepb.View{ViewId: "source_view", PrimaryDatasetId: "bars", DatasetIds: []string{"funding", "bars"}}}, nil
}
func (c *recalcViewMetadataClient) CreateView(context.Context, *storagepb.CreateViewReq) (*storagepb.CreateViewRsp, error) {
	return &storagepb.CreateViewRsp{RetInfo: success()}, nil
}
func (c *recalcViewMetadataClient) UpsertViewColumn(context.Context, *storagepb.UpsertViewColumnReq) (*storagepb.UpsertViewColumnRsp, error) {
	return &storagepb.UpsertViewColumnRsp{RetInfo: success()}, nil
}
