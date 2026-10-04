package compiler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type factorSetCatalogFake struct {
	sets        []FactorSetDescriptor
	factors     map[string][]FactorDescriptor
	setCalls    int
	factorCalls []string
}

func (f *factorSetCatalogFake) ListFactorSets(context.Context) ([]FactorSetDescriptor, error) {
	f.setCalls++
	return f.sets, nil
}

func (f *factorSetCatalogFake) ListFactors(_ context.Context, set FactorSetDescriptor) ([]FactorDescriptor, error) {
	f.factorCalls = append(f.factorCalls, set.SetID)
	return f.factors[set.SetID], nil
}

type factorSetStorageFake struct{}

func (factorSetStorageFake) GetView(_ context.Context, id string) (ViewDescriptor, error) {
	return ViewDescriptor{ID: id, DatasetID: "factor-result", Status: "enabled", Frequency: "1m"}, nil
}

func (factorSetStorageFake) ListViewColumns(context.Context, string) ([]ViewColumn, error) {
	return []ViewColumn{{Name: "factor-result.momentum", Attributes: map[string]string{
		"origin_factor_id": "momentum", "factor_output": "score",
	}}}, nil
}

func TestVerifyDependenciesUsesFactorSets(t *testing.T) {
	baseFactors := map[string][]FactorDescriptor{
		"set-result": {{FactorID: "momentum", SetID: "set-result", Outputs: []string{"score"}, Status: "enabled", ResultDatasetID: "factor-result"}},
		"set-other":  {{FactorID: "momentum", SetID: "set-other", Outputs: []string{"score"}, Status: "enabled", ResultDatasetID: "another-result"}},
	}
	compiled := CompiledStrategy{
		Factors: []CompiledFactor{{FactorID: "momentum", SetID: "set-result", ResultDatasetID: "factor-result", ResultViewID: "result-view", Frequency: "1m", Output: "score", ColumnName: "factor-result.momentum"}},
	}
	tests := []struct {
		name           string
		sets           []FactorSetDescriptor
		factors        map[string][]FactorDescriptor
		wantErr        string
		wantFactorList bool
	}{
		{
			name:           "enabled factor in the result view factor set",
			sets:           []FactorSetDescriptor{{SetID: "set-result", ResultDatasetID: "factor-result", Status: "enabled"}, {SetID: "set-other", ResultDatasetID: "another-result", Status: "enabled"}},
			factors:        baseFactors,
			wantFactorList: true,
		},
		{
			name:    "factor set must be enabled",
			sets:    []FactorSetDescriptor{{SetID: "set-result", ResultDatasetID: "factor-result", Status: "disabled"}},
			factors: baseFactors,
			wantErr: "factor set",
		},
		{
			name:           "factor must belong to the result dataset set",
			sets:           []FactorSetDescriptor{{SetID: "set-result", ResultDatasetID: "factor-result", Status: "enabled"}},
			factors:        map[string][]FactorDescriptor{"set-result": {{FactorID: "momentum", SetID: "set-result", Outputs: []string{"score"}, Status: "enabled", ResultDatasetID: "another-result"}}},
			wantErr:        "factor",
			wantFactorList: true,
		},
		{
			name:           "declared factor must exist in the result dataset set",
			sets:           []FactorSetDescriptor{{SetID: "set-result", ResultDatasetID: "factor-result", Status: "enabled"}},
			factors:        map[string][]FactorDescriptor{"set-result": {}},
			wantErr:        "factor",
			wantFactorList: true,
		},
		{
			name:           "factor must be enabled",
			sets:           []FactorSetDescriptor{{SetID: "set-result", ResultDatasetID: "factor-result", Status: "enabled"}},
			factors:        map[string][]FactorDescriptor{"set-result": {{FactorID: "momentum", SetID: "set-result", Outputs: []string{"score"}, Status: "disabled", ResultDatasetID: "factor-result"}}},
			wantErr:        "factor",
			wantFactorList: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := &factorSetCatalogFake{sets: tt.sets, factors: tt.factors}
			err := (Compiler{Factors: catalog, Storage: factorSetStorageFake{}}).VerifyDependencies(context.Background(), compiled)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
			require.Equal(t, 1, catalog.setCalls)
			if tt.wantFactorList {
				require.Contains(t, catalog.factorCalls, "set-result")
			}
		})
	}
}
