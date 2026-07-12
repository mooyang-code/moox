package rpc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/symbol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobTypesFromInstances_DedupesAndDerivesJobType(t *testing.T) {
	got := jobTypesFromInstances([]domain.TaskInstance{
		{TaskParams: `{"job_type":"collect.symbol"}`},
		{TaskParams: `{"job_type":"collect.symbol"}`},
		{TaskParams: `{"data_type":"kline"}`},
	})
	assert.Equal(t, []string{"collect.symbol", "collect.kline"}, got)
	assert.Empty(t, jobTypesFromInstances([]domain.TaskInstance{{TaskParams: `{"data_type":""}`}}))
}

func TestDataTypeConfigFromDefinition(t *testing.T) {
	def := symbol.Definition(jobs.JobTypeCollectSymbol)
	cfg := dataTypeConfigFromDefinition(def)
	assert.Equal(t, "symbol", cfg.GetDataType())
	assert.Equal(t, "标的", cfg.GetTypeName())
	fields := dataTypeFieldsFromDefinition(def)
	require.NotEmpty(t, fields)
	assert.NotEmpty(t, fields[0].GetFieldName())
}

func TestStructFromAnyAndValueFromAny(t *testing.T) {
	st := structFromAny(map[string]any{"k": "v"})
	assert.Equal(t, "v", st.GetFields()["k"].GetStringValue())
	assert.Empty(t, structFromAny(make(chan int)).GetFields())

	val := valueFromAny("hello")
	assert.Equal(t, "hello", val.GetStringValue())
	assert.Equal(t, "", valueFromAny(make(chan int)).GetStringValue())
}

func TestJobsListDefinitionsNotEmpty(t *testing.T) {
	assert.NotEmpty(t, jobs.ListDefinitions())
}
