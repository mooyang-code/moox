package resample

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type runnerSource struct {
	subjects     []domain.DatasetSubject
	keepDuration string
}

func (s runnerSource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{DataSourceID: "crypto_market", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1m"}, Attributes: map[string]string{"market_type": "spot"}, KeepDuration: s.keepDuration}, nil
}
func (s runnerSource) ListSubjects(context.Context, string, string, string) ([]domain.DatasetSubject, error) {
	return s.subjects, nil
}

func TestRunnerTickPlansAndProcessesRealtimeBucket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.db")
	db, err := store.Open(&store.Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	params := `{"provider":"moox","market_type":"spot","source_dataset_id":"source_bars","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"spot_kline_derived_5m","target_frequency":"5m","alignment":"epoch_utc","settle_delay_ms":0}`
	require.NoError(t, db.TaskRules().Create(context.Background(), domain.TaskRule{SpaceID: "crypto", RuleID: "rule-5m", DataType: "kline_resample", Provider: "moox", MarketType: "spot", CollectParams: params, PrepareState: domain.PrepareStateReady, Enabled: true}))
	start := time.Unix(300, 0).UTC()
	now := start.Add(5 * time.Minute)
	fake := &fakePrimary{}
	for i := 0; i < 5; i++ {
		at := start.Add(time.Duration(i) * time.Minute)
		fields := []*storagepb.FieldValue{}
		for _, item := range []struct {
			name  string
			value float64
		}{{"open", 1}, {"high", 2}, {"low", 0.5}, {"close", 1.5}, {"volume", 1}, {"quote_volume", 2}} {
			fields = append(fields, &storagepb.FieldValue{FieldId: item.name, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: item.value}}})
		}
		fields = append(fields, &storagepb.FieldValue{FieldId: "trade_num", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: 1}}})
		fake.rows = append(fake.rows, &storagepb.RowFieldValues{Key: rowKey("crypto", "source_bars", "BTC", "1m", at, "venue:binance"), Fields: fields})
	}
	runner := &Runner{Rules: db.TaskRules(), Instances: db.TaskInstances(), Source: runnerSource{subjects: []domain.DatasetSubject{{SubjectID: "BTC", Status: "active"}}}, Primary: fake, Config: RunnerConfig{SpaceID: "crypto", WorkerConcurrency: 1, WorkerJobTimeout: time.Second, RepairLookbackBuckets: 0}}
	require.NoError(t, runner.Tick(context.Background(), now))
	require.Len(t, fake.writes, 1)
	instances, _, err := db.TaskInstances().List(context.Background(), store.TaskInstanceFilter{SpaceID: "crypto", RuleID: "rule-5m", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, instances, 1)
}

func TestEnsureReadinessForClaimsUsesClaimedCursor(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	params := `{"provider":"moox","market_type":"spot","source_dataset_id":"source_bars","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"spot_kline_derived_5m","target_frequency":"5m","alignment":"epoch_utc"}`
	rule := domain.TaskRule{SpaceID: "crypto", RuleID: "rule-5m", DataType: "kline_resample", Provider: "moox", MarketType: "spot", CollectParams: params, PrepareState: domain.PrepareStateReady, Enabled: true}
	require.NoError(t, db.TaskRules().Create(context.Background(), rule))
	cursor := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	result := domain.NewResampleTaskResult(cursor)
	encoded, err := result.Marshal()
	require.NoError(t, err)
	instance := domain.TaskInstance{SpaceID: "crypto", TaskID: "task-btc", RuleID: rule.RuleID, Provider: "moox", MarketType: "spot", DataType: "kline_resample", DatasetID: "spot_kline_derived_5m", SubjectID: "BTC", Frequency: "5m", TaskParams: params, Result: encoded}
	require.NoError(t, db.TaskInstances().UpsertMany(context.Background(), []domain.TaskInstance{instance}))
	stored, err := db.TaskInstances().Get(context.Background(), "crypto", "task-btc")
	require.NoError(t, err)
	claimResult, err := domain.ParseResampleTaskResult(stored.Result)
	require.NoError(t, err)
	claimResult.ActiveOrigin = domain.ResampleOriginRealtime
	claimResult.ActiveBucket = &cursor
	runner := &Runner{Instances: db.TaskInstances(), Readiness: db.PeriodReadiness()}
	require.NoError(t, runner.ensureReadinessForClaims(context.Background(), []store.ResampleTaskClaim{{Instance: stored, Result: claimResult}}, []domain.TaskRule{rule}))
	require.NoError(t, db.PeriodReadiness().MarkSubjectSuccess(context.Background(), domain.PeriodKey{SpaceID: "crypto", DatasetID: "spot_kline_derived_5m", Frequency: "5m", PeriodTime: cursor}, "BTC", localResampleFunction, writeSource, time.Now().UTC()))
	reports, err := db.PeriodReadiness().FinalizeDue(context.Background(), time.Now().UTC(), 10)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, cursor, reports[0].Readiness.PeriodTime)
}

func TestResampleBackfillSyncIgnoresSubjectAddedAfterRequestStart(t *testing.T) {
	started := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	result := domain.NewResampleTaskResult(started)
	result.Backfill = &domain.ResampleBackfill{RequestID: "request-1", Start: started, End: started.Add(time.Hour), NextBucket: started.Add(time.Hour), State: domain.ResampleBackfillSyncing}
	encoded, err := result.Marshal()
	require.NoError(t, err)
	requestID, ready := resampleBackfillSyncRequest([]domain.TaskInstance{
		{Result: encoded},
		{Result: `{"schema_version":1,"state":"idle","state_version":0}`},
	})
	require.True(t, ready)
	require.Equal(t, "request-1", requestID)
}

type syncPointPrimary struct {
	fakePrimary
	appendCalls []string
	waitReq     *storagepb.WaitViewSyncPointReq
}

func (s *syncPointPrimary) AppendDatasetSyncPoint(_ context.Context, spaceID, datasetID, requestID, source string) error {
	s.appendCalls = append(s.appendCalls, spaceID+"/"+datasetID+"/"+requestID+"/"+source)
	return nil
}

func (s *syncPointPrimary) WaitViewSyncPoint(_ context.Context, req *storagepb.WaitViewSyncPointReq) (*storagepb.WaitViewSyncPointRsp, error) {
	s.waitReq = req
	return &storagepb.WaitViewSyncPointRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Ready: true}, nil
}

func TestCompleteBackfillWaitsForViewFenceBeforeSyncing(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	params := `{"provider":"moox","market_type":"spot","source_dataset_id":"source_bars","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"spot_kline_derived_5m","target_frequency":"5m","alignment":"epoch_utc"}`
	require.NoError(t, db.TaskRules().Create(context.Background(), domain.TaskRule{SpaceID: "crypto", RuleID: "rule-5m", DataType: "kline_resample", Provider: "moox", MarketType: "spot", CollectParams: params, PrepareState: domain.PrepareStateReady, Enabled: true}))
	started := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	result := domain.NewResampleTaskResult(started)
	result.Backfill = &domain.ResampleBackfill{RequestID: "request-1", Start: started, End: started.Add(time.Hour), NextBucket: started.Add(time.Hour), State: domain.ResampleBackfillSyncing}
	encoded, err := result.Marshal()
	require.NoError(t, err)
	require.NoError(t, db.TaskInstances().UpsertMany(context.Background(), []domain.TaskInstance{{SpaceID: "crypto", TaskID: "task-btc", RuleID: "rule-5m", Provider: "moox", MarketType: "spot", DataType: "kline_resample", DatasetID: "spot_kline_derived_5m", SubjectID: "BTC", Frequency: "5m", Result: encoded}}))
	primary := &syncPointPrimary{}
	runner := &Runner{Instances: db.TaskInstances(), Primary: primary}
	rule, err := db.TaskRules().GetByRuleID(context.Background(), "crypto", "rule-5m")
	require.NoError(t, err)
	require.NoError(t, runner.completeBackfills(context.Background(), []domain.TaskRule{*rule}))
	require.Equal(t, []string{"crypto/spot_kline_derived_5m/request-1/catchup"}, primary.appendCalls)
	require.NotNil(t, primary.waitReq)
	require.Equal(t, "spot_kline_derived_5m_view", primary.waitReq.GetViewId())
	require.Equal(t, "request-1", primary.waitReq.GetRequestId())
	instance, err := db.TaskInstances().Get(context.Background(), "crypto", "task-btc")
	require.NoError(t, err)
	updated, err := domain.ParseResampleTaskResult(instance.Result)
	require.NoError(t, err)
	require.Equal(t, domain.ResampleBackfillComplete, updated.Backfill.State)
}

func TestSourceRetentionExpiredIsTerminal(t *testing.T) {
	source := runnerSource{keepDuration: "1h"}
	expired, reason := sourceRetentionExpired(context.Background(), source, "crypto", "source", time.Now().UTC().Add(-2*time.Hour))
	require.True(t, expired)
	require.Contains(t, reason, "retention expired")
}

func TestChooseRepairBucketUsesDurableWindowCursor(t *testing.T) {
	target := 90 * time.Minute
	oldest := time.Unix(0, 0).UTC()
	latest := oldest.Add(3 * target)

	assert.Equal(t, oldest, chooseRepairBucket(domain.ResampleTaskResult{}, oldest, latest, target))
	middle := latest.Add(-target)
	result := domain.ResampleTaskResult{RepairNextBucket: &middle}
	assert.Equal(t, middle, chooseRepairBucket(result, oldest, latest, target))
	stale := oldest.Add(-target)
	result.RepairNextBucket = &stale
	assert.Equal(t, oldest, chooseRepairBucket(result, oldest, latest, target))
	unaligned := oldest.Add(time.Minute)
	result.RepairNextBucket = &unaligned
	assert.Equal(t, oldest, chooseRepairBucket(result, oldest, latest, target))
}

func TestRepairScanCursorAdvancesAcrossSkippedTimerTicks(t *testing.T) {
	cursor := 0
	starts := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		start := repairScanStart(cursor, 4)
		starts = append(starts, start)
		cursor = repairScanAdvance(start, 1, 4)
	}
	assert.Equal(t, []int{0, 1, 2, 3}, starts)
}
