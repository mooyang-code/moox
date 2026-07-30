package rpc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestFactorConvertRoundTrip(t *testing.T) {
	def := domain.FactorDef{
		FactorID: "f1", Name: "Bias", SourceCode: "x=1", SourceHash: "h",
		InputColumns: []string{"close", "funding_rate"}, Outputs: []string{"bias_20", "bias_96"},
		ParamsJSON: `{"windows":[20,96]}`, LookbackPeriods: 200, Status: domain.FactorStatusEnabled,
	}
	got := factorFromPB(factorToPB(def))
	require.Equal(t, def.FactorID, got.FactorID)
	require.Equal(t, def.InputColumns, got.InputColumns)
	require.Equal(t, def.Outputs, got.Outputs)
	require.Equal(t, def.ParamsJSON, got.ParamsJSON)
	require.Equal(t, def.LookbackPeriods, got.LookbackPeriods)
}
