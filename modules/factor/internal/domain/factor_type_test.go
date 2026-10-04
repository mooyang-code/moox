package domain

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestFactorTypeRequired(t *testing.T) {
	factor := FactorDef{FactorID: "F", Name: "F", SourceCode: "x", InputColumns: []string{"close"}, Outputs: []string{"value"}, LookbackPeriods: 1}
	_, err := NormalizeFactorDefinition(factor)
	require.ErrorContains(t, err, "factor_type")
}

func TestFactorTypes(t *testing.T) {
	for _, value := range []string{"timeseries", "cross_section"} {
		require.NoError(t, ValidateFactorType(value))
	}
	for _, value := range []string{"", "unknown", " timeseries "} {
		require.ErrorContains(t, ValidateFactorType(value), "factor_type")
	}
}
