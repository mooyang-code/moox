package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
		FactorID: "Bias", InputColumns: []string{"close"}, Outputs: []string{"bias"},
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
		FactorID: "Generic", InputColumns: []string{"value"}, Outputs: []string{"value"},
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
	require.ErrorContains(t, err, "must match factor file name")

	options.Outputs = []string{"changed"}
	_, err = svc.ImportFactorFile(context.Background(), path, options)
	require.ErrorContains(t, err, "immutable")
	stored, err := repo.Get(context.Background(), "Generic")
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
		FactorID: "Generic", Name: "Generic", SourceCode: "old", SourceHash: "old",
		InputColumns: []string{"value"}, Outputs: []string{"value"}, ParamsJSON: `{}`,
		LookbackPeriods: 2, Status: domain.FactorStatusEnabled,
	}))
	svc := NewService(repo, nil, Options{FactorsDir: dir})

	_, err = svc.ImportFactorFile(context.Background(), path, ImportOptions{
		FactorID: "Generic", InputColumns: []string{"value"}, Outputs: []string{"value"},
		ParamsJSON: `{"window":2}`, LookbackPeriods: 3,
	})
	require.ErrorContains(t, err, "disable")
	stored, err := repo.Get(context.Background(), "Generic")
	require.NoError(t, err)
	require.Equal(t, domain.FactorStatusEnabled, stored.Status)
	require.Equal(t, "old", stored.SourceCode)
	require.Equal(t, 2, stored.LookbackPeriods)
}

func TestEnsureSourceArtifactsRestoresEnabledFactorAfterDeployReplacement(t *testing.T) {
	dir := t.TempDir()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(factorschema.AllSQL()).Error)
	repo := store.NewFactorRepository(db)
	source := "def compute(df, params):\n    return df\n"
	factor, err := domain.NormalizeFactorDefinition(domain.FactorDef{
		FactorID: "Bias", Name: "Bias", SourceCode: source,
		InputColumns: []string{"close"}, Outputs: []string{"bias"}, ParamsJSON: `{}`,
		LookbackPeriods: 5, Status: domain.FactorStatusEnabled,
	})
	require.NoError(t, err)
	sourceSum := sha256.Sum256([]byte(factor.SourceCode))
	factor.SourceHash = hex.EncodeToString(sourceSum[:])
	require.NoError(t, repo.Create(context.Background(), factor))
	before, err := repo.Get(context.Background(), "Bias")
	require.NoError(t, err)

	svc := NewService(repo, nil, Options{FactorsDir: dir})
	require.NoError(t, svc.EnsureSourceArtifacts(context.Background()))

	stored, err := repo.Get(context.Background(), "Bias")
	require.NoError(t, err)
	require.Equal(t, domain.FactorStatusEnabled, stored.Status)
	require.Equal(t, factor.SourceHash, stored.SourceHash)
	require.Equal(t, before.ModifyTime, stored.ModifyTime)
	require.FileExists(t, stored.SourcePath)
	require.Contains(t, stored.SourcePath, filepath.Join(".versions", "factor", "Bias", factor.SourceHash))
}

func TestResultDataset(t *testing.T) {
	require.Equal(t, "dataset_foo_kline_factor", ResultDataset("foo_kline"))
	require.Equal(t, "dataset_crypto_spot_kline_1m_factor", ResultDatasetForView("dataset_binance_spot_kline_1m", "view_crypto_spot_kline_1m"))
	require.Equal(t, "view_crypto_spot_kline_1m_factor", ResultViewForView("dataset_binance_spot_kline_1m", "view_crypto_spot_kline_1m"))
	require.Equal(t, "dataset_foo_view_factor", ResultDataset("foo_view"))
	require.Equal(t, ResultDataset("bars"), ResultDatasetForView("bars", "view_bars"))
	require.NotEqual(t, ResultDatasetForView("bars", "view_a"), ResultDatasetForView("bars", "view_b"))
	require.LessOrEqual(t, len(ResultDatasetForView("bars", "view_a")), 50)
	require.LessOrEqual(t, len(ResultViewForView("bars", "view_a")), 50)
	require.NotEqual(t, ResultDataset("foo"), ResultDataset("foo_kline"))
	require.NotEqual(t, ResultDataset("same-long-readable-prefix-one"), ResultDataset("same-long-readable-prefix-two"))
	require.NotEqual(t, ResultDataset("same-long-prefix-138"), ResultDataset("same-long-prefix-489"))
	require.LessOrEqual(t, len(ResultDataset("same-long-prefix-138")), 50)
	require.LessOrEqual(t, len(ResultView("same-long-prefix-138")), 50)
}
