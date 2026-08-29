package rpc

import (
	"context"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

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
