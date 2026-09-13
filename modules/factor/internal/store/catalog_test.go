package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func catalogStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(&Options{Path: filepath.Join(t.TempDir(), "catalog.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.ApplySchema(factorschema.AllSQL()))
	return s
}

func TestCatalogSnapshotDoesNotMixConcurrentCommit(t *testing.T) {
	s := catalogStore(t)
	ctx := context.Background()
	sqlDB, err := s.db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(2)
	require.NoError(t, s.db.Exec(`INSERT INTO t_factor_defs(c_factor_id,c_name,c_factor_type,c_source_code,c_source_hash,c_input_columns_json,c_outputs_json,c_lookback_periods) VALUES ('f','old','timeseries','old','hash','[]','[]',1);
 INSERT INTO t_factor_bindings(c_binding_id,c_factor_id,c_space_id,c_source_view_id,c_freq,c_result_dataset_id,c_result_view_id) VALUES ('b','f','old','v','1m','r','rv')`).Error)
	before, err := s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	var once sync.Once
	var writerErr error
	require.NoError(t, s.db.Callback().Query().After("gorm:query").Register("test:concurrent_catalog_commit", func(tx *gorm.DB) {
		if tx.Statement.Table != "t_factor_defs" {
			return
		}
		once.Do(func() {
			writerErr = s.db.Transaction(func(writer *gorm.DB) error {
				if err := writer.Exec("UPDATE t_factor_defs SET c_name='new', c_source_code='new'").Error; err != nil {
					return err
				}
				return writer.Exec("UPDATE t_factor_bindings SET c_space_id='new'").Error
			})
		})
	}))
	snapshot, err := s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.NoError(t, writerErr)
	require.Equal(t, before, snapshot)
	require.NoError(t, s.db.Callback().Query().Remove("test:concurrent_catalog_commit"))
	after, err := s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.Greater(t, after.Revision, before.Revision)
	require.Equal(t, "new", after.Factors[0].SourceCode)
	require.Equal(t, "new", after.Bindings[0].SpaceID)
}

func TestCatalogRevisionAdvancesAfterEmptyBootstrap(t *testing.T) {
	s := catalogStore(t)
	ctx := context.Background()
	changed, err := s.ReplaceCatalogSnapshot(ctx, domain.CatalogSnapshot{Revision: 1})
	require.NoError(t, err)
	require.True(t, changed)
	before, err := s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, before.Revision)
	require.Empty(t, before.Factors)
	require.NoError(t, s.Factors().Create(ctx, domain.FactorDef{
		FactorID: "bias", Name: "Bias", FactorType: domain.FactorTypeTimeSeries,
		SourceCode: "def compute(df, params, context):\n    return df\n", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias_20"}, ParamsJSON: "{}",
		LookbackPeriods: 20, Status: domain.FactorStatusDisabled,
	}))
	after, err := s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, after.Revision)
	require.Len(t, after.Factors, 1)
	require.Equal(t, "bias", after.Factors[0].FactorID)
}

func TestCatalogSnapshotRevisionAndRollback(t *testing.T) {
	s := catalogStore(t)
	ctx := context.Background()
	initial, err := s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 0, initial.Revision)
	f := domain.FactorDef{FactorID: "f", Name: "factor", FactorType: domain.FactorTypeCrossSection, SourceCode: "def compute(): pass", SourceHash: "hash", ParamsJSON: "{}", LookbackPeriods: 1, Status: domain.FactorStatusDisabled, InputColumns: []string{"close"}, Outputs: []string{"value"}}
	require.NoError(t, s.Factors().Create(ctx, f))
	before, err := s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.Positive(t, before.Revision)
	require.Equal(t, f.SourceCode, before.Factors[0].SourceCode)
	require.Equal(t, f.FactorType, before.Factors[0].FactorType)
	require.Error(t, s.db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec("UPDATE t_factor_defs SET c_source_code = 'changed'").Error)
		return tx.Exec("INSERT INTO nonexistent VALUES (1)").Error
	}))
	after, err := s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestSnapshotHashIgnoresLocalArtifactMetadata(t *testing.T) {
	source := catalogStore(t)
	ctx := context.Background()
	require.NoError(t, source.Factors().Create(ctx, domain.FactorDef{
		FactorID: "f", Name: "factor", FactorType: domain.FactorTypeTimeSeries,
		SourceCode: "source", SourceHash: "hash", InputColumns: []string{}, Outputs: []string{},
		ParamsJSON: "{}", LookbackPeriods: 1, Status: domain.FactorStatusEnabled,
	}))
	snapshot, err := source.CatalogSnapshot(ctx)
	require.NoError(t, err)
	replica := catalogStore(t)
	changed, err := replica.ReplaceCatalogSnapshot(ctx, *snapshot)
	require.NoError(t, err)
	require.True(t, changed)
	snapshot.Factors[0].SourcePath = "/another-host/factors/f.py"
	snapshot.Factors[0].ModifyTime = time.Now().Add(time.Hour)
	changed, err = replica.ReplaceCatalogSnapshot(ctx, *snapshot)
	require.NoError(t, err)
	require.False(t, changed)
	snapshot.Factors[0].SourceCode = "different algorithm"
	_, err = replica.ReplaceCatalogSnapshot(ctx, *snapshot)
	require.ErrorIs(t, err, ErrCatalogRevisionConflict)
}

func TestCatalogRevisionIgnoresUnchangedValues(t *testing.T) {
	s := catalogStore(t)
	ctx := context.Background()
	require.NoError(t, s.db.Exec(`INSERT INTO t_factor_defs
		(c_factor_id,c_name,c_factor_type,c_source_code,c_source_hash,c_input_columns_json,c_outputs_json,c_lookback_periods)
		VALUES ('f','factor','timeseries','source','hash','[]','[]',1)`).Error)
	require.NoError(t, s.db.Exec(`INSERT INTO t_factor_bindings
		(c_binding_id,c_factor_id,c_space_id,c_source_view_id,c_freq,c_result_dataset_id,c_result_view_id)
		VALUES ('b','f','s','v','1m','r','rv')`).Error)
	before, err := s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.NoError(t, s.db.Exec("UPDATE t_factor_defs SET c_status=c_status, c_name=c_name").Error)
	require.NoError(t, s.db.Exec("UPDATE t_factor_bindings SET c_status=c_status, c_freq=c_freq").Error)
	after, err := s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, before.Revision, after.Revision)
	require.NoError(t, s.db.Exec("UPDATE t_factor_bindings SET c_freq='5m'").Error)
	after, err = s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, before.Revision+1, after.Revision)
}

func TestReplaceCatalogSnapshotAtomicAndPreservesManifests(t *testing.T) {
	s := catalogStore(t)
	ctx := context.Background()
	snapshot := domain.CatalogSnapshot{Revision: 10, Factors: []domain.FactorDef{{FactorID: "f", Name: "factor", FactorType: domain.FactorTypeTimeSeries, SourceCode: "source", SourceHash: "hash", InputColumns: []string{}, Outputs: []string{}, ParamsJSON: "{}", LookbackPeriods: 1, Status: domain.FactorStatusEnabled}}, Bindings: []domain.FactorBinding{{BindingID: "b", FactorID: "f", SpaceID: "s", SourceViewID: "v", Freq: "1m", SubjectMode: "all", SubjectsJSON: "[]", ResultDatasetID: "r", ResultViewID: "rv", Status: domain.BindingStatusEnabled}}}
	changed, err := s.ReplaceCatalogSnapshot(ctx, snapshot)
	require.NoError(t, err)
	require.True(t, changed)
	current, err := s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 10, current.Revision)
	key := OutputManifestKey{BindingID: "b", SubjectID: "subject", Frequency: "1m", PeriodTime: time.Now().UTC()}
	require.NoError(t, s.OutputManifests().Replace(ctx, key, []string{"row"}))
	changed, err = s.ReplaceCatalogSnapshot(ctx, snapshot)
	require.NoError(t, err)
	require.False(t, changed)
	snapshot.Factors[0].SourceCode = "conflicting duplicate"
	_, err = s.ReplaceCatalogSnapshot(ctx, snapshot)
	require.ErrorIs(t, err, ErrCatalogRevisionConflict)
	_, err = s.ReplaceCatalogSnapshot(ctx, domain.CatalogSnapshot{Revision: 0})
	require.Error(t, err)
	snapshot.Revision = 9
	_, err = s.ReplaceCatalogSnapshot(ctx, snapshot)
	require.ErrorIs(t, err, ErrCatalogRevisionRegression)
	snapshot.Revision = 11
	snapshot.Bindings[0].FactorID = "missing"
	_, err = s.ReplaceCatalogSnapshot(ctx, snapshot)
	require.Error(t, err)
	after, err := s.CatalogSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, current, after)
	changed, err = s.ReplaceCatalogSnapshot(ctx, domain.CatalogSnapshot{Revision: 12})
	require.NoError(t, err)
	require.True(t, changed)
	keys, err := s.OutputManifests().ListByBinding(ctx, "b")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	deleted, err := s.OutputManifests().DeleteBefore(ctx, time.Now().UTC().Add(time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
}
