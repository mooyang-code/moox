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
	assert.Equal(t, "collect.kline", def.JobType)
	assert.Equal(t, "kline", def.DataType)
	assert.Equal(t, "K线", def.TypeName)
	require.Len(t, def.Fields, 3)
	assert.Equal(t, "intervals", def.Fields[2].FieldKey)
}

func TestBuildTaskSpecs_MultipleIntervals_ShouldExpandSubjects(t *testing.T) {
	params := &domain.CollectParams{}
	params.Normalize("binance", "kline")
	params.Collector.Intervals = []string{"1m", "5m"}
	params.Target.DatasetID = "ds-1"
	subjects := []domain.DatasetSubject{
		{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT"},
		{SubjectID: "ETH-USDT", ExternalSymbol: "ETHUSDT"},
	}

	specs := BuildTaskSpecs(params, subjects)
	require.Len(t, specs, 4)
	assert.Equal(t, "BTCUSDT", specs[0].Symbol)
	assert.Equal(t, "1m", specs[0].Interval)
}

func TestBuildTaskSpecs_EmptyInterval_ShouldDefaultToOneMinute(t *testing.T) {
	params := &domain.CollectParams{}
	params.Normalize("binance", "kline")
	params.Target.DatasetID = "ds-1"
	subjects := []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT"}}

	specs := BuildTaskSpecs(params, subjects)
	require.Len(t, specs, 1)
	assert.Equal(t, "1m", specs[0].Interval)
}

func TestNewJobDefinition_Planner_InvalidSourceKind_ShouldReturnError(t *testing.T) {
	def := NewJobDefinition()
	params := &domain.CollectParams{}
	params.Normalize("binance", "kline")
	params.Source.Kind = "none"

	_, err := def.Planner(context.Background(), &domain.TaskRule{}, params, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dataset_subjects")
}
