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
	return domain.MustDefinition(factorID, factorID, "outputs:\n  "+factorID+": close\n")
}

// changedSpec 返回改了表达式的同一个定义（输出列不变）。
func changedSpec(def domain.FactorDef, spec string) domain.FactorDef {
	changed := domain.MustDefinition(def.FactorID, def.Name, spec)
	changed.CreatedAt = def.CreatedAt
	return changed
}

// addTestMember creates the definition if needed and attaches it to the set with the given status.
func addTestMember(t *testing.T, s *Store, setID, factorID, status string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.GetFactor(ctx, factorID); err != nil {
		require.NoError(t, s.CreateFactor(ctx, testFactorDef(factorID)))
	}
	_, err := s.AddMember(ctx, setID, factorID, factorID)
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

// 同一个源数据集与频率可以有多个集合（各策略实例的私有集合）；集合 ID 与结果数据集仍各自唯一。
func TestCreateSetAllowsSharedSourceButUniqueResult(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateSet(ctx, testSet("set_prices", domain.SetStatusEnabled)))

	private := testSet("set_prices_private", domain.SetStatusPending)
	private.ResultDatasetID, private.OwnerInstanceID = "dataset_factor_private", "inst-1"
	require.NoError(t, s.CreateSet(ctx, private))
	owned, err := s.GetSetByOwner(ctx, "inst-1")
	require.NoError(t, err)
	require.Equal(t, "set_prices_private", owned.SetID)

	duplicate := testSet("set_prices_duplicate", domain.SetStatusPending)
	duplicate.ResultDatasetID = "dataset_factor_private"
	require.ErrorIs(t, s.CreateSet(ctx, duplicate), ErrConflict)
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
	def = changedSpec(def, "outputs:\n  factor_close: ts_mean(close, 10)\n")
	require.NoError(t, s.UpdateFactor(ctx, def))
	def, err = s.GetFactor(ctx, def.FactorID)
	require.NoError(t, err)
	require.Equal(t, 10, def.LookbackPeriods, "读取时编译得到派生字段")
	require.Equal(t, domain.SpecHash(def.Spec), def.SpecHash)

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
	_, err = s.AddMember(ctx, set.SetID, factor.FactorID, factor.FactorID)
	require.NoError(t, err)
	require.NoError(t, s.SetMemberStatus(ctx, set.SetID, factor.FactorID, domain.MemberStatusDisabled, domain.MemberStatusEnabled))
	require.ErrorIs(t, s.SetMemberStatus(ctx, set.SetID, factor.FactorID, domain.MemberStatusDisabled, domain.MemberStatusDisabled), ErrConflict)
	member, err := s.GetMember(ctx, set.SetID, factor.FactorID)
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusEnabled, member.Status)
}
