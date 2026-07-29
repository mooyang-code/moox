package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func TestRunImportRejectsEnabledDefinitionUpdate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "factor.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "old", SourceHash: "old",
		InputColumns: []string{"close"}, Outputs: []string{"bias"}, ParamsJSON: "{}",
		LookbackPeriods: 2, Status: domain.FactorStatusEnabled,
	}))
	require.NoError(t, db.Close())

	sourcePath := filepath.Join(dir, "Bias.py")
	require.NoError(t, os.WriteFile(sourcePath, []byte("def compute(df, params): return df\n"), 0o644))
	err = runImport(context.Background(), cliConfig{
		DBPath: dbPath, FactorsDir: filepath.Join(dir, "factors"), File: sourcePath,
		FactorID: "bias", InputColumns: []string{"close"}, Outputs: []string{"bias"},
		ParamsJSON: "{}", LookbackPeriods: 3,
	}, &bytes.Buffer{})
	require.ErrorContains(t, err, "disable")
}
