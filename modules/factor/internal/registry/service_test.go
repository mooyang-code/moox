package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestImportFactorFileUsesExplicitGenericDefinition(t *testing.T) {
	dir := t.TempDir()
	source := "def compute(df, params): return {'bias': df['close']}\n"
	path := filepath.Join(dir, "Bias.py")
	require.NoError(t, os.WriteFile(path, []byte(source), 0o644))
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(factorschema.AllSQL()).Error)
	svc := NewService(store.NewFactorRepository(db), nil, Options{FactorsDir: dir})
	factor, err := svc.ImportFactorFile(context.Background(), path, ImportOptions{
		FactorID: "bias", InputColumns: []string{"close"}, Outputs: []string{"bias"},
		ParamsJSON: `{"window":20}`, LookbackPeriods: 20,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"close"}, factor.InputColumns)
	require.Equal(t, []string{"bias"}, factor.Outputs)
	require.Equal(t, 20, factor.LookbackPeriods)
	require.NotEmpty(t, factor.SourcePath)
	require.Equal(t, "disabled", factor.Status)
}

func TestImportFactorFileUpdatesMutableFieldsButRejectsNameOrOutputChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Generic.py")
	require.NoError(t, os.WriteFile(path, []byte("def compute(df, params): return {'value': df['value']}\n"), 0o644))
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(factorschema.AllSQL()).Error)
	repo := store.NewFactorRepository(db)
	svc := NewService(repo, nil, Options{FactorsDir: dir})
	options := ImportOptions{
		FactorID: "generic", InputColumns: []string{"value"}, Outputs: []string{"value"},
		ParamsJSON: `{}`, LookbackPeriods: 2,
	}
	_, err = svc.ImportFactorFile(context.Background(), path, options)
	require.NoError(t, err)

	options.LookbackPeriods = 3
	updated, err := svc.ImportFactorFile(context.Background(), path, options)
	require.NoError(t, err)
	require.Equal(t, 3, updated.LookbackPeriods)
	require.Equal(t, "disabled", updated.Status)

	renamedPath := filepath.Join(dir, "Renamed.py")
	require.NoError(t, os.WriteFile(renamedPath, []byte("def compute(df, params): return {'value': df['value']}\n"), 0o644))
	_, err = svc.ImportFactorFile(context.Background(), renamedPath, options)
	require.ErrorContains(t, err, "name is immutable")

	options.Outputs = []string{"changed"}
	_, err = svc.ImportFactorFile(context.Background(), path, options)
	require.ErrorContains(t, err, "immutable")
	stored, err := repo.Get(context.Background(), "generic")
	require.NoError(t, err)
	require.Equal(t, []string{"value"}, stored.Outputs)
}

func TestImportFactorFileRejectsEnabledDefinitionUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Generic.py")
	require.NoError(t, os.WriteFile(path, []byte("def compute(df, params): return {'value': df['value']}\n"), 0o644))
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(factorschema.AllSQL()).Error)
	repo := store.NewFactorRepository(db)
	require.NoError(t, repo.Create(context.Background(), domain.FactorDef{
		FactorID: "generic", Name: "Generic", SourceCode: "old", SourceHash: "old",
		InputColumns: []string{"value"}, Outputs: []string{"value"}, ParamsJSON: `{}`,
		LookbackPeriods: 2, Status: domain.FactorStatusEnabled,
	}))
	svc := NewService(repo, nil, Options{FactorsDir: dir})

	_, err = svc.ImportFactorFile(context.Background(), path, ImportOptions{
		FactorID: "generic", InputColumns: []string{"value"}, Outputs: []string{"value"},
		ParamsJSON: `{"window":2}`, LookbackPeriods: 3,
	})
	require.ErrorContains(t, err, "disable")
	stored, err := repo.Get(context.Background(), "generic")
	require.NoError(t, err)
	require.Equal(t, domain.FactorStatusEnabled, stored.Status)
	require.Equal(t, "old", stored.SourceCode)
	require.Equal(t, 2, stored.LookbackPeriods)
}

func TestResultDataset(t *testing.T) {
	require.Equal(t, "foo_kline_factor", ResultDataset("foo_kline"))
	require.NotEqual(t, ResultDataset("foo"), ResultDataset("foo_kline"))
	require.NotEqual(t, ResultDataset("same-long-readable-prefix-one"), ResultDataset("same-long-readable-prefix-two"))
	require.NotEqual(t, ResultDataset("same-long-prefix-138"), ResultDataset("same-long-prefix-489"))
	require.LessOrEqual(t, len(ResultDataset("same-long-prefix-138")), 20)
}
