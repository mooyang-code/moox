package catalogsync

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestPrepareArtifactsUsesVerifiedLocalImmutablePaths(t *testing.T) {
	code := "def compute(df, params, context):\n    return df\n"
	snapshot := domain.CatalogSnapshot{Revision: 1, Factors: []domain.FactorDef{{
		FactorID: "f", Name: "Bias", FactorType: domain.FactorTypeTimeSeries,
		SourceCode: code, SourceHash: fmt.Sprintf("%x", sha256.Sum256([]byte(code))), SourcePath: "/control-host/Bias.py",
	}}}
	root := t.TempDir()
	local, err := PrepareArtifacts(context.Background(), root, snapshot)
	require.NoError(t, err)
	require.Equal(t, "/control-host/Bias.py", snapshot.Factors[0].SourcePath)
	require.Equal(t, filepath.Join(root, "factor", "Bias", snapshot.Factors[0].SourceHash, "module.py"), local.Factors[0].SourcePath)
	raw, err := os.ReadFile(local.Factors[0].SourcePath)
	require.NoError(t, err)
	require.Equal(t, code, string(raw))
	localAgain, err := PrepareArtifacts(context.Background(), root, snapshot)
	require.NoError(t, err)
	require.Equal(t, local, localAgain)
	snapshot.Factors[0].LookbackPeriods = domain.MaxTimeSeriesLookback + 1
	_, err = PrepareArtifacts(context.Background(), root, snapshot)
	require.ErrorContains(t, err, "lookback_periods must not exceed")
	snapshot.Factors[0].LookbackPeriods = domain.MaxTimeSeriesLookback
	_, err = PrepareArtifacts(context.Background(), root, snapshot)
	require.NoError(t, err)
	missingGeneration := snapshot
	missingGeneration.Bindings = []domain.FactorBinding{{BindingID: "b", FactorID: "f"}}
	_, err = PrepareArtifacts(context.Background(), root, missingGeneration)
	require.Error(t, err)
	snapshot.Factors = append(snapshot.Factors, domain.FactorDef{FactorID: "bad", Name: "Broken", FactorType: domain.FactorTypeTimeSeries, SourceCode: "changed", SourceHash: "bad"})
	invalidRoot := filepath.Join(t.TempDir(), "not-created")
	_, err = PrepareArtifacts(context.Background(), invalidRoot, snapshot)
	require.ErrorContains(t, err, "hash mismatch")
	_, err = os.Stat(invalidRoot)
	require.True(t, os.IsNotExist(err))
}
