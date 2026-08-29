package rpc

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestValidateResampleBackfillWindowRejectsOpenOrExpiredSourceWindow(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	request := domain.ResampleBackfillRequest{RequestID: "r1", Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour)}
	require.ErrorContains(t, validateResampleBackfillWindow(request, time.Hour, 10*time.Second, "1h", now), "older than source Dataset retention")

	request.Start = now.Add(-2 * time.Hour)
	request.End = now.Add(time.Hour)
	require.ErrorContains(t, validateResampleBackfillWindow(request, time.Hour, 0, "", now), "closed bucket")
}

func TestValidateResampleBackfillWindowAcceptsClosedRetainedWindow(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	request := domain.ResampleBackfillRequest{RequestID: "r1", Start: now.Add(-3 * time.Hour), End: now.Add(-time.Hour)}
	require.NoError(t, validateResampleBackfillWindow(request, time.Hour, 10*time.Second, "24h", now))
}

type validationDatasetSource map[string]storagesource.DatasetInfo

func (s validationDatasetSource) GetDataset(_ context.Context, _ string, datasetID string) (storagesource.DatasetInfo, error) {
	info, ok := s[datasetID]
	if !ok {
		return storagesource.DatasetInfo{}, fmt.Errorf("missing dataset %s", datasetID)
	}
	return info, nil
}

func (validationDatasetSource) ListSubjects(context.Context, string, string, string) ([]domain.DatasetSubject, error) {
	return nil, nil
}

func TestValidateTaskRuleDatasetsRejectsMarketAndFrequencyMismatch(t *testing.T) {
	service := &Service{datasetSrc: validationDatasetSource{
		"symbols": {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_RECORD, Status: "active", Attributes: map[string]string{"market_type": "spot"}},
		"bars":    {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1m"}, Attributes: map[string]string{"market_type": "spot"}},
	}}
	rule := domain.TaskRule{SpaceID: "crypto", DataType: "kline", Provider: "binance", MarketType: "spot", CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"5m"}`}
	require.ErrorContains(t, service.validateTaskRuleDatasets(context.Background(), rule), `does not enable frequency "5m"`)

	rule.CollectParams = `{"provider":"binance","market_type":"swap","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m"}`
	require.ErrorContains(t, service.validateTaskRuleDatasets(context.Background(), rule), "market_type=spot does not match rule market_type=swap")
}

func TestValidateTaskRuleDatasetsRejectsSymbolMarketMismatch(t *testing.T) {
	service := &Service{datasetSrc: validationDatasetSource{
		"symbols": {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_RECORD, Status: "active", Attributes: map[string]string{"market_type": "spot"}},
	}}
	rule := domain.TaskRule{
		SpaceID: "crypto", DataType: "symbol", Provider: "binance", MarketType: "swap",
		CollectParams: `{"provider":"binance","market_type":"swap","symbol_source":"exchange","target_dataset_id":"symbols"}`,
	}
	require.ErrorContains(t, service.validateTaskRuleDatasets(context.Background(), rule), "market_type=spot does not match rule market_type=swap")
}

func TestValidateTaskRuleAcceptsCollectorLocalResampleWithoutCloudRoute(t *testing.T) {
	rule := domain.TaskRule{
		SpaceID: "crypto", RuleID: "resample-1", DataType: "kline_resample", Provider: "moox", MarketType: "spot",
		CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"spot_kline_derived_4h","target_frequency":"4H","alignment":"epoch_utc"}`,
	}
	require.NoError(t, validateTaskRule(rule))
}

func TestValidateTaskRuleRejectsUnsupportedStockHistoryMode(t *testing.T) {
	rule := domain.TaskRule{
		SpaceID: "stock_cn", RuleID: "stock-bars", DataType: "kline", Provider: "stock_cn_multi", MarketType: "equity",
		CollectParams: `{"provider":"stock_cn_multi","market_type":"equity","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"stock_cn_kline","frequency":"1m","history_policy":{"mode":"lookback","lookback":5}}`,
	}
	require.ErrorContains(t, validateTaskRule(rule), "only supports live_only")
}

func TestPreserveTaskRuleCoverageStartOnOrdinaryUpdate(t *testing.T) {
	original := time.Date(2026, 8, 29, 1, 2, 0, 0, time.UTC)
	replacement := original.Add(24 * time.Hour)
	desired := domain.TaskRule{CoverageStartTime: &replacement}
	preserveTaskRuleCoverageStart(domain.TaskRule{CoverageStartTime: &original}, &desired)
	require.NotNil(t, desired.CoverageStartTime)
	require.Equal(t, original, desired.CoverageStartTime.UTC())
}

func TestValidateResampleRuleUpdateLocksIdentityAtEveryPrepareState(t *testing.T) {
	base := domain.TaskRule{
		SpaceID: "crypto", RuleID: "resample-1", DataType: "kline_resample", Provider: "moox", MarketType: "spot", Enabled: true,
		CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1H","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"4H","alignment":"epoch_utc","settle_delay_ms":10000}`,
	}
	for _, state := range []domain.TaskRulePrepareState{domain.PrepareStatePending, domain.PrepareStateWaitingView, domain.PrepareStateReady} {
		t.Run(string(state), func(t *testing.T) {
			existing := base
			existing.PrepareState = state
			mutable := base
			mutable.CollectParams = `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1H","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"4H","alignment":"epoch_utc","settle_delay_ms":20000}`
			require.NoError(t, validateTaskRuleUpdate(existing, mutable))

			changed := mutable
			changed.CollectParams = `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1H","source_series_tag":"venue:okx","target_dataset_id":"target","target_frequency":"4H","alignment":"epoch_utc","settle_delay_ms":20000}`
			require.ErrorContains(t, validateTaskRuleUpdate(existing, changed), "create a new rule")
		})
	}
}

func TestValidateResampleSourceDoesNotFoldMonthIntoMinute(t *testing.T) {
	service := &Service{datasetSrc: validationDatasetSource{
		"source": {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1M"}, Attributes: map[string]string{"market_type": "spot"}},
	}}
	rule := domain.TaskRule{
		SpaceID: "crypto", RuleID: "resample-1", DataType: "kline_resample", Provider: "moox", MarketType: "spot",
		CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"spot_kline_derived_5m","target_frequency":"5m","alignment":"epoch_utc"}`,
	}
	require.ErrorContains(t, service.validateTaskRuleDatasets(context.Background(), rule), `does not enable frequency "1m"`)
}

func TestValidateTaskRuleDatasetsAcceptsExchangeSourceForMooxResample(t *testing.T) {
	service := &Service{datasetSrc: validationDatasetSource{
		"source": {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1H"}, Attributes: map[string]string{"market_type": "spot"}},
	}}
	rule := domain.TaskRule{
		SpaceID: "crypto", RuleID: "resample-1", DataType: "kline_resample", Provider: "moox", MarketType: "spot",
		CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1H","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"4H","alignment":"epoch_utc"}`,
	}
	require.NoError(t, service.validateTaskRuleDatasets(context.Background(), rule))
}

func TestGetKlineResampleBackfillKeepsMixedActiveRequestCancelable(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	start := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	makeInstance := func(taskID string, state domain.ResampleBackfillState) domain.TaskInstance {
		result := domain.NewResampleTaskResult(start)
		result.LastError = "source retention expired"
		result.Backfill = &domain.ResampleBackfill{RequestID: "request-1", Start: start, End: start.Add(time.Hour), NextBucket: start, State: state}
		encoded, marshalErr := result.Marshal()
		require.NoError(t, marshalErr)
		return domain.TaskInstance{SpaceID: "crypto", TaskID: taskID, RuleID: "rule-5m", DataType: "kline_resample", Result: encoded}
	}
	instances := []domain.TaskInstance{makeInstance("failed", domain.ResampleBackfillFailed), makeInstance("syncing", domain.ResampleBackfillSyncing)}
	require.NoError(t, db.TaskInstances().UpsertMany(context.Background(), instances))
	service := &Service{instanceRepo: db.TaskInstances()}
	rsp, err := service.GetKlineResampleBackfill(context.Background(), &pb.GetKlineResampleBackfillReq{SpaceId: "crypto", RuleId: "rule-5m", RequestId: "request-1"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "syncing", rsp.GetState())
	require.EqualValues(t, 1, rsp.GetFailed())
	require.EqualValues(t, 1, rsp.GetSyncing())
}
