package rpc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestFactorConvertRoundTrip(t *testing.T) {
	def := domain.FactorDef{
		FactorID: "f1", Name: "Bias", SourceCode: "x=1", SourceHash: "h",
		Periods: []int{20, 96}, LookbackBars: 200,
		Depends: []string{"funding_rate"}, Status: domain.FactorStatusEnabled,
	}
	got := factorFromPB(factorToPB(def))
	require.Equal(t, def.FactorID, got.FactorID)
	require.Equal(t, def.Periods, got.Periods)
	require.Equal(t, def.Depends, got.Depends)
}
