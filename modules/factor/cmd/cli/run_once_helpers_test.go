package main

import (
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMustParseParams_AndFilterFactors(t *testing.T) {
	assert.Equal(t, []int{1, 2, 3}, mustParseParams(`[1,2,3]`))
	assert.Nil(t, mustParseParams(`not-json`))

	factors := []domain.FactorDef{{FactorID: "a"}, {FactorID: "b"}, {FactorID: "c"}}
	got := filterFactors(factors, []string{"b", "c"})
	require.Len(t, got, 2)
	assert.Equal(t, "b", got[0].FactorID)
	assert.Equal(t, factors, filterFactors(factors, nil))
}

func TestInputColumns_IncludesExtras(t *testing.T) {
	cols := inputColumns([]engine.FactorSpec{{ExtraColumns: []string{"vwap", "close"}}})
	assert.Contains(t, cols, "close")
	assert.Contains(t, cols, "vwap")
}

func TestBuildTask_UsesLookbackAndParams(t *testing.T) {
	cfg := cliConfig{SpaceID: "s", DatasetID: "d", SubjectID: "BTC", Freq: "1m", FactorsDir: t.TempDir()}
	task := buildTask(cfg, []domain.FactorDef{{
		FactorID: "f1", Name: "demo", ParamsJSON: `[10]`, LookbackBars: 20, WritebackBars: 1,
	}})
	require.NotNil(t, task)
	assert.Equal(t, 20, task.LookbackBars)
	require.Len(t, task.Factors, 1)
	assert.Equal(t, []int{10}, task.Factors[0].Params)
}
