package catalogsync

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func TestSynchronizerReconcilesAndRequiresFencedCommit(t *testing.T) {
	ctx := context.Background()
	replica, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "replica.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, replica.Close()) })
	require.NoError(t, replica.ApplySchema(factorschema.AllSQL()))
	source := "def compute(df, params, context):\n    return df\n"
	next := &domain.CatalogSnapshot{Revision: 10, Factors: []domain.FactorDef{{
		FactorID: "f", Name: "Factor", FactorType: "timeseries", SourceCode: source,
		SourceHash: fmt.Sprintf("%x", sha256.Sum256([]byte(source))), LookbackPeriods: 1,
		InputColumns: []string{}, Outputs: []string{}, ParamsJSON: "{}", Status: "disabled",
	}}}
	activationCount := 0
	syncer, err := NewSynchronizer(t.TempDir(), replica, func(context.Context) (*domain.CatalogSnapshot, error) { return next, nil },
		func(_ context.Context, previous, prepared domain.CatalogSnapshot, commit func() error) error {
			activationCount++
			require.Less(t, previous.Revision, prepared.Revision)
			require.NotEmpty(t, prepared.Factors[0].SourcePath)
			return commit()
		})
	require.NoError(t, err)
	result, err := syncer.Sync(ctx)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.EqualValues(t, 10, result.Revision)
	result, err = syncer.Sync(ctx)
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.Equal(t, 1, activationCount)
	next.Revision = 9
	_, err = syncer.Sync(ctx)
	require.ErrorContains(t, err, "regressed")
	next.Revision = 11
	syncer.activate = func(context.Context, domain.CatalogSnapshot, domain.CatalogSnapshot, func() error) error { return nil }
	result, err = syncer.Sync(ctx)
	require.ErrorContains(t, err, "did not commit")
	require.EqualValues(t, 10, result.Revision)
	actual, err := replica.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 10, actual.Revision)
}
