package store

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestManifestGenerationRetainsRemovedOwnership(t *testing.T) {
	s := catalogStore(t)
	ctx := context.Background()
	old := OutputManifestKey{BindingID: "reused", BindingGeneration: "old", CleanupTaskJSON: `{"ResultDatasetID":"old-result","Factor":{"Outputs":["old-value"]}}`, SubjectID: "BTC", Frequency: "1m", PeriodTime: time.Now().UTC()}
	next := old
	next.BindingGeneration = "new"
	next.CleanupTaskJSON = `{"ResultDatasetID":"new-result","Factor":{"Outputs":["new-value"]}}`
	require.NoError(t, s.OutputManifests().Replace(ctx, old, []string{"old-row"}))
	require.NoError(t, s.OutputManifests().Replace(ctx, next, []string{"new-row"}))
	_, err := s.ReplaceCatalogSnapshot(ctx, domain.CatalogSnapshot{Revision: 100})
	require.NoError(t, err)
	owned, err := s.OutputManifests().ListOwned(ctx)
	require.NoError(t, err)
	require.Len(t, owned, 2)
	require.Contains(t, owned, old)
	require.Contains(t, owned, next)
	require.NoError(t, s.OutputManifests().Replace(ctx, old, nil))
	keys, err := s.OutputManifests().Get(ctx, next)
	require.NoError(t, err)
	require.Equal(t, []string{"new-row"}, keys)
}

func TestBindingGenerationChangesOnlyWithOwnership(t *testing.T) {
	s := catalogStore(t)
	ctx := context.Background()
	require.NoError(t, s.db.Exec(`INSERT INTO t_factor_defs(c_factor_id,c_name,c_factor_type,c_source_code,c_source_hash,c_input_columns_json,c_outputs_json,c_lookback_periods) VALUES ('f','factor','timeseries','source','hash','[]','[]',1)`).Error)
	b := domain.FactorBinding{BindingID: "b", FactorID: "f", SpaceID: "s", SourceViewID: "v", Freq: "1m", ResultDatasetID: "old", ResultViewID: "rv"}
	require.NoError(t, s.Bindings().Upsert(ctx, b))
	bindings, err := s.Bindings().ListByFactor(ctx, "f")
	require.NoError(t, err)
	generation := bindings[0].BindingGeneration
	require.NotEmpty(t, generation)
	require.NoError(t, s.Bindings().Upsert(ctx, b))
	bindings, err = s.Bindings().ListByFactor(ctx, "f")
	require.NoError(t, err)
	require.Equal(t, generation, bindings[0].BindingGeneration)
	b.Status = domain.BindingStatusDisabled
	require.NoError(t, s.Bindings().Upsert(ctx, b))
	bindings, err = s.Bindings().ListByFactor(ctx, "f")
	require.NoError(t, err)
	require.Equal(t, generation, bindings[0].BindingGeneration)
	b.ResultDatasetID = "new"
	require.NoError(t, s.Bindings().Upsert(ctx, b))
	bindings, err = s.Bindings().ListByFactor(ctx, "f")
	require.NoError(t, err)
	require.NotEqual(t, generation, bindings[0].BindingGeneration)
	beforeDelete := bindings[0].BindingGeneration
	replica := catalogStore(t)
	snapshot, err := s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	_, err = replica.ReplaceCatalogSnapshot(ctx, *snapshot)
	require.NoError(t, err)
	require.NoError(t, s.Bindings().Delete(ctx, b.BindingID))
	snapshot, err = s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	_, err = replica.ReplaceCatalogSnapshot(ctx, *snapshot)
	require.NoError(t, err)
	// Even supplying the old incarnation cannot resurrect a deleted owner.
	b.BindingGeneration = beforeDelete
	require.NoError(t, s.Bindings().Upsert(ctx, b))
	snapshot, err = s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	_, err = replica.ReplaceCatalogSnapshot(ctx, *snapshot)
	require.NoError(t, err)
	bindings, err = replica.Bindings().ListByFactor(ctx, "f")
	require.NoError(t, err)
	require.NotEqual(t, beforeDelete, bindings[0].BindingGeneration)
}
