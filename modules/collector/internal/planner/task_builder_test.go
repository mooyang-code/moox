package planner

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskBuilder_NewTaskBuilder_ShouldReturnBuilder(t *testing.T) {
	b := NewTaskBuilder()
	require.NotNil(t, b)
}

func TestTaskBuilder_BuildInstances_NilRule_ShouldReturnError(t *testing.T) {
	b := NewTaskBuilder()
	_, err := b.BuildInstances(context.Background(), nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rule is required")
}

func TestTaskBuilder_BuildDatasetDrivenInstances_EmptySubjects_ShouldReturnEmpty(t *testing.T) {
	b := NewTaskBuilder()
	rule := &domain.TaskRule{
		RuleID:        "rule-1",
		SpaceID:       "space-1",
		Exchange:      "binance",
		DataType:      "kline",
		CollectParams: `{"source":{"kind":"dataset_subjects","dataset_id":"ds-1"},"collector":{"exchange":"binance","market":"spot","data_type":"kline","intervals":["1m"]},"target":{"dataset_id":"ds-1"},"schedule":{"interval":"1m"}}`,
	}
	instances, err := b.BuildDatasetDrivenInstances(context.Background(), rule, nil)
	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestTaskBuilder_BuildInstancesWithParams_KlineSubjects_ShouldCreateInstances(t *testing.T) {
	b := NewTaskBuilder()
	params := &domain.CollectParams{}
	params.Normalize("binance", "kline")
	params.Source.Kind = "dataset_subjects"
	params.Collector.Market = "spot"
	params.Target.DatasetID = "ds-1"
	params.Collector.Intervals = []string{"1m", "5m"}

	rule := &domain.TaskRule{RuleID: "rule-1", SpaceID: "space-1"}
	subjects := []domain.DatasetSubject{
		{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT"},
	}
	instances, err := b.BuildInstancesWithParams(context.Background(), rule, params, subjects)
	require.NoError(t, err)
	require.Len(t, instances, 2)
	assert.Equal(t, "BTCUSDT", instances[0].Symbol)
	assert.NotEmpty(t, instances[0].TaskID)
}

func TestTaskBuilder_BuildInstancesWithParams_SymbolJob_ShouldCreateSingleInstance(t *testing.T) {
	b := NewTaskBuilder()
	params := &domain.CollectParams{}
	params.Normalize("binance", "symbol")
	params.Source.Kind = "none"
	params.Collector.Market = "spot"
	params.Target.DatasetID = "ds-symbol"

	rule := &domain.TaskRule{RuleID: "rule-2", SpaceID: "space-1"}
	instances, err := b.BuildInstancesWithParams(context.Background(), rule, params, nil)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "symbol", instances[0].DataType)
}
