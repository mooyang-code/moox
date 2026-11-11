package kline

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewJobDefinition_ShouldExposeKlineMetadata(t *testing.T) {
	def := NewJobDefinition()
	assert.Equal(t, "kline", def.DataType)
	assert.Equal(t, "K线", def.TypeName)
	require.Len(t, def.Fields, 2)
	assert.Equal(t, "intervals", def.Fields[1].FieldKey)
}

func TestBuildTaskSpecs_MultipleIntervals_ShouldExpandSubjects(t *testing.T) {
	params := &domain.CollectParams{}
	params.Normalize("binance", "spot", "kline")
	params.Collector.Intervals = []string{"1m", "5m"}
	params.Target.DatasetID = "ds-1"
	subjects := []domain.DatasetSubject{
		{SubjectID: "BTC-USDT"},
		{SubjectID: "ETH-USDT"},
	}

	specs := BuildTaskSpecs(params, subjects)
	require.Len(t, specs, 4)
	assert.Equal(t, "BTC-USDT", specs[0].SubjectID)
	assert.Equal(t, "1m", specs[0].Params["interval"])
}

func TestBuildTaskSpecs_EmptyInterval_ShouldDefaultToOneMinute(t *testing.T) {
	params := &domain.CollectParams{}
	params.Normalize("binance", "spot", "kline")
	params.Target.DatasetID = "ds-1"
	subjects := []domain.DatasetSubject{{SubjectID: "BTC-USDT"}}

	specs := BuildTaskSpecs(params, subjects)
	require.Len(t, specs, 1)
	assert.Equal(t, "1m", specs[0].Params["interval"])
}

func TestNewJobDefinition_Planner_InvalidSourceKind_ShouldReturnError(t *testing.T) {
	def := NewJobDefinition()
	params := &domain.CollectParams{}
	params.Normalize("binance", "spot", "kline")
	params.Source.Kind = "none"

	_, err := def.Planner(context.Background(), &domain.CollectionTask{}, params, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dataset")
}

// A 股（标签的 sync.source 是 eastmoney、市场 equity）支持 1m、5m、1d；其他周期建任务时被拒。
func TestNewJobDefinition_ShouldSupportStockDaily(t *testing.T) {
	def := NewJobDefinition()
	for frequency, want := range map[string]bool{"1m": true, "5m": true, "1d": true, "15m": false, "1w": false} {
		params, err := domain.ParseCollectParams(`{"target_dataset_id":"bars","frequency":"`+frequency+`"}`, "eastmoney", "equity", "kline")
		require.NoError(t, err, frequency)
		assert.Equal(t, want, def.Matches(params), frequency)
	}
}
