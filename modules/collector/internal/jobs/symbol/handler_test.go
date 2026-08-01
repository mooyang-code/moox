package symbol

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewJobDefinition_ShouldExposeSymbolMetadata(t *testing.T) {
	def := NewJobDefinition()
	assert.Equal(t, "symbol", def.DataType)
	assert.Equal(t, "标的", def.TypeName)
	require.Len(t, def.Fields, 1)
	assert.Equal(t, "inst_type", def.Fields[0].FieldKey)
}

func TestBuildTaskSpecs_ValidParams_ShouldReturnSingleTask(t *testing.T) {
	params := &domain.CollectParams{}
	params.Normalize("binance", "spot", "symbol")
	params.Source.Kind = "none"
	params.Collector.Market = "spot"
	params.Target.DatasetID = "ds-symbol"

	specs := BuildTaskSpecs(params)
	require.Len(t, specs, 1)
	assert.Equal(t, "binance", specs[0].Exchange)
	assert.Equal(t, "symbol", specs[0].DataType)
	assert.NotContains(t, specs[0].Params, "job_type")
}

func TestNewJobDefinition_Planner_InvalidSourceKind_ShouldReturnError(t *testing.T) {
	def := NewJobDefinition()
	params := &domain.CollectParams{}
	params.Normalize("binance", "spot", "symbol")
	params.Source.Kind = "dataset_subjects"

	_, err := def.Planner(context.Background(), &domain.TaskRule{}, params, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "none source")
}
