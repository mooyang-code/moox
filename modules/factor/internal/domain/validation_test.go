package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeFactorDefinitionSortsAndValidatesPeriods(t *testing.T) {
	got, err := NormalizeFactorDefinition(FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "def signal(): pass",
		Periods: []int{20, 5, 20}, LookbackBars: 30,
		Status: FactorStatusEnabled,
	})
	require.NoError(t, err)
	require.Equal(t, []int{5, 20}, got.Periods)
}

func TestNormalizeFactorDefinitionRejectsInvalidWindow(t *testing.T) {
	_, err := NormalizeFactorDefinition(FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "x",
		Periods: []int{20}, LookbackBars: 10,
	})
	require.Error(t, err)
}

func TestNormalizeFactorDefinitionRejectsInvalidValues(t *testing.T) {
	tests := []FactorDef{
		{FactorID: "f", Name: "F", SourceCode: "x", LookbackBars: 20},
		{FactorID: "f", Name: "F", SourceCode: "x", Periods: []int{0}, LookbackBars: 20},
		{FactorID: "f", Name: "F", SourceCode: "x", Periods: []int{20}, LookbackBars: 20, Status: "bad"},
	}
	for _, test := range tests {
		_, err := NormalizeFactorDefinition(test)
		require.Error(t, err)
	}
}
