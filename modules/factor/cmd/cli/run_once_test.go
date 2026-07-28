package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestRunInitAndImport(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "factor.db")
	factorsDir := filepath.Join(tmp, "factors")
	require.NoError(t, os.MkdirAll(factorsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "Bias.py"), []byte(
		"def compute(df, params):\n    return {'bias': df['close']}\n",
	), 0o644))
	var out bytes.Buffer
	require.NoError(t, run(context.Background(), []string{"init", "--db", dbPath}, &out))
	out.Reset()
	require.NoError(t, run(context.Background(), []string{
		"import", "--db", dbPath, "--factors-dir", factorsDir,
		"--file", filepath.Join(factorsDir, "Bias.py"), "--factor-id", "bias",
		"--input-columns", "close", "--outputs", "bias", "--params-json", "{}",
		"--lookback-rows", "20", "--status", "enabled",
	}, &out))
	require.Contains(t, out.String(), `"ok":true`)
}

func TestRunOnceRequiresRange(t *testing.T) {
	err := runOnce(context.Background(), cliConfig{}, &bytes.Buffer{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "required")
}

func TestExecutableFactorGroupsHonorBindingScope(t *testing.T) {
	factors := []domain.FactorDef{{FactorID: "a"}, {FactorID: "b"}}
	bindings := []domain.FactorBinding{
		{FactorID: "a", SpaceID: "crypto", SourceDataset: "bars", Freq: "1m", SubjectMode: domain.SubjectModeAll, TargetDataset: "custom"},
		{FactorID: "b", SpaceID: "crypto", SourceDataset: "bars", Freq: "1m", SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["ETH"]`},
	}
	groups := executableFactorGroups(factors, bindings, cliConfig{
		SpaceID: "crypto", DatasetID: "bars", SubjectID: "BTC", Freq: "1m",
	})
	require.Equal(t, map[string][]domain.FactorDef{"custom": {{FactorID: "a"}}}, groups)
}
