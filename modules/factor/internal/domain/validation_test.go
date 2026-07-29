package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeFactorDefinitionCanonicalizesGenericContract(t *testing.T) {
	got, err := NormalizeFactorDefinition(FactorDef{
		FactorID: " excess-return ", Name: " ExcessReturn ",
		SourceCode:      " def compute(df, params): return {} ",
		InputColumns:    []string{" benchmark_return ", "nav", "nav"},
		Outputs:         []string{"rolling_rank", "excess_return"},
		ParamsJSON:      ` { "window": 20 } `,
		LookbackPeriods: 20,
	})
	require.NoError(t, err)
	require.Equal(t, "excess-return", got.FactorID)
	require.Equal(t, "ExcessReturn", got.Name)
	require.Equal(t, []string{"benchmark_return", "nav"}, got.InputColumns)
	require.Equal(t, []string{"excess_return", "rolling_rank"}, got.Outputs)
	require.Equal(t, `{"window":20}`, got.ParamsJSON)
	require.Equal(t, FactorStatusDisabled, got.Status)
}

func TestNormalizeFactorDefinitionRejectsInvalidValues(t *testing.T) {
	valid := FactorDef{
		FactorID: "f", Name: "F", SourceCode: "x",
		InputColumns: []string{"close"}, Outputs: []string{"value"},
		ParamsJSON: `{}`, LookbackPeriods: 1,
	}
	tests := map[string]func(*FactorDef){
		"empty inputs":      func(f *FactorDef) { f.InputColumns = nil },
		"empty outputs":     func(f *FactorDef) { f.Outputs = nil },
		"reserved input":    func(f *FactorDef) { f.InputColumns = []string{"data_time"} },
		"reserved output":   func(f *FactorDef) { f.Outputs = []string{"data_time"} },
		"tag input":         func(f *FactorDef) { f.InputColumns = []string{"series_tag"} },
		"tag output":        func(f *FactorDef) { f.Outputs = []string{"series_tag"} },
		"blank input item":  func(f *FactorDef) { f.InputColumns = []string{"close", " "} },
		"blank output item": func(f *FactorDef) { f.Outputs = []string{"value", " "} },
		"invalid lookback":  func(f *FactorDef) { f.LookbackPeriods = 0 },
		"invalid json":      func(f *FactorDef) { f.ParamsJSON = `{"window":` },
		"array params":      func(f *FactorDef) { f.ParamsJSON = `[]` },
		"null params":       func(f *FactorDef) { f.ParamsJSON = `null` },
		"string params":     func(f *FactorDef) { f.ParamsJSON = `"x"` },
		"number params":     func(f *FactorDef) { f.ParamsJSON = `1` },
		"trailing json":     func(f *FactorDef) { f.ParamsJSON = `{} {}` },
		"invalid status":    func(f *FactorDef) { f.Status = "bad" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			factor := valid
			mutate(&factor)
			_, err := NormalizeFactorDefinition(factor)
			require.Error(t, err)
		})
	}
}

func TestNormalizeFactorDefinitionDefaultsEmptyParamsObject(t *testing.T) {
	got, err := NormalizeFactorDefinition(FactorDef{
		FactorID: "f", Name: "F", SourceCode: "x",
		InputColumns: []string{"close"}, Outputs: []string{"value"},
		LookbackPeriods: 1,
	})
	require.NoError(t, err)
	require.Equal(t, `{}`, got.ParamsJSON)
}
