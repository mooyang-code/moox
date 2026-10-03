package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.NoError(t, s.ApplySchema(factorschema.AllSQL()))
	return s
}

func testSet(setID, status string) domain.FactorSet {
	return domain.FactorSet{
		SetID: setID, SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m",
		SubjectMode: domain.SubjectModeAll, Subjects: []string{},
		ResultDatasetID: "dataset_factor_prices", Status: status,
	}
}

func testFactorDef(factorID, status string) domain.FactorDef {
	return domain.FactorDef{
		SetID: "set_prices", FactorID: factorID, Name: factorID,
		FactorType: domain.FactorTypeTimeSeries, SourceCode: "def compute(): return 1",
		SourceHash: "hash-" + factorID, InputColumns: []string{"close"}, Outputs: []string{factorID},
		ParamsJSON: "{}", LookbackPeriods: 1, Status: status,
	}
}

func TestCreateSetRejectsDuplicateSourceFreq(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateSet(ctx, testSet("set_prices", domain.SetStatusEnabled)))

	duplicate := testSet("set_prices_duplicate", domain.SetStatusPending)
	duplicate.ResultDatasetID = "dataset_factor_duplicate"
	err := s.CreateSet(ctx, duplicate)
	require.ErrorIs(t, err, ErrConflict)
}

func TestEnabledSetByDatasetReturnsOnlyEnabledFactors(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateSet(ctx, testSet("set_prices", domain.SetStatusEnabled)))
	for _, def := range []domain.FactorDef{
		testFactorDef("z_factor", domain.FactorStatusEnabled),
		testFactorDef("a_factor", domain.FactorStatusEnabled),
		testFactorDef("disabled_factor", domain.FactorStatusDisabled),
	} {
		require.NoError(t, s.CreateFactor(ctx, def))
	}

	set, defs, found, err := s.EnabledSetByDataset(ctx, "crypto", "dataset_prices", "1m")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "set_prices", set.SetID)
	require.Equal(t, []string{"a_factor", "z_factor"}, []string{defs[0].FactorID, defs[1].FactorID})

	set, defs, found, err = s.EnabledSetByDataset(ctx, "crypto", "dataset_prices", "5m")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, defs)
}

func TestDeleteSetFailsWhenFactorsExist(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateSet(ctx, testSet("set_prices", domain.SetStatusDisabled)))
	require.NoError(t, s.CreateFactor(ctx, testFactorDef("factor_close", domain.FactorStatusDisabled)))

	require.Error(t, s.DeleteSet(ctx, "set_prices"))
	_, err := s.GetSet(ctx, "set_prices")
	require.NoError(t, err)
}

func TestSetCRUDAndFactorCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	set := testSet("set_prices", domain.SetStatusPending)
	set.SubjectMode = domain.SubjectModeInclude
	set.Subjects = []string{"BTCUSDT", "ETHUSDT"}
	require.NoError(t, s.CreateSet(ctx, set))
	got, err := s.GetSet(ctx, set.SetID)
	require.NoError(t, err)
	require.Equal(t, set.Subjects, got.Subjects)

	set.Subjects = []string{"SOLUSDT"}
	set.Status = domain.SetStatusEnabled
	require.NoError(t, s.UpdateSet(ctx, set))
	got, err = s.GetSet(ctx, set.SetID)
	require.NoError(t, err)
	require.Equal(t, []string{"SOLUSDT"}, got.Subjects)
	require.Equal(t, domain.SetStatusPending, got.Status)

	sets, err := s.ListSets(ctx)
	require.NoError(t, err)
	require.Len(t, sets, 1)

	def := testFactorDef("factor_close", domain.FactorStatusDisabled)
	require.NoError(t, s.CreateFactor(ctx, def))
	def, err = s.GetFactor(ctx, def.FactorID)
	require.NoError(t, err)
	require.Equal(t, "set_prices", def.SetID)
	require.NotZero(t, def.CreatedAt)
	require.NotZero(t, def.UpdatedAt)
	def.LookbackPeriods = 10
	require.NoError(t, s.UpdateFactor(ctx, def))
	def, err = s.GetFactor(ctx, def.FactorID)
	require.NoError(t, err)
	require.Equal(t, 10, def.LookbackPeriods)

	defs, err := s.ListFactors(ctx, "set_prices", domain.FactorStatusDisabled)
	require.NoError(t, err)
	require.Len(t, defs, 1)
	require.NoError(t, s.DeleteFactor(ctx, def.FactorID))
	require.NoError(t, s.DeleteSet(ctx, set.SetID))
}

func TestLifecycleStatusWritesUseExpectedStatus(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	set := testSet("set_prices", domain.SetStatusPending)
	require.NoError(t, s.CreateSet(ctx, set))
	require.NoError(t, s.SetSetStatus(ctx, set.SetID, domain.SetStatusPending, domain.SetStatusEnabled))
	require.ErrorIs(t, s.SetSetStatus(ctx, set.SetID, domain.SetStatusPending, domain.SetStatusDisabled), ErrConflict)
	storedSet, err := s.GetSet(ctx, set.SetID)
	require.NoError(t, err)
	require.Equal(t, domain.SetStatusEnabled, storedSet.Status)

	factor := testFactorDef("factor_close", domain.FactorStatusDisabled)
	require.NoError(t, s.CreateFactor(ctx, factor))
	require.NoError(t, s.SetFactorStatus(ctx, factor.FactorID, domain.FactorStatusDisabled, domain.FactorStatusEnabled))
	require.ErrorIs(t, s.SetFactorStatus(ctx, factor.FactorID, domain.FactorStatusDisabled, domain.FactorStatusDisabled), ErrConflict)
	storedFactor, err := s.GetFactor(ctx, factor.FactorID)
	require.NoError(t, err)
	require.Equal(t, domain.FactorStatusEnabled, storedFactor.Status)
}

func TestUpdateAndDeleteFactorRequireDisabled(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateSet(ctx, testSet("set_prices", domain.SetStatusEnabled)))
	factor := testFactorDef("factor_close", domain.FactorStatusEnabled)
	require.NoError(t, s.CreateFactor(ctx, factor))

	factor.SourceCode = "changed"
	require.ErrorContains(t, s.UpdateFactor(ctx, factor), "disabled")
	require.ErrorContains(t, s.DeleteFactor(ctx, factor.FactorID), "disabled")
	stored, err := s.GetFactor(ctx, factor.FactorID)
	require.NoError(t, err)
	require.NotEqual(t, "changed", stored.SourceCode)
}
