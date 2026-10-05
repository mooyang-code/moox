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

func testFactorDef(factorID string) domain.FactorDef {
	return domain.FactorDef{
		FactorID: factorID, Name: factorID,
		FactorType: domain.FactorTypeTimeSeries, SourceCode: "def compute(): return 1",
		SourceHash: "hash-" + factorID, InputColumns: []string{"close"}, Outputs: []string{factorID},
		ParamsJSON: "{}", LookbackPeriods: 1,
	}
}

// addTestMember creates the definition if needed and attaches it to the set with the given status.
func addTestMember(t *testing.T, s *Store, setID, factorID, status string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.GetFactor(ctx, factorID); err != nil {
		require.NoError(t, s.CreateFactor(ctx, testFactorDef(factorID)))
	}
	_, err := s.AddMember(ctx, setID, factorID)
	require.NoError(t, err)
	if status == domain.MemberStatusEnabled {
		require.NoError(t, s.SetMemberStatus(ctx, setID, factorID, domain.MemberStatusDisabled, domain.MemberStatusEnabled))
	}
}

func TestCreateFactorsRollsBackCatalogImportOnConflict(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateSet(ctx, testSet("set_prices", domain.SetStatusEnabled)))
	factor := testFactorDef("factor_close")
	err := s.CreateFactors(ctx, []domain.FactorDef{factor, factor})
	require.ErrorIs(t, err, ErrConflict)
	_, err = s.GetFactor(ctx, factor.FactorID)
	require.Error(t, err)
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

func TestEnabledSetByDatasetReturnsOnlyEnabledMembers(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateSet(ctx, testSet("set_prices", domain.SetStatusEnabled)))
	addTestMember(t, s, "set_prices", "z_factor", domain.MemberStatusEnabled)
	addTestMember(t, s, "set_prices", "a_factor", domain.MemberStatusEnabled)
	addTestMember(t, s, "set_prices", "disabled_factor", domain.MemberStatusDisabled)

	set, defs, found, err := s.EnabledSetByDataset(ctx, "crypto", "dataset_prices", "1m")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "set_prices", set.SetID)
	require.Equal(t, []string{"a_factor", "z_factor"}, []string{defs[0].FactorID, defs[1].FactorID})

	_, defs, found, err = s.EnabledSetByDataset(ctx, "crypto", "dataset_prices", "5m")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, defs)
}

func TestDeleteSetFailsWhenMembersExist(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateSet(ctx, testSet("set_prices", domain.SetStatusDisabled)))
	addTestMember(t, s, "set_prices", "factor_close", domain.MemberStatusDisabled)

	require.ErrorIs(t, s.DeleteSet(ctx, "set_prices"), ErrConflict)
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

	def := testFactorDef("factor_close")
	require.NoError(t, s.CreateFactor(ctx, def))
	def, err = s.GetFactor(ctx, def.FactorID)
	require.NoError(t, err)
	require.NotZero(t, def.CreatedAt)
	require.NotZero(t, def.UpdatedAt)
	def.LookbackPeriods = 10
	require.NoError(t, s.UpdateFactor(ctx, def))
	def, err = s.GetFactor(ctx, def.FactorID)
	require.NoError(t, err)
	require.Equal(t, 10, def.LookbackPeriods)

	defs, err := s.ListFactors(ctx)
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

	factor := testFactorDef("factor_close")
	require.NoError(t, s.CreateFactor(ctx, factor))
	_, err = s.AddMember(ctx, set.SetID, factor.FactorID)
	require.NoError(t, err)
	require.NoError(t, s.SetMemberStatus(ctx, set.SetID, factor.FactorID, domain.MemberStatusDisabled, domain.MemberStatusEnabled))
	require.ErrorIs(t, s.SetMemberStatus(ctx, set.SetID, factor.FactorID, domain.MemberStatusDisabled, domain.MemberStatusDisabled), ErrConflict)
	member, err := s.GetMember(ctx, set.SetID, factor.FactorID)
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusEnabled, member.Status)
}
