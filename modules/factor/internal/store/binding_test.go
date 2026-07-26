package store

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFactorRepositoryRoundTrip(t *testing.T) {
	repo := NewFactorRepository(openTestDB(t))
	factor := testFactor("bias", domain.FactorStatusEnabled)
	require.NoError(t, repo.Upsert(context.Background(), factor))
	got, err := repo.Get(context.Background(), factor.FactorID)
	require.NoError(t, err)
	require.Equal(t, []int{20, 96}, got.Periods)
	require.Equal(t, []string{"funding_rate"}, got.Depends)
}

func TestListExecutableExcludesDisabledFactor(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, NewFactorRepository(db).Upsert(context.Background(), testFactor("bias", domain.FactorStatusDisabled)))
	require.NoError(t, NewBindingRepository(db).Upsert(context.Background(), domain.FactorBinding{
		BindingID: "bind-bias", FactorID: "bias", SpaceID: "crypto",
		SourceDataset: "bars", Freq: "1m", SubjectMode: domain.SubjectModeAll,
		SubjectsJSON: "[]", TargetDataset: "bars_factor", Status: domain.BindingStatusEnabled,
	}))
	rows, err := NewBindingRepository(db).ListExecutable(context.Background())
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, NewFactorRepository(db).SetStatus(context.Background(), "bias", domain.FactorStatusEnabled))
	rows, err = NewBindingRepository(db).ListExecutable(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(factorschema.AllSQL()).Error)
	return db
}

func testFactor(id, status string) domain.FactorDef {
	return domain.FactorDef{
		FactorID: id, Name: "Factor_" + id, SourceCode: "def signal(): pass",
		SourceHash: "hash", Periods: []int{20, 96}, LookbackBars: 200,
		Depends: []string{"funding_rate"}, Status: status,
	}
}
