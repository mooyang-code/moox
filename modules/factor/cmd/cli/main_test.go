package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
)

func TestParseImport(t *testing.T) {
	cfg, err := parseArgs([]string{"import", "--task", "ftask_bars_1m", "--file", "./xbx.yaml", "--factor-id", "bias"})
	require.NoError(t, err)
	require.Equal(t, "ftask_bars_1m", cfg.TaskID)
	require.Equal(t, "./xbx.yaml", cfg.File)
	require.Equal(t, "bias", cfg.FactorID)

	alone, err := parseArgs([]string{"import", "--file", "./xbx.yaml"})
	require.NoError(t, err)
	require.Empty(t, alone.TaskID)

	for _, retired := range []string{"--inputs", "--outputs", "--lookback", "--params", "--factor-type"} {
		_, err := parseArgs([]string{"import", "--file", "./xbx.yaml", retired, "x"})
		require.Error(t, err, retired)
	}
}

func TestParseImportCatalogUsesDirectoryAndTask(t *testing.T) {
	cfg, err := parseArgs([]string{"import-catalog", "--task", "ftask_bars_1m", "--dir", "/opt/factors"})
	require.NoError(t, err)
	require.Equal(t, "ftask_bars_1m", cfg.TaskID)
	require.Equal(t, "/opt/factors", cfg.CatalogDir)
}

func TestParseImportCatalogDefaultsToFactorsDirectory(t *testing.T) {
	cfg, err := parseArgs([]string{"import-catalog"})
	require.NoError(t, err)
	require.Equal(t, "./factors", cfg.CatalogDir)
}

func TestParseRecalcRangeAndSelectors(t *testing.T) {
	cfg, err := parseArgs([]string{
		"recalc", "--task", "ftask_bars_1m", "--start", "2026-10-04T00:00:00Z",
		"--end", "2026-10-04T01:00:00Z", "--member", "bias", "--member", "cci",
		"--subject", "BTC,ETH",
	})
	require.NoError(t, err)
	require.Equal(t, time.Hour, cfg.EndTime.Sub(cfg.StartTime))
	require.Equal(t, []string{"bias", "cci"}, cfg.Members)
	require.Equal(t, []string{"BTC", "ETH"}, cfg.Subjects)
	require.Empty(t, cfg.DBPath)
}

func TestStatusRequiresFactorTarget(t *testing.T) {
	_, err := parseArgs([]string{"status"})
	require.NoError(t, err)
	var output bytes.Buffer
	err = runStatus(t.Context(), cliConfig{}, &output)
	require.ErrorContains(t, err, "FactorMgr target is required")
}

func TestLegacyCLICommandsAreRemoved(t *testing.T) {
	for _, command := range []string{"recalc-cancel", "recalc-status", "clear-queue", "replay", "run-once"} {
		_, err := parseArgs([]string{command})
		require.ErrorContains(t, err, "unknown command")
	}
}

func TestRunInitCreatesFactorSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "factor.db")
	var output bytes.Buffer
	require.NoError(t, runInit(cliConfig{DBPath: dbPath}, &output))
	var result struct {
		OK     bool     `json:"ok"`
		Tables []string `json:"tables"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.True(t, result.OK)
	require.Equal(t, []string{"t_factor_tasks", "t_factor_defs", "t_factor_task_members", "t_factor_recalc_jobs"}, result.Tables)
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

const testLibrary = `- factor_id: bias
  name: bias
  spec: |
    params:
      window: {type: int, min: 2, max: 60, default: 5}
    output: close / ts_mean(close, window)
- factor_id: cci
  name: CCI
  spec: |
    output: close
`

func TestImportCatalogCreatesDefinitionsOnly(t *testing.T) {
	root := t.TempDir()
	factorsDir := filepath.Join(root, "factors")
	require.NoError(t, os.MkdirAll(factorsDir, 0o755))
	// The library has no set or status field: it only describes definitions.
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "lib.yaml"), []byte(testLibrary), 0o600))
	dbPath := filepath.Join(root, "factor.db")
	configPath := filepath.Join(root, "app.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("database:\n  path: "+dbPath+"\n"), 0o600))

	var output bytes.Buffer
	require.NoError(t, runImportCatalog(t.Context(), cliConfig{ConfigPath: configPath, CatalogDir: factorsDir}, &output))
	var result struct {
		OK       bool `json:"ok"`
		Imported []struct {
			FactorID     string `json:"factor_id"`
			MemberStatus string `json:"member_status"`
		} `json:"imported"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.True(t, result.OK)
	require.Len(t, result.Imported, 2)
	require.Equal(t, "bias", result.Imported[0].FactorID)
	require.Empty(t, result.Imported[0].MemberStatus)

	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	defs, err := db.ListFactors(t.Context())
	require.NoError(t, err)
	require.Len(t, defs, 2)
	require.Equal(t, []string{"bias"}, defs[0].Outputs)
	usages, err := db.ListUsages(t.Context(), "bias")
	require.NoError(t, err)
	require.Empty(t, usages["bias"])
	require.NoError(t, db.Close())

	output.Reset()
	err = runImportCatalog(t.Context(), cliConfig{ConfigPath: configPath, CatalogDir: factorsDir}, &output)
	require.ErrorContains(t, err, "already exists", "重复导入不覆盖已有定义")
}

func TestImportSelectsOneDefinitionFromFile(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "lib.yaml")
	require.NoError(t, os.WriteFile(library, []byte(testLibrary), 0o600))
	dbPath := filepath.Join(root, "factor.db")
	configPath := filepath.Join(root, "app.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("database:\n  path: "+dbPath+"\n"), 0o600))

	var output bytes.Buffer
	err := runImport(t.Context(), cliConfig{ConfigPath: configPath, File: library}, &output)
	require.ErrorContains(t, err, "--factor-id", "文件里有多个定义时必须指定")
	err = runImport(t.Context(), cliConfig{ConfigPath: configPath, File: library, FactorID: "Missing"}, &output)
	require.ErrorContains(t, err, "没有因子 Missing")
	require.NoError(t, runImport(t.Context(), cliConfig{ConfigPath: configPath, File: library, FactorID: "cci"}, &output))
	require.Contains(t, output.String(), `"definition_hash":"sha256:`)
}

func TestParsePurgeDatasetRequiresSpaceAndDataset(t *testing.T) {
	if _, err := parseArgs([]string{"purge-dataset", "--space", "crypto"}); err == nil {
		t.Fatal("缺少 --dataset 应被拒绝")
	}
	cfg, err := parseArgs([]string{"purge-dataset", "--space", "crypto", "--dataset", "dataset_factor_x", "--yes"})
	if err != nil || cfg.SpaceID != "crypto" || cfg.DatasetID != "dataset_factor_x" || !cfg.Yes {
		t.Fatalf("解析不符：%+v err=%v", cfg, err)
	}
	if err := runPurgeDataset(context.Background(), cliConfig{SpaceID: "crypto", DatasetID: "d"}, io.Discard); err == nil {
		t.Fatal("没有 --yes 应被拒绝")
	}
}
