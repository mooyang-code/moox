package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRunImportRejectsEnabledDefinitionUpdate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "factor.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{FactorType: "timeseries",
		FactorID: "Bias", Name: "Bias", SourceCode: "old", SourceHash: "old",
		InputColumns: []string{"close"}, Outputs: []string{"bias"}, ParamsJSON: "{}",
		LookbackPeriods: 2, Status: domain.FactorStatusEnabled,
	}))
	require.NoError(t, db.Close())

	sourcePath := filepath.Join(dir, "Bias.py")
	require.NoError(t, os.WriteFile(sourcePath, []byte("def compute(df, params): return df\n"), 0o644))
	err = runImport(context.Background(), cliConfig{
		DBPath: dbPath, FactorsDir: filepath.Join(dir, "factors"), File: sourcePath,
		FactorID: "Bias", FactorType: "timeseries", InputColumns: []string{"close"}, Outputs: []string{"bias"},
		ParamsJSON: "{}", LookbackPeriods: 3,
	}, &bytes.Buffer{})
	require.ErrorContains(t, err, "disable")
}

func TestParseImportCatalog(t *testing.T) {
	cfg, err := parseArgs([]string{"import-catalog", "--db", "/tmp/factor.db", "--factors-dir", "/tmp/factors", "--catalog", "/tmp/catalog.json"})
	require.NoError(t, err)
	require.Equal(t, "import-catalog", cfg.Command)
	require.Equal(t, "/tmp/catalog.json", cfg.CatalogPath)
}

func TestImportRequiresExplicitFactorType(t *testing.T) {
	for _, factorType := range []string{"", "bad", "timeseries", "cross_section"} {
		t.Run(factorType, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "Typed.py")
			require.NoError(t, os.WriteFile(path, []byte("def compute(df, params): return df\n"), 0o644))
			cfg := cliConfig{DBPath: filepath.Join(dir, "db"), File: path, FactorID: "Typed", FactorType: factorType, InputColumns: []string{"close"}, Outputs: []string{"value"}, LookbackPeriods: 1}
			err := runImport(context.Background(), cfg, &bytes.Buffer{})
			if factorType == "" || factorType == "bad" {
				require.ErrorContains(t, err, "factor_type")
				return
			}
			require.NoError(t, err)
			db, err := store.Open(&store.Options{Path: cfg.DBPath})
			require.NoError(t, err)
			defer db.Close()
			factor, err := db.Factors().Get(context.Background(), "Typed")
			require.NoError(t, err)
			require.Equal(t, factorType, factor.FactorType)
		})
	}
}

func TestRunImportCatalogValidatesAllEntriesBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	factorsDir := filepath.Join(dir, "factors")
	require.NoError(t, os.MkdirAll(factorsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "First.py"), []byte("def compute(df, params):\n    return df\n"), 0o644))
	catalog := []map[string]any{
		{"file": "First.py", "factor_type": "timeseries", "factor_id": "First", "input_columns": []string{"close"}, "outputs": []string{"first"}, "lookback_periods": 1},
		{"file": "Second.py", "factor_type": "timeseries", "factor_id": "second", "input_columns": []string{}, "outputs": []string{"second"}, "lookback_periods": 1},
	}
	catalogPath := filepath.Join(dir, "catalog.json")
	raw, err := json.Marshal(catalog)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(catalogPath, raw, 0o644))
	dbPath := filepath.Join(dir, "factor.db")
	var output bytes.Buffer
	err = runImportCatalog(context.Background(), cliConfig{DBPath: dbPath, FactorsDir: factorsDir, CatalogPath: catalogPath}, &output)
	require.Error(t, err)
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	_, err = db.Factors().Get(context.Background(), "First")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, db.Close())
}

func TestRunImportCatalogRollsBackWhenBatchMutationFails(t *testing.T) {
	dir := t.TempDir()
	factorsDir := filepath.Join(dir, "factors")
	require.NoError(t, os.MkdirAll(factorsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "First.py"), []byte("def compute(df, params):\n    return df\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "Bias.py"), []byte("def compute(df, params):\n    return df\n"), 0o644))
	dbPath := filepath.Join(dir, "factor.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{FactorType: "timeseries",
		FactorID: "Bias", Name: "Bias", SourceCode: "old", SourceHash: "old",
		InputColumns: []string{"close"}, Outputs: []string{"bias"}, ParamsJSON: "{}",
		LookbackPeriods: 2, Status: domain.FactorStatusEnabled,
	}))
	require.NoError(t, db.Close())
	catalog := []map[string]any{
		{"file": "First.py", "factor_type": "timeseries", "factor_id": "First", "input_columns": []string{"close"}, "outputs": []string{"first"}, "lookback_periods": 1},
		{"file": "Bias.py", "factor_type": "timeseries", "factor_id": "Bias", "input_columns": []string{"close"}, "outputs": []string{"bias"}, "lookback_periods": 2},
	}
	raw, err := json.Marshal(catalog)
	require.NoError(t, err)
	catalogPath := filepath.Join(dir, "catalog.json")
	require.NoError(t, os.WriteFile(catalogPath, raw, 0o644))
	var output bytes.Buffer
	err = runImportCatalog(context.Background(), cliConfig{DBPath: dbPath, FactorsDir: factorsDir, CatalogPath: catalogPath}, &output)
	require.ErrorContains(t, err, `disable factor "Bias"`)
	check, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, check.ApplySchema(factorschema.AllSQL()))
	_, err = check.Factors().Get(context.Background(), "First")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, check.Close())
}
